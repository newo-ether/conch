package handler

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/newo-ether/conch/crypto"
)

const TransferChunkBytes = 256 << 10
const TransferMaxBytes int64 = 1 << 30

// FileTransferHandler keeps binary payloads inside the authenticated file envelope.
// Read actions are stateless. The client verifies the complete source before uploading.
type FileTransferHandler struct {
	APIKey    []byte
	KeyPair   *crypto.KeyPair
	Transfers *FileTransfers
}

type transferRequest struct {
	Path      string `json:"path"`
	ID        string `json:"id"`
	Offset    int64  `json:"offset"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	Data      string `json:"data"`
	Overwrite bool   `json:"overwrite"`
}

type transferResult struct {
	OK         bool   `json:"ok"`
	ID         string `json:"id,omitempty"`
	Path       string `json:"path,omitempty"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256,omitempty"`
	Offset     int64  `json:"offset"`
	Data       string `json:"data,omitempty"`
	State      string `json:"state,omitempty"`
	ChunkBytes int    `json:"chunk_bytes,omitempty"`
	MaxBytes   int64  `json:"max_bytes,omitempty"`
}

func (h *FileTransferHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Defense in depth also bounds callers that mount the handler without auth middleware.
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20+1))
	if err != nil || len(body) > 1<<20 {
		http.Error(w, "transfer request exceeds 1MB", http.StatusRequestEntityTooLarge)
		return
	}
	plain, key, err := decryptBody(r, body, h.APIKey, h.KeyPair)
	if err != nil {
		writePreDispatchError(w, r, h.APIKey, err.Error())
		return
	}
	var req transferRequest
	if err = json.Unmarshal(plain, &req); err != nil {
		writeJSONResponse(w, map[string]string{"error": "invalid transfer request"}, key)
		return
	}
	action := strings.TrimPrefix(r.URL.Path, "/file/transfer/")
	var result transferResult
	switch action {
	case "capabilities":
		result = transferResult{OK: true, ChunkBytes: TransferChunkBytes, MaxBytes: TransferMaxBytes}
	case "info":
		result, err = transferInfo(r.Context(), req.Path)
	case "read":
		result, err = transferRead(r.Context(), req)
	default:
		if h.Transfers == nil {
			err = fmt.Errorf("file transfer storage unavailable")
		} else {
			result, err = h.Transfers.apply(r.Context(), action, req)
		}
	}
	if err != nil {
		writeJSONResponse(w, map[string]string{"error": err.Error()}, key)
		return
	}
	writeJSONResponse(w, result, key)
}

func transferPath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("transfer path must be absolute")
	}
	return filepath.Abs(filepath.Clean(path))
}

func transferInfo(ctx context.Context, path string) (transferResult, error) {
	path, err := transferPath(path)
	if err != nil {
		return transferResult{}, err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return transferResult{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return transferResult{}, err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return transferResult{}, err
	}
	if !before.Mode().IsRegular() || before.Size() > TransferMaxBytes {
		return transferResult{}, fmt.Errorf("source must be a regular file of at most 1GiB")
	}
	hash := sha256.New()
	count, err := copyTransfer(ctx, hash, io.LimitReader(file, TransferMaxBytes+1))
	if err != nil {
		return transferResult{}, err
	}
	after, err := file.Stat()
	if err != nil {
		return transferResult{}, err
	}
	if count != before.Size() || count != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return transferResult{}, fmt.Errorf("source changed while hashing")
	}
	return transferResult{OK: true, Path: path, Size: count, SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}

func transferRead(ctx context.Context, req transferRequest) (transferResult, error) {
	path, err := transferPath(req.Path)
	if err != nil {
		return transferResult{}, err
	}
	if req.Offset < 0 || req.Size < 0 || req.Size > TransferMaxBytes || req.Offset > req.Size {
		return transferResult{}, fmt.Errorf("invalid transfer read range")
	}
	file, err := os.Open(path)
	if err != nil {
		return transferResult{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return transferResult{}, err
	}
	if !info.Mode().IsRegular() || info.Size() != req.Size {
		return transferResult{}, fmt.Errorf("source changed or is not a regular file")
	}
	if err := ctx.Err(); err != nil {
		return transferResult{}, err
	}
	data := make([]byte, min(int64(TransferChunkBytes), req.Size-req.Offset))
	if len(data) > 0 {
		if _, err := file.ReadAt(data, req.Offset); err != nil {
			return transferResult{}, err
		}
	}
	return transferResult{OK: true, Path: path, Size: req.Size, Offset: req.Offset, Data: base64.StdEncoding.EncodeToString(data)}, nil
}

func copyTransfer(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buffer := make([]byte, TransferChunkBytes)
	var count int64
	for {
		if err := ctx.Err(); err != nil {
			return count, err
		}
		n, readErr := src.Read(buffer)
		if n > 0 {
			written, err := dst.Write(buffer[:n])
			count += int64(written)
			if err != nil {
				return count, err
			}
			if written != n {
				return count, io.ErrShortWrite
			}
		}
		if readErr == io.EOF {
			return count, nil
		}
		if readErr != nil {
			return count, readErr
		}
	}
}

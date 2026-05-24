package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/newo-ether/conch/crypto"
)

const maxFileSize = 1 << 20 // 1 MB

// FileReadRequest is the decrypted body for POST /file/read.
type FileReadRequest struct {
	Path   string `json:"path"`
	Offset int64  `json:"offset"`
	Limit  int64  `json:"limit"`
}

type FileReadResponse struct {
	Content    string `json:"content"`
	Lines      int    `json:"lines"`
	TotalLines int    `json:"totalLines"`
}

// FileWriteRequest is the decrypted body for POST /file/write.
type FileWriteRequest struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type FileWriteResponse struct {
	OK bool `json:"ok"`
}

// FileGlobRequest is the decrypted body for POST /file/glob.
type FileGlobRequest struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
}

type FileGlobResponse struct {
	Files []string `json:"files"`
}

// FileGrepRequest is the decrypted body for POST /file/grep.
type FileGrepRequest struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
	Glob    string `json:"glob"`
}

type GrepMatch struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Content string `json:"content"`
}

type FileGrepResponse struct {
	Matches []GrepMatch `json:"matches"`
}

// decryptBody handles the shared encryption-detection logic.
func decryptBody(r *http.Request, bodyBytes []byte, apiKey []byte, keyPair *crypto.KeyPair) ([]byte, []byte, error) {
	var aesKey []byte
	var plaintext []byte

	if r.Header.Get("X-Encryption") == "v1" {
		clientPubKeyStr := r.Header.Get("X-Client-Public-Key")
		if clientPubKeyStr == "" {
			return nil, nil, fmt.Errorf("missing client public key")
		}
		clientPubKey, err := crypto.DecodePublicKey(clientPubKeyStr)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid client public key: %w", err)
		}
		aesKey, err = crypto.DeriveSharedSecret(keyPair.PrivateKey, clientPubKey)
		if err != nil {
			return nil, nil, fmt.Errorf("key derivation failed: %w", err)
		}
		plaintext, err = crypto.Decrypt(aesKey, string(bodyBytes))
		if err != nil {
			return nil, nil, fmt.Errorf("decryption failed: %w", err)
		}
	} else if len(apiKey) > 0 {
		return nil, nil, fmt.Errorf("encryption required")
	} else {
		plaintext = bodyBytes
	}

	return plaintext, aesKey, nil
}

func writeJSONResponse(w http.ResponseWriter, v any, aesKey []byte) {
	var data []byte
	if v != nil {
		data, _ = json.Marshal(v)
	}

	var body string
	if aesKey != nil {
		enc, err := crypto.Encrypt(aesKey, data)
		if err != nil {
			log.Printf("ERROR: encrypting response: %v", err)
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}
		body = enc
	} else {
		body = string(data)
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Write([]byte(body))
}

// FileReadHandler serves POST /file/read.
type FileReadHandler struct {
	APIKey  []byte
	KeyPair *crypto.KeyPair
}

func (h *FileReadHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
		return
	}

	plaintext, aesKey, err := decryptBody(r, bodyBytes, h.APIKey, h.KeyPair)
	if err != nil {
		writeJSONResponse(w, map[string]string{"error": err.Error()}, aesKey)
		return
	}

	var req FileReadRequest
	if err := json.Unmarshal(plaintext, &req); err != nil {
		writeJSONResponse(w, map[string]string{"error": "invalid json"}, aesKey)
		return
	}
	if req.Path == "" {
		writeJSONResponse(w, map[string]string{"error": "path is required"}, aesKey)
		return
	}
	if req.Limit <= 0 || req.Limit > maxFileSize {
		req.Limit = maxFileSize
	}
	if req.Offset < 0 {
		req.Offset = 0
	}

	f, err := os.Open(req.Path)
	if err != nil {
		writeJSONResponse(w, map[string]string{"error": fmt.Sprintf("open: %v", err)}, aesKey)
		return
	}
	defer f.Close()

	if req.Offset > 0 {
		if _, err := f.Seek(req.Offset, io.SeekStart); err != nil {
			writeJSONResponse(w, map[string]string{"error": fmt.Sprintf("seek: %v", err)}, aesKey)
			return
		}
	}

	buf := make([]byte, req.Limit)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		writeJSONResponse(w, map[string]string{"error": fmt.Sprintf("read: %v", err)}, aesKey)
		return
	}
	content := string(buf[:n])

	// Count lines in returned content
	lines := 0
	if len(content) > 0 {
		lines = 1
		for _, c := range content {
			if c == '\n' {
				lines++
			}
		}
	}

	resp := FileReadResponse{
		Content:    content,
		Lines:      lines,
		TotalLines: lines, // not scanning whole file for total
	}
	writeJSONResponse(w, resp, aesKey)
}

// FileWriteHandler serves POST /file/write.
type FileWriteHandler struct {
	APIKey  []byte
	KeyPair *crypto.KeyPair
}

func (h *FileWriteHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
		return
	}

	plaintext, aesKey, err := decryptBody(r, bodyBytes, h.APIKey, h.KeyPair)
	if err != nil {
		writeJSONResponse(w, map[string]string{"error": err.Error()}, aesKey)
		return
	}

	var req FileWriteRequest
	if err := json.Unmarshal(plaintext, &req); err != nil {
		writeJSONResponse(w, map[string]string{"error": "invalid json"}, aesKey)
		return
	}
	if req.Path == "" {
		writeJSONResponse(w, map[string]string{"error": "path is required"}, aesKey)
		return
	}
	if len(req.Content) > maxFileSize {
		writeJSONResponse(w, map[string]string{"error": "content exceeds 1MB limit"}, aesKey)
		return
	}

	if err := os.MkdirAll(filepath.Dir(req.Path), 0755); err != nil {
		writeJSONResponse(w, map[string]string{"error": fmt.Sprintf("mkdir: %v", err)}, aesKey)
		return
	}
	if err := os.WriteFile(req.Path, []byte(req.Content), 0644); err != nil {
		writeJSONResponse(w, map[string]string{"error": fmt.Sprintf("write: %v", err)}, aesKey)
		return
	}

	writeJSONResponse(w, FileWriteResponse{OK: true}, aesKey)
}

// FileGlobHandler serves POST /file/glob.
type FileGlobHandler struct {
	APIKey  []byte
	KeyPair *crypto.KeyPair
}

func (h *FileGlobHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
		return
	}

	plaintext, aesKey, err := decryptBody(r, bodyBytes, h.APIKey, h.KeyPair)
	if err != nil {
		writeJSONResponse(w, map[string]string{"error": err.Error()}, aesKey)
		return
	}

	var req FileGlobRequest
	if err := json.Unmarshal(plaintext, &req); err != nil {
		writeJSONResponse(w, map[string]string{"error": "invalid json"}, aesKey)
		return
	}
	if req.Pattern == "" {
		writeJSONResponse(w, map[string]string{"error": "pattern is required"}, aesKey)
		return
	}
	if req.Path == "" {
		req.Path, _ = os.UserHomeDir()
	}

	matches, err := filepath.Glob(filepath.Join(req.Path, req.Pattern))
	if err != nil {
		writeJSONResponse(w, map[string]string{"error": fmt.Sprintf("glob: %v", err)}, aesKey)
		return
	}

	if len(matches) > 1000 {
		matches = matches[:1000]
	}
	if matches == nil {
		matches = []string{}
	}

	writeJSONResponse(w, FileGlobResponse{Files: matches}, aesKey)
}

// FileGrepHandler serves POST /file/grep.
type FileGrepHandler struct {
	APIKey  []byte
	KeyPair *crypto.KeyPair
}

func (h *FileGrepHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
		return
	}

	plaintext, aesKey, err := decryptBody(r, bodyBytes, h.APIKey, h.KeyPair)
	if err != nil {
		writeJSONResponse(w, map[string]string{"error": err.Error()}, aesKey)
		return
	}

	var req FileGrepRequest
	if err := json.Unmarshal(plaintext, &req); err != nil {
		writeJSONResponse(w, map[string]string{"error": "invalid json"}, aesKey)
		return
	}
	if req.Pattern == "" {
		writeJSONResponse(w, map[string]string{"error": "pattern is required"}, aesKey)
		return
	}
	if req.Path == "" {
		req.Path, _ = os.UserHomeDir()
	}

	re, err := regexp.Compile(req.Pattern)
	if err != nil {
		writeJSONResponse(w, map[string]string{"error": fmt.Sprintf("regex compile: %v", err)}, aesKey)
		return
	}

	matches := make([]GrepMatch, 0)

	var globFunc func(string) bool
	if req.Glob != "" {
		globFunc = func(name string) bool {
			ok, _ := filepath.Match(req.Glob, filepath.Base(name))
			return ok
		}
	}

	err = filepath.Walk(req.Path, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if len(matches) >= 500 {
			return filepath.SkipAll
		}
		if globFunc != nil && !globFunc(path) {
			return nil
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		for i, line := range strings.Split(string(content), "\n") {
			if re.MatchString(line) {
				matches = append(matches, GrepMatch{
					Path:    path,
					Line:    i + 1,
					Content: line,
				})
				if len(matches) >= 500 {
					return filepath.SkipAll
				}
			}
		}
		return nil
	})

	if err != nil && err != filepath.SkipAll {
		writeJSONResponse(w, map[string]string{"error": fmt.Sprintf("walk: %v", err)}, aesKey)
		return
	}

	writeJSONResponse(w, FileGrepResponse{Matches: matches}, aesKey)
}

package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/newo-ether/conch/crypto"
)

func TestTransferEncryptedHTTPRoundTripAndReplayRejection(t *testing.T) {
	apiKey := []byte("transfer-encryption-fixture")
	pair, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	store := newTestTransfers(t)
	secured := AuthMiddleware(apiKey, crypto.NewNonceTracker())(&FileTransferHandler{
		APIKey: apiKey, KeyPair: pair, Transfers: store,
	})
	call := func(action string, payload transferRequest) transferResult {
		t.Helper()
		client, err := crypto.GenerateKeyPair()
		if err != nil {
			t.Fatal(err)
		}
		session, err := crypto.DeriveSharedSecret(client.PrivateKey, pair.PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		plain, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		body, err := crypto.Encrypt(session, plain)
		if err != nil {
			t.Fatal(err)
		}
		nonce, err := crypto.GenerateNonce()
		if err != nil {
			t.Fatal(err)
		}
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		request := httptest.NewRequest(http.MethodPost, "/file/transfer/"+action, strings.NewReader(body))
		request.Header.Set("X-Encryption", "v2")
		request.Header.Set("X-Client-Public-Key", client.PublicKeyBase64())
		request.Header.Set("X-Timestamp", ts)
		request.Header.Set("X-Nonce", nonce)
		request.Header.Set("X-Signature", crypto.Sign(apiKey, ts, request.Method, request.URL.Path,
			crypto.SHA256Hex([]byte(body)), nonce, client.PublicKeyBase64()))
		response := httptest.NewRecorder()
		secured.ServeHTTP(response, request)
		ciphertext := response.Body.Bytes()
		if !crypto.VerifyResponseSignature(session, response.Code, ciphertext, response.Header().Get(crypto.ResponseSignatureHeader)) {
			t.Fatalf("unauthenticated %s response: status=%d", action, response.Code)
		}
		decoded, err := crypto.Decrypt(session, string(ciphertext))
		if err != nil {
			t.Fatal(err)
		}
		var result transferResult
		if err := json.Unmarshal(decoded, &result); err != nil || !result.OK {
			t.Fatalf("%s failed: %s (%v)", action, decoded, err)
		}
		// Reusing an accepted envelope never dispatches a second mutation.
		replay := httptest.NewRequest(http.MethodPost, request.URL.Path, strings.NewReader(body))
		replay.Header = request.Header.Clone()
		rejected := httptest.NewRecorder()
		secured.ServeHTTP(rejected, replay)
		if rejected.Code != http.StatusUnauthorized {
			t.Fatalf("%s replay status=%d", action, rejected.Code)
		}
		return result
	}
	if call("capabilities", transferRequest{}).ChunkBytes != TransferChunkBytes {
		t.Fatal("wrong chunk capability")
	}
	data := bytes.Repeat([]byte{255, 0, 128, 10, 13}, 60_001)
	target := filepath.Join(t.TempDir(), "destination")
	req := transferRequest{ID: strings.Repeat("d", 32), Path: target,
		Size: int64(len(data)), SHA256: crypto.SHA256Hex(data)}
	call("begin", req)
	for offset := 0; offset < len(data); offset += TransferChunkBytes {
		call("chunk", transferRequest{ID: req.ID, Offset: int64(offset),
			Data: base64.StdEncoding.EncodeToString(data[offset:min(len(data), offset+TransferChunkBytes)])})
	}
	if call("commit", req).State != "committed" || call("status", req).State != "committed" {
		t.Fatal("publication receipt missing")
	}
	info := call("info", transferRequest{Path: target})
	if info.SHA256 != req.SHA256 || info.Size != req.Size {
		t.Fatal("destination metadata changed")
	}
	var downloaded []byte
	for offset := int64(0); offset < req.Size; {
		result := call("read", transferRequest{Path: target, Offset: offset, Size: req.Size})
		chunk, err := base64.StdEncoding.DecodeString(result.Data)
		if err != nil {
			t.Fatal(err)
		}
		downloaded = append(downloaded, chunk...)
		offset += int64(len(chunk))
	}
	if !bytes.Equal(data, downloaded) {
		t.Fatal("encrypted binary round trip changed bytes")
	}
	call("abort", req)
	if got, err := os.ReadFile(target); err != nil || !bytes.Equal(got, data) {
		t.Fatal("abort removed committed file", err)
	}
}

func TestTransferBeginRejectsDestinationStagingCollision(t *testing.T) {
	id := strings.Repeat("a", 32)
	cases := []struct {
		name string
		base string
	}{
		{name: "exact", base: ".conch-transfer-" + id},
	}
	if runtime.GOOS == "windows" {
		cases = append(cases, struct {
			name string
			base string
		}{name: "windows-case-insensitive", base: strings.ToUpper(".conch-transfer-" + id)})
	}
	for _, test := range cases {
		for _, overwrite := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/overwrite=%t", test.name, overwrite), func(t *testing.T) {
				store := newTestTransfers(t)
				dir := t.TempDir()
				target := filepath.Join(dir, test.base)
				stage := filepath.Join(dir, ".conch-transfer-"+id)
				req := transferRequest{ID: id, Path: target, Size: 3,
					SHA256: crypto.SHA256Hex([]byte("abc")), Overwrite: overwrite}
				if _, err := store.apply(context.Background(), "begin", req); err == nil ||
					!strings.Contains(err.Error(), "destination conflicts with transfer staging path") {
					t.Fatalf("expected staging collision rejection, got %v", err)
				}
				for _, path := range []string{target, stage, filepath.Join(store.dir, id+".json")} {
					if _, err := os.Lstat(path); !os.IsNotExist(err) {
						t.Fatalf("rejected begin left an artifact at %s: %v", path, err)
					}
				}
				if len(store.entries) != 0 {
					t.Fatal("rejected begin retained a transfer entry")
				}
				for _, path := range []string{dir, store.dir} {
					entries, err := os.ReadDir(path)
					if err != nil || len(entries) != 0 {
						t.Fatalf("rejected begin changed directory %s: %v (%v)", path, entries, err)
					}
				}
			})
		}
	}
}

func TestTransferFailedBeginReleasesCapacityAndOwnedJournal(t *testing.T) {
	s := newTestTransfers(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "existing")
	if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	for index := range maxTransfers + 1 {
		_, err := s.begin(transferRequest{ID: fmt.Sprintf("%032x", index), Path: target, SHA256: crypto.SHA256Hex(nil)})
		if err == nil {
			t.Fatal("existing destination accepted")
		}
	}
	if len(s.entries) != 0 {
		t.Fatal("failed begins exhausted transfer capacity")
	}
	id := strings.Repeat("e", 32)
	stage := filepath.Join(dir, ".conch-transfer-"+id)
	if err := os.WriteFile(stage, []byte("unowned"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := s.begin(transferRequest{ID: id, Path: filepath.Join(dir, "new"), SHA256: crypto.SHA256Hex(nil)})
	if err == nil {
		t.Fatal("staging collision accepted")
	}
	if got, err := os.ReadFile(stage); err != nil || string(got) != "unowned" {
		t.Fatal("failed begin removed unowned staging", err)
	}
	if _, err := os.Stat(filepath.Join(s.dir, id+".json")); !os.IsNotExist(err) {
		t.Fatal("failed begin left cleanup journal")
	}
	if len(s.entries) != 0 {
		t.Fatal("failed staging retained entry")
	}
}

func TestTransferRejectsOversizedAndRepeatedChunks(t *testing.T) {
	s := newTestTransfers(t)
	req := transferRequest{ID: strings.Repeat("f", 32), Path: filepath.Join(t.TempDir(), "target"),
		Size: 2, SHA256: crypto.SHA256Hex([]byte("ab"))}
	transferApply(t, s, "begin", req)
	for _, chunk := range []transferRequest{
		{ID: req.ID, Data: strings.Repeat("A", base64.StdEncoding.EncodedLen(TransferChunkBytes)+1)},
		{ID: req.ID, Offset: 1, Data: base64.StdEncoding.EncodeToString([]byte("a"))},
		{ID: req.ID, Data: base64.StdEncoding.EncodeToString([]byte("abc"))},
	} {
		if _, err := s.apply(context.Background(), "chunk", chunk); err == nil {
			t.Fatal("invalid chunk accepted")
		}
	}
	chunk := transferRequest{ID: req.ID, Data: base64.StdEncoding.EncodeToString([]byte("a"))}
	transferApply(t, s, "chunk", chunk)
	if _, err := s.apply(context.Background(), "chunk", chunk); err == nil {
		t.Fatal("repeated chunk accepted")
	}
	if transferApply(t, s, "status", req).Offset != 1 {
		t.Fatal("invalid chunks changed accepted offset")
	}
	transferApply(t, s, "abort", req)
}

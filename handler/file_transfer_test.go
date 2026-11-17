package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/newo-ether/conch/crypto"
)

func newTestTransfers(t *testing.T) *FileTransfers {
	t.Helper()
	s, err := NewFileTransfers(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func transferApply(t *testing.T, s *FileTransfers, action string, req transferRequest) transferResult {
	t.Helper()
	result, err := s.apply(context.Background(), action, req)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func stageTransfer(t *testing.T, s *FileTransfers, path, id string, data []byte, overwrite bool) transferRequest {
	t.Helper()
	req := transferRequest{ID: id, Path: path, Size: int64(len(data)), SHA256: crypto.SHA256Hex(data), Overwrite: overwrite}
	transferApply(t, s, "begin", req)
	for offset := 0; offset < len(data); offset += TransferChunkBytes {
		chunk := data[offset:min(offset+TransferChunkBytes, len(data))]
		transferApply(t, s, "chunk", transferRequest{ID: id, Offset: int64(offset), Data: base64.StdEncoding.EncodeToString(chunk)})
	}
	return req
}

func TestTransferBinaryRoundTripAndEmpty(t *testing.T) {
	for _, data := range [][]byte{nil, bytes.Repeat([]byte{0, 255, 128, 13, 10, 1}, 100000)} {
		s := newTestTransfers(t)
		dir := t.TempDir()
		source := filepath.Join(dir, "source")
		if err := os.WriteFile(source, data, 0600); err != nil {
			t.Fatal(err)
		}
		info, err := transferInfo(context.Background(), source)
		if err != nil {
			t.Fatal(err)
		}
		var downloaded []byte
		for offset := int64(0); offset < info.Size; {
			result, err := transferRead(context.Background(), transferRequest{Path: info.Path, Size: info.Size, Offset: offset})
			if err != nil {
				t.Fatal(err)
			}
			chunk, err := base64.StdEncoding.DecodeString(result.Data)
			if err != nil {
				t.Fatal(err)
			}
			downloaded = append(downloaded, chunk...)
			offset += int64(len(chunk))
		}
		if !bytes.Equal(data, downloaded) {
			t.Fatal("source bytes changed")
		}
		target := filepath.Join(dir, "target")
		req := stageTransfer(t, s, target, strings.Repeat("a", 32), downloaded, false)
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Fatal("partial target published")
		}
		result := transferApply(t, s, "commit", req)
		if result.State != "committed" || result.SHA256 != info.SHA256 {
			t.Fatalf("bad receipt: %+v", result)
		}
		// Lost commit responses can be reconciled without publishing again.
		if transferApply(t, s, "status", req).State != "committed" {
			t.Fatal("missing receipt")
		}
		if transferApply(t, s, "commit", req).State != "committed" {
			t.Fatal("commit not idempotent")
		}
		got, err := os.ReadFile(target)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatal("destination bytes changed", err)
		}
	}
}

func TestTransferNoOverwriteRaceAndReplacement(t *testing.T) {
	s := newTestTransfers(t)
	target := filepath.Join(t.TempDir(), "target")
	a := stageTransfer(t, s, target, strings.Repeat("a", 32), []byte("a"), false)
	b := stageTransfer(t, s, target, strings.Repeat("b", 32), []byte("b"), false)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, req := range []transferRequest{a, b} {
		wg.Add(1)
		go func(req transferRequest) {
			defer wg.Done()
			_, err := s.apply(context.Background(), "commit", req)
			results <- err
		}(req)
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("successful publications = %d", success)
	}
	replacement := stageTransfer(t, s, target, strings.Repeat("c", 32), []byte("replacement"), true)
	transferApply(t, s, "commit", replacement)
	got, _ := os.ReadFile(target)
	if string(got) != "replacement" {
		t.Fatal("overwrite failed")
	}
}

func TestTransferMismatchCancellationAbortAndExpiry(t *testing.T) {
	s := newTestTransfers(t)
	target := filepath.Join(t.TempDir(), "target")
	req := stageTransfer(t, s, target, strings.Repeat("a", 32), []byte("abc"), false)
	s.entries[req.ID].sha256 = strings.Repeat("0", 64)
	if _, err := s.apply(context.Background(), "commit", req); err == nil {
		t.Fatal("hash mismatch accepted")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("bad target published")
	}
	if _, err := os.Stat(s.entries[req.ID].stage); !os.IsNotExist(err) {
		t.Fatal("bad staging retained")
	}
	req = stageTransfer(t, s, target, strings.Repeat("b", 32), []byte("ok"), false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.apply(ctx, "commit", req); err == nil {
		t.Fatal("cancelled commit accepted")
	}
	transferApply(t, s, "abort", req)
	if _, err := os.Stat(s.entries[req.ID].stage); !os.IsNotExist(err) {
		t.Fatal("aborted staging retained")
	}
	req = stageTransfer(t, s, target, strings.Repeat("c", 32), []byte("ok"), false)
	stage := s.entries[req.ID].stage
	s.expire(time.Now().Add(2 * transferTTL))
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Fatal("expired staging retained")
	}
}

func TestTransferRestartRemovesAbandonedStaging(t *testing.T) {
	dir := t.TempDir()
	id := strings.Repeat("a", 32)
	stage := filepath.Join(t.TempDir(), ".conch-transfer-"+id)
	if err := os.WriteFile(stage, []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	journal, _ := json.Marshal(stage)
	if err := os.WriteFile(filepath.Join(dir, id+".json"), journal, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := NewFileTransfers(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Fatal("orphan staging retained")
	}
}

func TestTransferReadBoundsAndSourceDrift(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(path, []byte("abc"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, req := range []transferRequest{
		{Path: path, Offset: -1, Size: 3}, {Path: path, Offset: 4, Size: 3},
		{Path: path, Size: 4}, {Path: path, Size: TransferMaxBytes + 1},
	} {
		if _, err := transferRead(context.Background(), req); err == nil {
			t.Fatalf("accepted bad range: %+v", req)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := transferInfo(ctx, path); err == nil {
		t.Fatal("cancelled source hash accepted")
	}
}

func TestTransferHandlerRejectsUnauthenticatedAndOversized(t *testing.T) {
	key := []byte("transfer-test-key")
	pair, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	h := &FileTransferHandler{APIKey: key, KeyPair: pair, Transfers: newTestTransfers(t)}
	secured := AuthMiddleware(key, crypto.NewNonceTracker())(h)
	request := httptest.NewRequest(http.MethodPost, "/file/transfer/begin", strings.NewReader(`{}`))
	response := httptest.NewRecorder()
	secured.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", response.Code)
	}
	request = httptest.NewRequest(http.MethodPost, "/file/transfer/chunk", strings.NewReader(strings.Repeat("x", 1<<20+1)))
	response = httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status=%d", response.Code)
	}
}

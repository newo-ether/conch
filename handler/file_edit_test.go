package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileEditHandlesLargeFileWithoutTruncatingTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.txt")
	prefix := strings.Repeat("a", maxFileSize+128)
	original := prefix + "UNIQUE_MARKER" + strings.Repeat("z", 4096)
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeMode := beforeInfo.Mode().Perm()

	response := callPlainFileHandler(t, &FileEditHandler{}, "/file/edit", FileEditRequest{
		Path:      path,
		OldString: "UNIQUE_MARKER",
		NewString: "replacement",
	})
	var result FileEditResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v: %s", err, response.Body.String())
	}
	if !result.OK || result.Replacements != 1 || result.SHA256 == "" {
		t.Fatalf("unexpected response: %#v", result)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := prefix + "replacement" + strings.Repeat("z", 4096)
	if string(got) != want {
		t.Fatalf("large edit corrupted content: got %d bytes, want %d", len(got), len(want))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != beforeMode {
		t.Fatalf("permissions = %o, want preserved %o", info.Mode().Perm(), beforeMode)
	}
}

func TestFileEditCASConflictDoesNotMutateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cas.txt")
	original := []byte("before")
	if err := os.WriteFile(path, original, 0644); err != nil {
		t.Fatal(err)
	}

	response := callPlainFileHandler(t, &FileEditHandler{}, "/file/edit", FileEditRequest{
		Path:           path,
		OldString:      "before",
		NewString:      "after",
		ExpectedSHA256: strings.Repeat("0", 64),
	})
	var result map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !strings.Contains(result["error"].(string), "file changed concurrently") {
		t.Fatalf("unexpected response: %#v", result)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("CAS conflict mutated file: %q", got)
	}
}

func TestFileReadReportsTruncation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "read.txt")
	if err := os.WriteFile(path, []byte("0123456789"), 0644); err != nil {
		t.Fatal(err)
	}
	response := callPlainFileHandler(t, &FileReadHandler{}, "/file/read", FileReadRequest{
		Path:  path,
		Limit: 4,
	})
	var result FileReadResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Content != "0123" || !result.Truncated || result.Size != 10 {
		t.Fatalf("unexpected read result: %#v", result)
	}
}

func callPlainFileHandler(t *testing.T, handler http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(data))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

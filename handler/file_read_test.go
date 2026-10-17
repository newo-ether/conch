package handler

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/newo-ether/conch/crypto"
)

func TestFileReadTrimsPartialUTF8AtLimitBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "utf8.txt")
	if err := os.WriteFile(path, []byte("中文测试内容"), 0644); err != nil {
		t.Fatal(err)
	}
	// limit=4 lands inside the second multi-byte rune; the returned content must
	// be valid UTF-8 with the partial rune dropped.
	response := callPlainFileHandler(t, &FileReadHandler{}, "/file/read", FileReadRequest{
		Path:  path,
		Limit: 4,
	})
	var result FileReadResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(result.Content) {
		t.Fatalf("content is not valid UTF-8: %q", result.Content)
	}
	if strings.Contains(result.Content, "�") {
		t.Fatalf("content contains a replacement char: %q", result.Content)
	}
	if result.Content != "中" {
		t.Fatalf("content = %q, want %q", result.Content, "中")
	}
	if !result.Truncated {
		t.Fatalf("truncated = false, want true")
	}
}

func TestFileReadOffsetLandingMidRuneSkipsToRuneBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "utf8-offset.txt")
	if err := os.WriteFile(path, []byte("中文测试内容"), 0644); err != nil {
		t.Fatal(err)
	}
	// offset=1 lands inside the first rune; the read must start at 文.
	response := callPlainFileHandler(t, &FileReadHandler{}, "/file/read", FileReadRequest{
		Path:   path,
		Offset: 1,
	})
	var result FileReadResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(result.Content) {
		t.Fatalf("content is not valid UTF-8: %q", result.Content)
	}
	if !strings.HasPrefix(result.Content, "文") {
		t.Fatalf("content = %q, want prefix %q", result.Content, "文")
	}
}

func TestFileReadReportsTotalLinesForTruncatedReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lines.txt")
	if err := os.WriteFile(path, []byte("a\nb\nc\n"), 0644); err != nil {
		t.Fatal(err)
	}
	response := callPlainFileHandler(t, &FileReadHandler{}, "/file/read", FileReadRequest{
		Path:  path,
		Limit: 3,
	})
	var result FileReadResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Truncated {
		t.Fatalf("truncated = false, want true")
	}
	if result.TotalLines != 3 {
		t.Fatalf("totalLines = %d, want 3 (a trailing newline does not add a line)", result.TotalLines)
	}
	if result.Lines != 2 {
		t.Fatalf("lines = %d, want 2 for partial content %q", result.Lines, result.Content)
	}
}

func TestFileReadReportsWholeFileSHA256(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hash.txt")
	data := []byte("alpha\nbeta\ngamma\n")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	want := crypto.SHA256Hex(data)
	// Whole-file read, partial read from the start and a read from an offset all report the
	// hash of the complete file, which is what expected_sha256 compares against.
	for _, req := range []FileReadRequest{
		{Path: path},
		{Path: path, Limit: 3},
		{Path: path, Offset: 6, Limit: 4},
	} {
		response := callPlainFileHandler(t, &FileReadHandler{}, "/file/read", req)
		var result FileReadResponse
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.SHA256 != want {
			t.Fatalf("offset=%d limit=%d sha256 = %q, want %q", req.Offset, req.Limit, result.SHA256, want)
		}
	}
}

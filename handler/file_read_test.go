package handler

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
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
	if result.TotalLines != 4 {
		t.Fatalf("totalLines = %d, want 4 (whole-file convention: 1 + one per newline)", result.TotalLines)
	}
	if result.Lines != 2 {
		t.Fatalf("lines = %d, want 2 for partial content %q", result.Lines, result.Content)
	}
}

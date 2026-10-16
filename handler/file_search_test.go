package handler

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func decodeJSONMap(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode response: %v: %s", err, body)
	}
	return result
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestFileEditBatchAppliesInOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "batch.txt")
	writeTestFile(t, path, "alpha\r\nbeta\r\nbeta\r\ngamma\r\n")

	response := callPlainFileHandler(t, &FileEditHandler{}, "/file/edit", FileEditRequest{
		Path: path,
		Edits: []FileEditOperation{
			{OldString: "alpha", NewString: "ALPHA"},
			{OldString: "beta", NewString: "B", ReplaceAll: true},
			// Sees the output of the previous edit.
			{OldString: "B\nB", NewString: "joined"},
		},
	})
	var result FileEditResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v: %s", err, response.Body.String())
	}
	if !result.OK || result.Replacements != 4 || result.Edits != 3 {
		t.Fatalf("unexpected response: %s", response.Body.String())
	}
	got, _ := os.ReadFile(path)
	if want := "ALPHA\r\njoined\r\ngamma\r\n"; string(got) != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestFileEditBatchFailureWritesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "batch.txt")
	original := "one two two\n"
	writeTestFile(t, path, original)

	cases := []struct {
		name  string
		edits []FileEditOperation
		want  string
	}{
		{"missing", []FileEditOperation{{OldString: "one", NewString: "1"}, {OldString: "zzz", NewString: "x"}}, "edits[1]: old_string not found"},
		{"ambiguous", []FileEditOperation{{OldString: "one", NewString: "1"}, {OldString: "two", NewString: "2"}}, "edits[1]: found 2 matches"},
		{"consumed", []FileEditOperation{{OldString: "one", NewString: "1"}, {OldString: "one", NewString: "x"}}, "after applying earlier edits"},
		{"empty old", []FileEditOperation{{OldString: "", NewString: "x"}}, "edits[0]: old_string is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := callPlainFileHandler(t, &FileEditHandler{}, "/file/edit", FileEditRequest{Path: path, Edits: tc.edits})
			msg, _ := decodeJSONMap(t, response.Body.Bytes())["error"].(string)
			if !strings.Contains(msg, tc.want) {
				t.Fatalf("error = %q, want substring %q", msg, tc.want)
			}
			got, _ := os.ReadFile(path)
			if string(got) != original {
				t.Fatalf("failed batch mutated file: %q", got)
			}
		})
	}
}

func TestFileEditBatchRejectsMixedAndOversized(t *testing.T) {
	path := filepath.Join(t.TempDir(), "batch.txt")
	writeTestFile(t, path, "x\n")

	mixed := callPlainFileHandler(t, &FileEditHandler{}, "/file/edit", FileEditRequest{
		Path:      path,
		OldString: "x",
		Edits:     []FileEditOperation{{OldString: "x", NewString: "y"}},
	})
	if msg, _ := decodeJSONMap(t, mixed.Body.Bytes())["error"].(string); !strings.Contains(msg, "cannot be combined") {
		t.Fatalf("mixed error = %q", msg)
	}

	edits := make([]FileEditOperation, maxFileEditOperations+1)
	for i := range edits {
		edits[i] = FileEditOperation{OldString: "x", NewString: "x"}
	}
	tooMany := callPlainFileHandler(t, &FileEditHandler{}, "/file/edit", FileEditRequest{Path: path, Edits: edits})
	if msg, _ := decodeJSONMap(t, tooMany.Body.Bytes())["error"].(string); !strings.Contains(msg, "too many edits") {
		t.Fatalf("oversized error = %q", msg)
	}
}

func TestFileEditSingleModeErrorsUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "single.txt")
	writeTestFile(t, path, "a a\n")
	cases := map[string]FileEditRequest{
		"old_string is required":       {Path: path},
		"old_string not found in file": {Path: path, OldString: "z"},
		"found 2 matches of old_string; set replace_all=true or provide a unique match": {Path: path, OldString: "a"},
	}
	for want, req := range cases {
		response := callPlainFileHandler(t, &FileEditHandler{}, "/file/edit", req)
		if msg, _ := decodeJSONMap(t, response.Body.Bytes())["error"].(string); msg != want {
			t.Fatalf("error = %q, want %q", msg, want)
		}
	}
}

func TestFileReadReportsBytesRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "read.txt")
	// "中" is 3 bytes; offset 1 lands inside it and limit cuts "文".
	writeTestFile(t, path, "中文ab")
	response := callPlainFileHandler(t, &FileReadHandler{}, "/file/read", FileReadRequest{Path: path, Offset: 1, Limit: 4})
	var result FileReadResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	// Two continuation bytes dropped, then "文" does not fit in the remaining 2 bytes.
	if result.Content != "" || result.BytesRead != 2 {
		t.Fatalf("unexpected result: %#v", result)
	}

	response = callPlainFileHandler(t, &FileReadHandler{}, "/file/read", FileReadRequest{Path: path, Offset: 3, Limit: 4})
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Content != "文a" || result.BytesRead != 4 || !result.Truncated {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestFileReadPagingNeverStallsOnInvalidBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.bin")
	data := []byte{'a', 0xFF, 0xE4, 'b', 0x80, 0x80, 0x80, 0x80, 0xE4, 0xB8}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	var offset int64
	for steps := 0; offset < int64(len(data)); steps++ {
		if steps > len(data) {
			t.Fatalf("paging stalled at offset %d", offset)
		}
		response := callPlainFileHandler(t, &FileReadHandler{}, "/file/read", FileReadRequest{Path: path, Offset: offset, Limit: 4})
		var result FileReadResponse
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.BytesRead <= 0 {
			t.Fatalf("no progress at offset %d: %#v", offset, result)
		}
		offset += result.BytesRead
	}
	if offset != int64(len(data)) {
		t.Fatalf("paging ended at %d, want %d", offset, len(data))
	}
}

func grepRequest(t *testing.T, req FileGrepRequest) FileGrepResponse {
	t.Helper()
	response := callPlainFileHandler(t, &FileGrepHandler{}, "/file/grep", req)
	var result FileGrepResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v: %s", err, response.Body.String())
	}
	if !result.ContextSupported {
		t.Fatalf("contextSupported missing: %s", response.Body.String())
	}
	return result
}

func TestFileGrepSurvivesLongLineAndCountsSkippedFiles(t *testing.T) {
	root := t.TempDir()
	// A single line longer than the old 64 KB scanner default and close to the cap.
	writeTestFile(t, filepath.Join(root, "a_long.txt"), strings.Repeat("x", grepMaxFileSize-100)+"\nneedle\n")
	writeTestFile(t, filepath.Join(root, "b.txt"), "needle\n")
	writeTestFile(t, filepath.Join(root, "c_huge.txt"), strings.Repeat("needle\n", grepMaxFileSize/7+10))

	result := grepRequest(t, FileGrepRequest{Pattern: "needle", Path: root})
	if len(result.Matches) != 2 || result.SkippedLargeFiles != 1 || result.Truncated {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.Matches[0].Line != 2 {
		t.Fatalf("line = %d, want 2", result.Matches[0].Line)
	}
}

func TestFileGrepGlobCaseAndVCS(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "src", "deep", "a.kt"), "Hello\n")
	writeTestFile(t, filepath.Join(root, "src", "a.go"), "hello\n")
	writeTestFile(t, filepath.Join(root, ".git", "config.kt"), "hello\n")

	if got := grepRequest(t, FileGrepRequest{Pattern: "hello", Path: root}); len(got.Matches) != 1 {
		t.Fatalf("case-sensitive matches = %+v", got.Matches)
	}
	got := grepRequest(t, FileGrepRequest{Pattern: "hello", Path: root, CaseInsensitive: true})
	if len(got.Matches) != 2 {
		t.Fatalf("case-insensitive matches (VCS must be skipped) = %+v", got.Matches)
	}
	got = grepRequest(t, FileGrepRequest{Pattern: "hello", Path: root, Glob: "src/**/*.kt", CaseInsensitive: true})
	if len(got.Matches) != 1 || !strings.HasSuffix(got.Matches[0].Path, "a.kt") {
		t.Fatalf("path glob matches = %+v", got.Matches)
	}
	got = grepRequest(t, FileGrepRequest{Pattern: "hello", Path: root, Glob: "*.go"})
	if len(got.Matches) != 1 || !strings.HasSuffix(got.Matches[0].Path, "a.go") {
		t.Fatalf("basename glob matches = %+v", got.Matches)
	}
	// A file root still honours a basename glob.
	file := filepath.Join(root, "src", "a.go")
	if got := grepRequest(t, FileGrepRequest{Pattern: "hello", Path: file, Glob: "*.go"}); len(got.Matches) != 1 {
		t.Fatalf("file-root matches = %+v", got.Matches)
	}
}

func TestFileGrepContextAndUTF8Truncation(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "f.txt"), "l1\r\nl2\r\nhit\r\nl4\r\n")
	writeTestFile(t, filepath.Join(root, "g.txt"), strings.Repeat("a", grepMaxContentLen-1)+"中hit\n")

	got := grepRequest(t, FileGrepRequest{Pattern: "hit", Path: filepath.Join(root, "f.txt"), Context: 50})
	m := got.Matches[0]
	if m.Line != 3 || strings.Join(m.Before, "|") != "l1|l2" || strings.Join(m.After, "|") != "l4" {
		t.Fatalf("context match = %+v", m)
	}

	got = grepRequest(t, FileGrepRequest{Pattern: "hit", Path: filepath.Join(root, "g.txt")})
	if c := got.Matches[0].Content; len(c) != grepMaxContentLen-1 || strings.ContainsRune(c, '\uFFFD') {
		t.Fatalf("truncated content length %d", len(c))
	}
}

func TestFileGrepTruncationIsExact(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "exact.txt"), strings.Repeat("m\n", grepMaxMatches))
	if got := grepRequest(t, FileGrepRequest{Pattern: "m", Path: root}); len(got.Matches) != grepMaxMatches || got.Truncated {
		t.Fatalf("exact: %d matches, truncated=%v", len(got.Matches), got.Truncated)
	}
	writeTestFile(t, filepath.Join(root, "more.txt"), "m\n")
	if got := grepRequest(t, FileGrepRequest{Pattern: "m", Path: root}); len(got.Matches) != grepMaxMatches || !got.Truncated {
		t.Fatalf("over: %d matches, truncated=%v", len(got.Matches), got.Truncated)
	}
}

func TestFileGlobTruncationIsExactAndSkipsVCS(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < fileSearchMaxGlobMatches; i++ {
		writeTestFile(t, filepath.Join(root, fmt.Sprintf("f%04d.txt", i)), "")
	}
	writeTestFile(t, filepath.Join(root, ".git", "x.txt"), "")

	glob := func() FileGlobResponse {
		response := callPlainFileHandler(t, &FileGlobHandler{}, "/file/glob", FileGlobRequest{Pattern: "*.txt", Path: root})
		var result FileGlobResponse
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	if got := glob(); len(got.Files) != fileSearchMaxGlobMatches || got.Truncated {
		t.Fatalf("exact: %d files, truncated=%v", len(got.Files), got.Truncated)
	}
	writeTestFile(t, filepath.Join(root, "extra.txt"), "")
	if got := glob(); len(got.Files) != fileSearchMaxGlobMatches || !got.Truncated {
		t.Fatalf("over: %d files, truncated=%v", len(got.Files), got.Truncated)
	}
}

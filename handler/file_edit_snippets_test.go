package handler

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func numberedLines(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "l%d\n", i)
	}
	return b.String()
}

func snippetsFor(t *testing.T, content string, ops []FileEditOperation) ([]FileEditSnippet, bool, string) {
	t.Helper()
	replaced, _, tracker, errText := applyFileEditOperationsTracked(content, ops, len(ops) > 1)
	if errText != "" {
		t.Fatalf("apply: %s", errText)
	}
	snippets, truncated := tracker.snippets(replaced)
	return snippets, truncated, replaced
}

func TestFileEditSnippetsShowContextAroundSingleEdit(t *testing.T) {
	snippets, truncated, _ := snippetsFor(t, numberedLines(20), []FileEditOperation{{OldString: "l10\n", NewString: "X\nY\n"}})
	want := []FileEditSnippet{{StartLine: 7, Lines: []string{"l7", "l8", "l9", "X", "Y", "l11", "l12", "l13"}}}
	if truncated || !reflect.DeepEqual(snippets, want) {
		t.Fatalf("snippets = %#v truncated=%v, want %#v", snippets, truncated, want)
	}
}

func TestFileEditSnippetsTrackEarlierEditsAcrossLaterShifts(t *testing.T) {
	// The second edit inserts lines before the first edit's region, shifting it down.
	snippets, _, _ := snippetsFor(t, numberedLines(40), []FileEditOperation{
		{OldString: "l30\n", NewString: "THIRTY\n"},
		{OldString: "l2\n", NewString: "a\nb\nc\n"},
	})
	if len(snippets) != 2 {
		t.Fatalf("want 2 snippets, got %#v", snippets)
	}
	if snippets[0].StartLine != 1 || snippets[0].Lines[1] != "a" {
		t.Fatalf("first snippet = %#v", snippets[0])
	}
	// l30 was line 30; two extra lines above it put THIRTY on line 32.
	second := snippets[1]
	if got := second.Lines[32-second.StartLine]; got != "THIRTY" {
		t.Fatalf("THIRTY not at line 32: %#v", second)
	}
}

func TestFileEditSnippetsMergeNearbyAndCoverOverwrittenRegions(t *testing.T) {
	snippets, _, _ := snippetsFor(t, numberedLines(20), []FileEditOperation{
		{OldString: "l5\n", NewString: "five\n"},
		{OldString: "l7\n", NewString: "seven\n"},
		// Overwrites the first edit's text; the tracked region must follow it.
		{OldString: "five\n", NewString: "FIVE\n"},
	})
	want := []FileEditSnippet{{StartLine: 2, Lines: []string{"l2", "l3", "l4", "FIVE", "l6", "seven", "l8", "l9", "l10"}}}
	if !reflect.DeepEqual(snippets, want) {
		t.Fatalf("snippets = %#v, want %#v", snippets, want)
	}
}

func TestFileEditSnippetsHandleDeletionCRLFAndFileEdges(t *testing.T) {
	snippets, _, _ := snippetsFor(t, "a\r\nb\r\nc\r\n", []FileEditOperation{{OldString: "c\r\n", NewString: ""}})
	want := []FileEditSnippet{{StartLine: 1, Lines: []string{"a", "b"}}}
	if !reflect.DeepEqual(snippets, want) {
		t.Fatalf("deletion at end = %#v", snippets)
	}
	snippets, _, _ = snippetsFor(t, "only", []FileEditOperation{{OldString: "only", NewString: ""}})
	if len(snippets) != 0 {
		t.Fatalf("empty result should have no snippets: %#v", snippets)
	}
}

func TestFileEditSnippetsAreBounded(t *testing.T) {
	content := strings.Repeat("x\n\n\n\n\n\n\n\n\n\n", 50)
	snippets, truncated, _ := snippetsFor(t, content, []FileEditOperation{{OldString: "x", NewString: "y", ReplaceAll: true}})
	if !truncated || len(snippets) != maxFileEditSnippets {
		t.Fatalf("want %d snippets truncated, got %d truncated=%v", maxFileEditSnippets, len(snippets), truncated)
	}
	long := strings.Repeat("n\n", 200)
	snippets, truncated, _ = snippetsFor(t, "start\n", []FileEditOperation{{OldString: "start\n", NewString: long}})
	if !truncated || len(snippets) != 1 || len(snippets[0].Lines) != maxFileEditSnippetLines {
		t.Fatalf("long snippet not capped: %d lines truncated=%v", len(snippets[0].Lines), truncated)
	}
}

func TestFileEditResponseIncludesSnippets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.txt")
	if err := os.WriteFile(path, []byte(numberedLines(5)), 0600); err != nil {
		t.Fatal(err)
	}
	response := callPlainFileHandler(t, &FileEditHandler{}, "/file/edit", FileEditRequest{
		Path: path, OldString: "l3", NewString: "three",
	})
	var result FileEditResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v: %s", err, response.Body.String())
	}
	want := []FileEditSnippet{{StartLine: 1, Lines: []string{"l1", "l2", "three", "l4", "l5"}}}
	if !result.OK || !reflect.DeepEqual(result.Snippets, want) || result.SnippetsTruncated {
		t.Fatalf("unexpected response: %#v", result)
	}
}

package mcp

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func findTool(t *testing.T, name string) Tool {
	t.Helper()
	for _, tool := range allTools {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("tool %q not registered", name)
	return Tool{}
}

func TestFileEditSchemasAreSplit(t *testing.T) {
	single := findTool(t, "file_edit")
	if _, ok := single.InputSchema.Properties["edits"]; ok {
		t.Fatalf("file_edit must not advertise edits")
	}
	for _, field := range []string{"old_string", "new_string"} {
		if !slices.Contains(single.InputSchema.Required, field) {
			t.Fatalf("file_edit must require %s", field)
		}
	}

	multi := findTool(t, "file_multi_edit")
	for _, field := range []string{"old_string", "new_string", "replace_all"} {
		if _, ok := multi.InputSchema.Properties[field]; ok {
			t.Fatalf("file_multi_edit must not advertise %s", field)
		}
	}
	if !slices.Contains(multi.InputSchema.Required, "edits") {
		t.Fatalf("file_multi_edit must require edits")
	}
}

func TestFileMultiEditAppliesBatchAndRequiresEdits(t *testing.T) {
	transport := newFileToolsTestTransport(t)
	server := NewServer(map[string]*Transport{"test": transport}, nil)
	path := filepath.Join(t.TempDir(), "multi.txt")
	if err := os.WriteFile(path, []byte("a b c\n"), 0644); err != nil {
		t.Fatal(err)
	}

	missing, _ := server.doFileMultiEdit(context.Background(), transport, map[string]interface{}{"path": path})
	if !missing.IsError || !strings.Contains(missing.Content[0].Text, "edits is required") {
		t.Fatalf("missing edits result = %+v", missing)
	}

	// Empty single-edit placeholders next to edits must not conflict.
	result, _ := server.doFileMultiEdit(context.Background(), transport, map[string]interface{}{
		"path":        path,
		"old_string":  "",
		"new_string":  "",
		"replace_all": false,
		"edits": []interface{}{
			map[string]interface{}{"old_string": "a", "new_string": "1"},
			map[string]interface{}{"old_string": "1 b", "new_string": "x"},
		},
	})
	if result.IsError {
		t.Fatalf("multi edit failed: %+v", result)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "x c\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestFileEditStillAcceptsLegacyEdits(t *testing.T) {
	transport := newFileToolsTestTransport(t)
	server := NewServer(map[string]*Transport{"test": transport}, nil)
	path := filepath.Join(t.TempDir(), "legacy.txt")
	if err := os.WriteFile(path, []byte("a b\n"), 0644); err != nil {
		t.Fatal(err)
	}
	result, _ := server.doFileEdit(context.Background(), transport, map[string]interface{}{
		"path":  path,
		"edits": []interface{}{map[string]interface{}{"old_string": "b", "new_string": "2"}},
	})
	if result.IsError {
		t.Fatalf("legacy batch failed: %+v", result)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "a 2\n" {
		t.Fatalf("content = %q", got)
	}
}

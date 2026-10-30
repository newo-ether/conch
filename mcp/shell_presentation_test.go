package mcp

import (
	"strings"
	"testing"
)

func TestShellSchemasRequireDescription(t *testing.T) {
	for _, tool := range allTools {
		if !strings.HasPrefix(tool.Name, "shell_") {
			continue
		}
		if tool.InputSchema.Properties["description"].Type != "string" {
			t.Fatalf("%s description schema missing", tool.Name)
		}
		required := false
		for _, field := range tool.InputSchema.Required {
			required = required || field == "description"
		}
		if !required {
			t.Fatalf("%s description is not required", tool.Name)
		}
	}
}

func TestShellOutputSelection(t *testing.T) {
	const output = "one\ntwo\nthree\nfour\nfive\n"
	for _, test := range []struct {
		name       string
		head, tail float64
		want       string
	}{
		{"head and tail", 1, 2, "one\n... (2 lines omitted) ...\nfour\nfive"},
		{"head only", 2, 0, "one\ntwo\n... (3 lines omitted) ..."},
		{"tail only", 0, 2, "... (3 lines omitted) ...\nfour\nfive"},
		{"overlap", 3, 3, output},
		{"exact coverage", 2, 3, output},
		{"no selection", 0, 0, output},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := map[string]interface{}{"head_lines": test.head, "tail_lines": test.tail}
			if got := selectShellOutput(output, args); got != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
}

func TestShellSelectionPreservesTerminalEvidence(t *testing.T) {
	code := 7
	events := []LineEvent{
		{JobID: "retained-job", Stream: "stdout", Line: "first"},
		{Stream: "stderr", Line: "middle"},
		{Stream: "stdout", Line: "last"},
		{Warning: "truncated"},
		{JobID: "retained-job", ExitCode: &code},
	}
	result := formatShellOutput(selectShellEvents(events, map[string]interface{}{
		"head_lines": float64(1), "tail_lines": float64(1),
	}))
	text := result.Content[0].Text
	for _, want := range []string{"job_id: retained-job", "first", "1 lines omitted", "last", "[warning] truncated", "Exit code: 7"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %q", want, text)
		}
	}
	if events[1].Line != "middle" || result.IsError {
		t.Fatal("selection mutated original events or treated exit code as tool failure")
	}
}

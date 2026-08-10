package mcp

import (
	"strings"
	"testing"
)

func TestParseSSERequiresTerminalEvent(t *testing.T) {
	_, err := parseSSE(strings.NewReader(`event: line
data: {"line":"partial","stream":"stdout"}

`), nil)
	if err == nil || !strings.Contains(err.Error(), "without a terminal") {
		t.Fatalf("error = %v, want missing terminal", err)
	}
}

func TestParseSSEFailsOnMalformedJSON(t *testing.T) {
	_, err := parseSSE(strings.NewReader(`event: result
data: not-json

`), nil)
	if err == nil || !strings.Contains(err.Error(), "decode SSE result") {
		t.Fatalf("error = %v, want result decode failure", err)
	}
}

func TestParseSSEPreservesWarningAndMachineReadableTimeout(t *testing.T) {
	events, err := parseSSE(strings.NewReader(`event: warning
data: {"message":"line truncated"}

event: error
data: {"message":"command timed out","timed_out":true}

`), nil)
	if err != nil {
		t.Fatalf("parseSSE: %v", err)
	}
	if len(events) != 2 || events[0].Warning != "line truncated" {
		t.Fatalf("events = %#v", events)
	}
	if events[1].Error != "command timed out" || !events[1].TimedOut {
		t.Fatalf("timeout event = %#v", events[1])
	}
}

func TestParseSSEFailsClosedOnDecryptionError(t *testing.T) {
	_, err := parseSSE(strings.NewReader(`event: result
data: definitely-not-ciphertext

`), []byte("invalid-key"))
	if err == nil || !strings.Contains(err.Error(), "decrypt SSE") {
		t.Fatalf("error = %v, want decryption failure", err)
	}
}

func TestParseSSEBoundsRetainedOutput(t *testing.T) {
	line := strings.Repeat("x", 64*1024)
	var stream strings.Builder
	for i := 0; i < 20; i++ {
		stream.WriteString("event: line\ndata: {\"line\":\"")
		stream.WriteString(line)
		stream.WriteString("\",\"stream\":\"stdout\"}\n\n")
	}
	stream.WriteString("event: result\ndata: {\"exit_code\":0}\n\n")

	events, err := parseSSE(strings.NewReader(stream.String()), nil)
	if err != nil {
		t.Fatalf("parseSSE: %v", err)
	}
	retained := 0
	foundWarning := false
	for _, event := range events {
		retained += len(event.Line)
		if strings.Contains(event.Warning, "truncated at 1 MiB") {
			foundWarning = true
		}
	}
	if retained > maxRetainedShellBytes {
		t.Fatalf("retained %d bytes, limit %d", retained, maxRetainedShellBytes)
	}
	if !foundWarning {
		t.Fatal("bounded output did not surface a truncation warning")
	}
}

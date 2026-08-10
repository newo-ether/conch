package mcp

import (
	"strings"
	"testing"
)

func TestReadBoundedBodyRejectsOversizedResponse(t *testing.T) {
	_, err := readBoundedBody(strings.NewReader("12345"), 4)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("readBoundedBody error = %v, want explicit size failure", err)
	}
}

func TestUninitializedTransportIsUnavailable(t *testing.T) {
	transport := NewTransport("http://127.0.0.1", "key")
	available, reason := transport.Status()
	if available || !strings.Contains(reason, "not initialized") {
		t.Fatalf("Status = (%v, %q), want unavailable initialization state", available, reason)
	}
}

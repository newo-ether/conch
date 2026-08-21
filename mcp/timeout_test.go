package mcp

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestShellExecuteToolRequiresExplicitBoundedTimeout(t *testing.T) {
	var shellExecute *Tool
	for i := range allTools {
		if allTools[i].Name == "shell_execute" {
			shellExecute = &allTools[i]
			break
		}
	}
	if shellExecute == nil {
		t.Fatal("shell_execute tool not found")
	}

	timeoutProp, ok := shellExecute.InputSchema.Properties["timeout_ms"]
	if !ok {
		t.Fatal("shell_execute timeout_ms property missing")
	}
	if timeoutProp.Default != nil {
		t.Fatalf("timeout_ms default = %#v, want no implicit default", timeoutProp.Default)
	}
	if timeoutProp.Minimum != minShellExecuteTimeoutMs {
		t.Fatalf("timeout_ms minimum = %d, want %d", timeoutProp.Minimum, minShellExecuteTimeoutMs)
	}
	if timeoutProp.Maximum != maxShellExecuteTimeoutMs {
		t.Fatalf("timeout_ms maximum = %d, want %d", timeoutProp.Maximum, maxShellExecuteTimeoutMs)
	}

	required := false
	for _, name := range shellExecute.InputSchema.Required {
		if name == "timeout_ms" {
			required = true
			break
		}
	}
	if !required {
		t.Fatalf("shell_execute required fields = %v, want timeout_ms", shellExecute.InputSchema.Required)
	}
}

func TestShellExecuteRejectsInvalidTimeoutBeforeTransport(t *testing.T) {
	tests := []struct {
		name string
		args map[string]interface{}
		want string
	}{
		{
			name: "missing",
			args: map[string]interface{}{"command": "true"},
			want: "timeout_ms is required",
		},
		{
			name: "not number",
			args: map[string]interface{}{"command": "true", "timeout_ms": "30000"},
			want: "timeout_ms must be an integer",
		},
		{
			name: "fractional",
			args: map[string]interface{}{"command": "true", "timeout_ms": 1.5},
			want: "timeout_ms must be an integer",
		},
		{
			name: "zero",
			args: map[string]interface{}{"command": "true", "timeout_ms": float64(0)},
			want: "timeout_ms must be between 1 and 1800000",
		},
		{
			name: "above maximum",
			args: map[string]interface{}{"command": "true", "timeout_ms": float64(maxShellExecuteTimeoutMs + 1)},
			want: "timeout_ms must be between 1 and 1800000",
		},
	}

	server := &Server{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, rpcErr := server.doShellExecute(context.Background(), nil, tt.args)
			if rpcErr != nil {
				t.Fatalf("doShellExecute RPC error: %v", rpcErr)
			}
			if !result.IsError || len(result.Content) != 1 ||
				!strings.Contains(result.Content[0].Text, tt.want) {
				t.Fatalf("result = %#v, want error containing %q", result, tt.want)
			}
		})
	}
}

func TestShellExecuteRequestTimeoutUsesCallValuePlusSettlementMargin(t *testing.T) {
	tests := []struct {
		timeoutMs int
		want      time.Duration
	}{
		{timeoutMs: 30000, want: 30*time.Second + shellExecuteSettlementMargin},
		{timeoutMs: maxShellExecuteTimeoutMs, want: 30*time.Minute + shellExecuteSettlementMargin},
	}
	for _, tt := range tests {
		got, err := shellExecuteRequestTimeout(tt.timeoutMs)
		if err != nil {
			t.Fatalf("shellExecuteRequestTimeout(%d): %v", tt.timeoutMs, err)
		}
		if got != tt.want {
			t.Fatalf("shellExecuteRequestTimeout(%d) = %s, want %s", tt.timeoutMs, got, tt.want)
		}
	}
}

func TestTransportRejectsOutOfRangeExecuteTimeoutBeforeNetwork(t *testing.T) {
	transport := NewTransport("http://127.0.0.1:1", "key")
	for _, timeoutMs := range []int{0, maxShellExecuteTimeoutMs + 1} {
		_, err := transport.Execute(context.Background(), "true", timeoutMs, "")
		if err == nil || !strings.Contains(err.Error(), "timeout_ms must be between") {
			t.Fatalf("Execute timeout %d error = %v, want range error", timeoutMs, err)
		}
	}
}

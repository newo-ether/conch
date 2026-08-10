//go:build windows

package shell

import (
	"context"
	"testing"
	"time"
)

func TestDecodeWindowsCodePageSupportsGBK(t *testing.T) {
	got, ok := decodeWindowsCodePage(
		[]byte{0xD6, 0xD0, 0xCE, 0xC4},
		simplifiedChineseCodePage,
	)
	if !ok || got != "中文" {
		t.Fatalf("decoded output = (%q, %v), want (%q, true)", got, ok, "中文")
	}
}

func TestExecutorUsesUtf8ForPowerShellInputAndOutput(t *testing.T) {
	executor := NewExecutor(10*time.Second, 10*time.Second)
	var line string
	for event := range executor.Execute(
		context.Background(),
		Request{Command: "Write-Output '中文测试'", TimeoutMs: 10_000},
	) {
		if event.Error != "" {
			t.Fatalf("execution error: %s", event.Error)
		}
		if event.Line != "" {
			line = event.Line
		}
	}
	if line != "中文测试" {
		t.Fatalf("line = %q, want %q", line, "中文测试")
	}
}

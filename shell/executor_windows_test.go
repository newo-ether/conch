//go:build windows

package shell

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	result := executeWindowsTestCommand(t, "Write-Output '中文测试 日本語 한국어 café 😀 🚀'")
	if result.exitCode != 0 || result.output != "中文测试 日本語 한국어 café 😀 🚀" {
		t.Fatalf("result = %#v", result)
	}
}

func TestExecutorRunsSingleAndDoubleQuotedHereStringsAndFollowingStatements(t *testing.T) {
	target := filepath.Join(t.TempDir(), "here-string.txt")
	quotedTarget := strings.ReplaceAll(target, "'", "''")
	command := "$single = @'\nalpha @ literal\n中文 UTF-8 😀\n'@\n" +
		"$name = '世界'\n$double = @\"\nhello $name café 🚀\n\"@\n" +
		"[IO.File]::WriteAllText('" + quotedTarget + "', $single + \"`n\" + $double, [Text.UTF8Encoding]::new($false))\n" +
		"Write-Output 'AFTER_HERE_STRING'"
	result := executeWindowsTestCommand(t, command)
	if result.exitCode != 0 || result.output != "AFTER_HERE_STRING" {
		t.Fatalf("result = %#v", result)
	}
	written, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	text := string(written)
	if !strings.Contains(text, "alpha @ literal") || !strings.Contains(text, "中文 UTF-8 😀") ||
		!strings.Contains(text, "hello 世界 café 🚀") {
		t.Fatalf("written content = %q", text)
	}
}

func TestExecutorAcceptsMaximumBoundedCommandWithoutCommandLineOverflow(t *testing.T) {
	padding := strings.Repeat("#", MaxCommandBytes-len("Write-Output 'MAX_BOUND_OK'\n"))
	result := executeWindowsTestCommand(t, "Write-Output 'MAX_BOUND_OK'\n"+padding)
	if result.exitCode != 0 || result.output != "MAX_BOUND_OK" {
		t.Fatalf("result = %#v", result)
	}
}

func TestExecutorReportsPowerShellParserErrorsAsNonzero(t *testing.T) {
	result := executeWindowsTestCommand(t, "Write-Output ('unterminated'")
	if result.exitCode == 0 {
		t.Fatalf("parser failure reported success: %#v", result)
	}
	if strings.TrimSpace(result.output) == "" {
		t.Fatalf("parser diagnostic missing: %#v", result)
	}
}

func TestExecutorPreservesTailOutputBeforeRootExit(t *testing.T) {
	const lineCount = 4000
	result := executeWindowsTestCommand(
		t,
		"1.."+strconv.Itoa(lineCount)+" | ForEach-Object { Write-Output ('tail-' + $_) }",
	)
	if result.exitCode != 0 {
		t.Fatalf("exit code = %d, output tail = %q", result.exitCode, takeOutputTail(result.output, 200))
	}
	lines := strings.Split(result.output, "\n")
	if len(lines) != lineCount || lines[0] != "tail-1" || lines[lineCount-1] != "tail-4000" {
		t.Fatalf("captured %d lines, first = %q, last = %q", len(lines), lines[0], lines[len(lines)-1])
	}
}

func TestExecutorFinishesWhenDetachedDescendantRetainsOutputHandles(t *testing.T) {
	executor := NewExecutor(10*time.Second, 10*time.Second)
	events := executor.Execute(
		context.Background(),
		Request{
			Command: "$child = [Diagnostics.ProcessStartInfo]::new('powershell', " +
				"'-NoProfile -Command Start-Sleep -Seconds 5'); " +
				"$child.UseShellExecute = $false; [Diagnostics.Process]::Start($child) | Out-Null; " +
				"Write-Output 'PARENT_DONE'",
			TimeoutMs: 10_000,
		},
	)
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	var lines []string
	var warnings []string
	var exitCode *int
	for {
		select {
		case event, ok := <-events:
			if !ok {
				if !strings.Contains(strings.Join(lines, "\n"), "PARENT_DONE") {
					t.Fatalf("parent output missing: %q", strings.Join(lines, "\n"))
				}
				if exitCode == nil || *exitCode != 0 {
					t.Fatalf("exit code = %v", exitCode)
				}
				if len(warnings) != 0 {
					t.Fatalf("expected local bounded drain closure to be silent, warnings = %q", warnings)
				}
				return
			}
			if event.Line != "" {
				lines = append(lines, event.Line)
			}
			if event.Warning != "" {
				warnings = append(warnings, event.Warning)
			}
			if event.ExitCode != nil {
				code := *event.ExitCode
				exitCode = &code
			}
		case <-deadline.C:
			t.Fatal("executor waited for detached descendant instead of the root command")
		}
	}
}

func takeOutputTail(output string, limit int) string {
	if len(output) <= limit {
		return output
	}
	return output[len(output)-limit:]
}

type windowsTestResult struct {
	output   string
	exitCode int
}

func executeWindowsTestCommand(t *testing.T, command string) windowsTestResult {
	t.Helper()
	executor := NewExecutor(10*time.Second, 10*time.Second)
	var lines []string
	result := windowsTestResult{exitCode: -999}
	for event := range executor.Execute(
		context.Background(),
		Request{Command: command, TimeoutMs: 10_000},
	) {
		if event.Error != "" {
			lines = append(lines, event.Error)
		}
		if event.Line != "" {
			lines = append(lines, event.Line)
		}
		if event.ExitCode != nil {
			result.exitCode = *event.ExitCode
		}
	}
	result.output = strings.Join(lines, "\n")
	return result
}

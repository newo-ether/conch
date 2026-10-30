package shell

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestExecutorReportsInvalidWorkdirWithoutRunningCommand(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "regular-file")
	if err := os.WriteFile(file, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		workdir string
		reason  string
	}{
		{"missing", filepath.Join(dir, "missing folder"), "workdir does not exist"},
		{"file", file, "workdir is not a directory"},
	} {
		t.Run(test.name, func(t *testing.T) {
			executor := NewExecutor(time.Second, time.Second)
			var gotError string
			for event := range executor.Execute(context.Background(), Request{
				Command: printCommand("MUST_NOT_RUN"), Workdir: test.workdir,
			}) {
				if event.ExitCode != nil || event.Stream != "" {
					t.Fatalf("invalid workdir executed command: %#v", event)
				}
				gotError = event.Error
			}
			want := fmt.Sprintf("%s: %q", test.reason, test.workdir)
			if gotError != want {
				t.Fatalf("error = %q, want %q", gotError, want)
			}
		})
	}
}

func TestExecutorReturnsNonZeroExitCodeWithoutTransportError(t *testing.T) {
	executor := NewExecutor(5*time.Second, 5*time.Second)
	var exitCode *int
	for event := range executor.Execute(context.Background(), Request{Command: failingCommand()}) {
		if event.Error != "" {
			t.Fatalf("unexpected transport error: %s", event.Error)
		}
		if event.ExitCode != nil {
			code := *event.ExitCode
			exitCode = &code
		}
	}
	if exitCode == nil || *exitCode != 7 {
		t.Fatalf("exit code = %v, want 7", exitCode)
	}
}

func TestExecutorRejectsOversizedCommand(t *testing.T) {
	executor := NewExecutor(time.Second, time.Second)
	var gotError string
	for event := range executor.Execute(context.Background(), Request{
		Command: strings.Repeat("x", MaxCommandBytes+1),
	}) {
		if event.Error != "" {
			gotError = event.Error
		}
	}
	if !strings.Contains(gotError, "64KB") {
		t.Fatalf("error = %q, want command size failure", gotError)
	}
}

func TestExecutorMarksDeadlineTimeoutMachineReadably(t *testing.T) {
	executor := NewExecutor(100*time.Millisecond, time.Second)
	var timedOut bool
	var exitCode *int
	for event := range executor.Execute(context.Background(), Request{
		Command:   sleepCommand(),
		TimeoutMs: 100,
	}) {
		if event.TimedOut {
			timedOut = true
		}
		if event.ExitCode != nil {
			code := *event.ExitCode
			exitCode = &code
		}
	}
	if !timedOut {
		t.Fatal("deadline did not produce timed_out=true")
	}
	if exitCode != nil {
		t.Fatalf("timed-out command also produced an exit code: %d", *exitCode)
	}
}

func TestScanStreamDrainsOversizedLineAndContinues(t *testing.T) {
	input := strings.Repeat("x", (1<<20)+1) + "\nnext\n\n"
	events := make(chan LineEvent, 4)
	var wg sync.WaitGroup
	wg.Add(1)
	scanStream(&wg, events, strings.NewReader(input), "stdout")
	wg.Wait()
	close(events)

	var warning bool
	lines := make([]string, 0, 2)
	for event := range events {
		if event.Warning != "" {
			warning = true
		}
		if event.Line != "" || (event.Warning == "" && event.Stream != "") {
			lines = append(lines, event.Line)
		}
	}
	if !warning {
		t.Fatal("oversized line did not produce a warning")
	}
	if len(lines) != 2 || lines[0] != "next" || lines[1] != "" {
		t.Fatalf("subsequent lines = %#v, want [next empty]", lines)
	}
}

func failingCommand() string {
	if runtime.GOOS == "windows" {
		return "exit 7"
	}
	return "exit 7"
}

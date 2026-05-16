package shell

import (
	"bufio"
	"context"
	"encoding/json"
	"os/exec"
	"runtime"
	"time"
)

type LineEvent struct {
	Line     string `json:"line,omitempty"`
	Stream   string `json:"stream,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Error    string `json:"error,omitempty"`
}

type Request struct {
	Command   string `json:"command"`
	TimeoutMs int    `json:"timeout_ms"`
	Workdir   string `json:"workdir"`
}

type Executor struct {
	DefaultTimeout time.Duration
	MaxTimeout     time.Duration
}

func NewExecutor(defaultTimeout, maxTimeout time.Duration) *Executor {
	return &Executor{
		DefaultTimeout: defaultTimeout,
		MaxTimeout:     maxTimeout,
	}
}

func (e *Executor) Execute(ctx context.Context, req Request) <-chan LineEvent {
	ch := make(chan LineEvent, 10)

	go func() {
		defer close(ch)

		if req.Command == "" {
			ch <- LineEvent{Error: "empty command"}
			return
		}

		timeout := e.DefaultTimeout
		if req.TimeoutMs > 0 {
			timeout = time.Duration(req.TimeoutMs) * time.Millisecond
		}
		if timeout > e.MaxTimeout {
			timeout = e.MaxTimeout
		}

		execCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.CommandContext(execCtx, "cmd", "/C", req.Command)
		} else {
			cmd = exec.CommandContext(execCtx, "/bin/sh", "-c", req.Command)
		}

		if req.Workdir != "" {
			cmd.Dir = req.Workdir
		}

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			ch <- LineEvent{Error: "failed to create stdout pipe: " + err.Error()}
			return
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			ch <- LineEvent{Error: "failed to create stderr pipe: " + err.Error()}
			return
		}

		if err := cmd.Start(); err != nil {
			ch <- LineEvent{Error: "failed to start command: " + err.Error()}
			return
		}

		done := make(chan struct{})
		go func() {
			defer close(done)
			scanner := bufio.NewScanner(stdout)
			for scanner.Scan() {
				ch <- LineEvent{Line: scanner.Text(), Stream: "stdout"}
			}
		}()

		go func() {
			scanner := bufio.NewScanner(stderr)
			for scanner.Scan() {
				ch <- LineEvent{Line: scanner.Text(), Stream: "stderr"}
			}
		}()

		<-done
		cmd.Wait() // stderr goroutine may still be running, but we drain what we got

		exitCode := 0
		if cmd.ProcessState != nil {
			exitCode = cmd.ProcessState.ExitCode()
		}

		if execCtx.Err() == context.DeadlineExceeded {
			ch <- LineEvent{Error: "command timed out"}
		} else {
			ch <- LineEvent{ExitCode: &exitCode}
		}
	}()

	return ch
}

func (e *Executor) HealthCheck(ctx context.Context) ([]byte, error) {
	return json.Marshal(map[string]string{"status": "ok"})
}

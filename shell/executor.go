package shell

import (
	"bufio"
	"context"
	"encoding/json"
	"log"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// MaxConcurrentCommands caps the number of simultaneously executing commands.
const MaxConcurrentCommands = 10

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
	sem            chan struct{}
}

func NewExecutor(defaultTimeout, maxTimeout time.Duration) *Executor {
	return &Executor{
		DefaultTimeout: defaultTimeout,
		MaxTimeout:     maxTimeout,
		sem:            make(chan struct{}, MaxConcurrentCommands),
	}
}

func (e *Executor) Execute(ctx context.Context, req Request) <-chan LineEvent {
	return e.ExecuteWithMaxTimeout(ctx, req, e.MaxTimeout)
}

// ExecuteWithMaxTimeout shares the executor's global concurrency semaphore while allowing
// the background-job manager to use its separately configured (and still bounded) deadline.
func (e *Executor) ExecuteWithMaxTimeout(
	ctx context.Context,
	req Request,
	maxTimeout time.Duration,
) <-chan LineEvent {
	ch := make(chan LineEvent, 10)

	go func() {
		defer close(ch)

		if req.Command == "" {
			ch <- LineEvent{Error: "empty command"}
			return
		}

		// Acquire concurrency slot
		select {
		case e.sem <- struct{}{}:
			defer func() { <-e.sem }()
		case <-ctx.Done():
			ch <- LineEvent{Error: "request cancelled"}
			return
		}

		timeout := e.DefaultTimeout
		if req.TimeoutMs > 0 {
			timeout = time.Duration(req.TimeoutMs) * time.Millisecond
		}
		if maxTimeout <= 0 {
			maxTimeout = e.MaxTimeout
		}
		if timeout > maxTimeout {
			timeout = maxTimeout
		}

		execCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.CommandContext(execCtx, "powershell", "-NoProfile", "-")
			cmd.Stdin = strings.NewReader(req.Command)
		} else {
			cmd = exec.CommandContext(execCtx, "/bin/sh", "-c", req.Command)
			setSysProcAttr(cmd)
		}

		if req.Workdir != "" {
			cmd.Dir = req.Workdir
		}

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			log.Printf("ERROR: failed to create stdout pipe: %v", err)
			ch <- LineEvent{Error: "internal error"}
			return
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			log.Printf("ERROR: failed to create stderr pipe: %v", err)
			ch <- LineEvent{Error: "internal error"}
			return
		}

		if err := cmd.Start(); err != nil {
			log.Printf("ERROR: failed to start command: %v", err)
			ch <- LineEvent{Error: "internal error"}
			return
		}

		// Kill the entire process tree on timeout so background jobs (& / Start-Job)
		// won't hold stdout/stderr pipes open and deadlock the scanner goroutines.
		go func() {
			<-execCtx.Done()
			killProcessTree(cmd)
		}()

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			scanner := bufio.NewScanner(stdout)
			scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
			for scanner.Scan() {
				ch <- LineEvent{Line: scanner.Text(), Stream: "stdout"}
			}
		}()

		go func() {
			defer wg.Done()
			scanner := bufio.NewScanner(stderr)
			scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
			for scanner.Scan() {
				ch <- LineEvent{Line: scanner.Text(), Stream: "stderr"}
			}
		}()

		wg.Wait()
		cmd.Wait()

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

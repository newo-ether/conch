package shell

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"os/exec"
	"sync"
	"time"
)

const (
	// MaxConcurrentCommands caps the number of simultaneously executing commands.
	MaxConcurrentCommands = 10
	MaxCommandBytes       = 64 << 10
	MaxWorkdirBytes       = 32 << 10
)

type LineEvent struct {
	Line     string `json:"line,omitempty"`
	Stream   string `json:"stream,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Error    string `json:"error,omitempty"`
	// Warning reports a degraded but non-fatal condition, currently only output truncation.
	// It is a separate field from Error precisely because the command still runs to completion
	// and still has a meaningful exit code: folding truncation into Error would relabel a
	// successful command as failed and discard that exit code.
	Warning string `json:"warning,omitempty"`
	// TimedOut marks the ONE error that means "the deadline killed the process", as opposed to
	// an error produced by the command itself (curl's "Operation timed out", a Go "i/o timeout")
	// or by output handling. Clients must branch on this flag, never on the message text:
	// mistaking a command's own timeout for a deadline kill causes a non-idempotent command to be
	// silently re-run.
	TimedOut bool `json:"timed_out,omitempty"`
}

type Request struct {
	Command   string `json:"command"`
	TimeoutMs int    `json:"timeout_ms"`
	Workdir   string `json:"workdir"`
}

type processTreeController interface {
	Kill()
	Close()
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
		if len(req.Command) > MaxCommandBytes {
			ch <- LineEvent{Error: "command exceeds 64KB limit"}
			return
		}
		if len(req.Workdir) > MaxWorkdirBytes {
			ch <- LineEvent{Error: "workdir exceeds 32KB limit"}
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

		cmd := newShellCommand(execCtx, req.Command)
		setSysProcAttr(cmd)

		if req.Workdir != "" {
			cmd.Dir = req.Workdir
		}

		stdoutReader, stdoutWriter, err := os.Pipe()
		if err != nil {
			log.Printf("ERROR: failed to create stdout pipe: %v", err)
			ch <- LineEvent{Error: "internal error"}
			return
		}
		stderrReader, stderrWriter, err := os.Pipe()
		if err != nil {
			_ = stdoutReader.Close()
			_ = stdoutWriter.Close()
			log.Printf("ERROR: failed to create stderr pipe: %v", err)
			ch <- LineEvent{Error: "internal error"}
			return
		}

		cmd.Stdout = stdoutWriter
		cmd.Stderr = stderrWriter

		if err := cmd.Start(); err != nil {
			_ = stdoutReader.Close()
			_ = stdoutWriter.Close()
			_ = stderrReader.Close()
			_ = stderrWriter.Close()
			log.Printf("ERROR: failed to start command: %v", err)
			ch <- LineEvent{Error: "internal error"}
			return
		}

		// Start duplicates the write handles into the child. Drop the parent's copies so ordinary
		// completion produces EOF while Conch retains control of the local read handles.
		_ = stdoutWriter.Close()
		_ = stderrWriter.Close()

		// Bind descendants to an operating-system process-tree controller. Windows uses a Job
		// Object with KILL_ON_JOB_CLOSE; Unix uses a dedicated process group. If Windows cannot
		// attach the process, the controller retains the taskkill /T fallback.
		processTree, treeErr := newProcessTreeController(cmd)
		if treeErr != nil {
			log.Printf("WARNING: process tree isolation degraded for pid %d: %v", cmd.Process.Pid, treeErr)
		}
		defer processTree.Close()

		// Do not wait only on execCtx.Done(): the deferred cancel after an ordinary completion used
		// to wake this goroutine and issue a late tree kill. By that point the PID could already
		// have been recycled. processDone closes before the deferred cancel and fences that window.
		processDone := make(chan struct{})
		go func() {
			select {
			case <-execCtx.Done():
				processTree.Kill()
			case <-processDone:
			}
		}()

		var wg sync.WaitGroup
		wg.Add(2)
		scanStream(&wg, ch, stdoutReader, "stdout")
		scanStream(&wg, ch, stderrReader, "stderr")

		waitErr := cmd.Wait()
		close(processDone)
		// Preserve a bounded tail-drain opportunity after the root exits. A detached descendant may
		// keep inherited writers open indefinitely, so close only Conch's readers after the bound.
		drained := make(chan struct{})
		go func() {
			wg.Wait()
			close(drained)
		}()
		select {
		case <-drained:
		case <-time.After(500 * time.Millisecond):
			_ = stdoutReader.Close()
			_ = stderrReader.Close()
			<-drained
		}

		if execCtx.Err() == context.DeadlineExceeded {
			ch <- LineEvent{Error: "command timed out", TimedOut: true}
			return
		}
		if cmd.ProcessState == nil {
			log.Printf("ERROR: command wait produced no process state: %v", waitErr)
			ch <- LineEvent{Error: "internal error"}
			return
		}
		var exitErr *exec.ExitError
		if waitErr != nil && !errors.As(waitErr, &exitErr) {
			log.Printf("ERROR: failed waiting for command: %v", waitErr)
			ch <- LineEvent{Error: "internal error"}
			return
		}
		exitCode := cmd.ProcessState.ExitCode()
		ch <- LineEvent{ExitCode: &exitCode}
	}()

	return ch
}

// scanStream forwards bounded lines while continuing to drain oversized lines. Continuing the
// drain is essential: if a scanner stops at its token limit, the child can block forever on a full
// pipe and never reach its real exit status.
func scanStream(wg *sync.WaitGroup, ch chan<- LineEvent, r io.Reader, stream string) {
	go func() {
		defer wg.Done()
		const maxLineBytes = 1 << 20
		reader := bufio.NewReaderSize(r, 64*1024)
		line := make([]byte, 0, 64*1024)
		discarding := false

		for {
			fragment, continued, err := reader.ReadLine()
			if !discarding {
				if len(line)+len(fragment) <= maxLineBytes {
					line = append(line, fragment...)
				} else {
					discarding = true
				}
			}
			if !continued && (err == nil || len(fragment) > 0 || len(line) > 0 || discarding) {
				if discarding {
					ch <- LineEvent{
						Warning: "output truncated: a single " + stream +
							" line exceeded the 1MB limit (redirect verbose output to a file)",
					}
				} else {
					ch <- LineEvent{Line: decodeShellOutputLine(line), Stream: stream}
				}
				line = line[:0]
				discarding = false
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					ch <- LineEvent{Warning: stream + " read error: " + err.Error()}
				}
				return
			}
		}
	}()
}

func (e *Executor) HealthCheck(ctx context.Context) ([]byte, error) {
	return json.Marshal(map[string]string{"status": "ok"})
}

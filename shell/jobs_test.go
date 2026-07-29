package shell

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
	"unicode/utf8"
)

func TestJobManagerPersistsCompletedOutput(t *testing.T) {
	dir := t.TempDir()
	manager := newTestJobManager(t, dir)
	job, err := manager.Start(Request{Command: printCommand("hello")})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	finished := waitForTerminalJob(t, manager, job.JobID)
	if finished.State != JobStateSucceeded {
		t.Fatalf("state = %q, error = %q", finished.State, finished.Error)
	}
	if finished.Output == "" {
		t.Fatal("expected captured output")
	}

	reloaded := newTestJobManager(t, dir)
	persisted, ok := reloaded.Get(job.JobID)
	if !ok {
		t.Fatal("completed job was not loaded")
	}
	if persisted.State != JobStateSucceeded || persisted.Output == "" {
		t.Fatalf("persisted job = %#v", persisted)
	}
}

func TestJobManagerStopCancelsProcessTree(t *testing.T) {
	manager := newTestJobManager(t, t.TempDir())
	job, err := manager.Start(Request{Command: sleepCommand()})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, ok := manager.Stop(job.JobID); !ok {
		t.Fatal("Stop did not find running job")
	}
	finished := waitForTerminalJob(t, manager, job.JobID)
	if finished.State != JobStateStopped {
		t.Fatalf("state = %q, error = %q", finished.State, finished.Error)
	}
}

func TestJobManagerMarksOrphanedRunningJobInterrupted(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	job := Job{
		JobID:     "orphan",
		Command:   printCommand("never resumed"),
		State:     JobStateRunning,
		CreatedAt: now,
		StartedAt: now,
	}
	data, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "orphan.json"), data, 0600); err != nil {
		t.Fatal(err)
	}

	manager := newTestJobManager(t, dir)
	loaded, ok := manager.Get("orphan")
	if !ok {
		t.Fatal("orphaned job missing")
	}
	if loaded.State != JobStateInterrupted {
		t.Fatalf("state = %q", loaded.State)
	}
	if loaded.FinishedAt == nil {
		t.Fatal("interrupted job has no terminal timestamp")
	}
}

func TestJobOutputTruncationKeepsValidUTF8(t *testing.T) {
	manager := newTestJobManager(t, t.TempDir())
	manager.maxOutputBytes = 4
	job := &Job{}

	manager.appendOutputLocked(job, "你好")

	if !utf8.ValidString(job.Output) {
		t.Fatalf("truncated output is invalid UTF-8: %q", job.Output)
	}
	if job.Output != "好" {
		t.Fatalf("output = %q, want newest complete rune", job.Output)
	}
}

func TestJobManagerRecoversNewestCompleteTempSnapshot(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	oldJob := Job{
		JobID:     "recover",
		Command:   "old",
		State:     JobStateSucceeded,
		CreatedAt: now,
		StartedAt: now,
		Output:    "old",
	}
	newJob := oldJob
	newJob.Command = "new"
	newJob.Output = "new"
	writeJobSnapshot(t, filepath.Join(dir, "recover.json.bak"), oldJob)
	writeJobSnapshot(t, filepath.Join(dir, "recover.json.tmp"), newJob)

	manager := newTestJobManager(t, dir)
	recovered, ok := manager.Get("recover")
	if !ok {
		t.Fatal("recovered job missing")
	}
	if recovered.Output != "new" {
		t.Fatalf("output = %q, want newest temp snapshot", recovered.Output)
	}
}

func writeJobSnapshot(t *testing.T, path string, job Job) {
	t.Helper()
	data, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func newTestJobManager(t *testing.T, dir string) *JobManager {
	t.Helper()
	manager, err := NewJobManager(
		NewExecutor(5*time.Second, 5*time.Second),
		dir,
		time.Hour,
		5*time.Second,
		4096,
		20,
	)
	if err != nil {
		t.Fatalf("NewJobManager: %v", err)
	}
	return manager
}

func waitForTerminalJob(t *testing.T, manager *JobManager, id string) Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job, ok := manager.Get(id)
		if !ok {
			t.Fatalf("job %s disappeared", id)
		}
		if job.State != JobStateRunning && job.State != JobStateStopping {
			return job
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %s did not finish", id)
	return Job{}
}

func printCommand(value string) string {
	if runtime.GOOS == "windows" {
		return "Write-Output '" + value + "'"
	}
	return "printf '%s\\n' '" + value + "'"
}

func sleepCommand() string {
	if runtime.GOOS == "windows" {
		return "Start-Sleep -Seconds 30"
	}
	return "sleep 30"
}

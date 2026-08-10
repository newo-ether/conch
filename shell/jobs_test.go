package shell

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
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
	const jobID = "aaaaaaaaaaaaaaaaaaaaaaaa"
	dir := t.TempDir()
	now := time.Now().UTC()
	job := Job{
		JobID:     jobID,
		Command:   printCommand("never resumed"),
		State:     JobStateRunning,
		CreatedAt: now,
		StartedAt: now,
	}
	data, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, jobID+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}

	manager := newTestJobManager(t, dir)
	loaded, ok := manager.Get(jobID)
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
	const jobID = "bbbbbbbbbbbbbbbbbbbbbbbb"
	dir := t.TempDir()
	now := time.Now().UTC()
	oldJob := Job{
		JobID:     jobID,
		Command:   "old",
		State:     JobStateSucceeded,
		CreatedAt: now,
		StartedAt: now,
		Output:    "old",
	}
	newJob := oldJob
	newJob.Command = "new"
	newJob.Output = "new"
	writeJobSnapshot(t, filepath.Join(dir, jobID+".json.bak"), oldJob)
	writeJobSnapshot(t, filepath.Join(dir, jobID+".json.tmp"), newJob)

	manager := newTestJobManager(t, dir)
	recovered, ok := manager.Get(jobID)
	if !ok {
		t.Fatal("recovered job missing")
	}
	if recovered.Output != "new" {
		t.Fatalf("output = %q, want newest temp snapshot", recovered.Output)
	}
}

func TestJobRetentionStartsWhenJobFinishes(t *testing.T) {
	dir := t.TempDir()
	manager := newTestJobManager(t, dir)
	manager.retention = time.Hour
	now := time.Now().UTC()
	finished := now.Add(-30 * time.Minute)
	job := &Job{
		JobID:           "long-running",
		Command:         "long command",
		State:           JobStateSucceeded,
		CreatedAt:       now.Add(-48 * time.Hour),
		StartedAt:       now.Add(-48 * time.Hour),
		FinishedAt:      &finished,
		terminalSettled: true,
	}
	manager.jobs[job.JobID] = job
	writeJobSnapshot(t, filepath.Join(dir, job.JobID+".json"), *job)

	manager.cleanup()

	if _, ok := manager.Get(job.JobID); !ok {
		t.Fatal("recently finished long-running job was evicted using its creation time")
	}
}

func TestJobManagerAcknowledgesTerminalJobIdempotently(t *testing.T) {
	dir := t.TempDir()
	manager := newTestJobManager(t, dir)
	job, err := manager.Start(Request{Command: printCommand("acknowledge")})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForTerminalJob(t, manager, job.JobID)

	acknowledged, err := manager.Acknowledge(job.JobID)
	if err != nil || !acknowledged {
		t.Fatalf("Acknowledge = %v, %v", acknowledged, err)
	}
	if _, ok := manager.Get(job.JobID); ok {
		t.Fatal("acknowledged terminal job remains in memory")
	}
	if _, err := os.Stat(filepath.Join(dir, job.JobID+".json")); !os.IsNotExist(err) {
		t.Fatalf("acknowledged terminal snapshot still exists: %v", err)
	}
	acknowledged, err = manager.Acknowledge(job.JobID)
	if err != nil || !acknowledged {
		t.Fatalf("idempotent Acknowledge = %v, %v", acknowledged, err)
	}
}

func TestJobManagerRejectsAcknowledgingRunningJob(t *testing.T) {
	manager := newTestJobManager(t, t.TempDir())
	job, err := manager.Start(Request{Command: sleepCommand()})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if acknowledged, err := manager.Acknowledge(job.JobID); err == nil || acknowledged {
		t.Fatalf("running Acknowledge = %v, %v", acknowledged, err)
	}
	if _, ok := manager.Get(job.JobID); !ok {
		t.Fatal("rejected ACK removed running job")
	}
	manager.Stop(job.JobID)
	waitForTerminalJob(t, manager, job.JobID)
}

func TestJobManagerDoesNotExposeOrAcknowledgeTerminalBeforePersistenceRetry(t *testing.T) {
	manager := newTestJobManager(t, t.TempDir())
	terminalAttempt := make(chan struct{})
	releaseFailure := make(chan struct{})
	var once sync.Once
	var failures atomic.Int32
	manager.persistInterceptor = func(job Job) error {
		if job.State != JobStateSucceeded {
			return nil
		}
		once.Do(func() { close(terminalAttempt) })
		<-releaseFailure
		if failures.Add(1) == 1 {
			return errors.New("injected terminal persistence failure")
		}
		return nil
	}

	job, err := manager.Start(Request{Command: printCommand("settle")})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-terminalAttempt:
	case <-time.After(5 * time.Second):
		t.Fatal("terminal persistence was not attempted")
	}
	unsettled, ok := manager.Get(job.JobID)
	if !ok || unsettled.State != JobStateSettling {
		t.Fatalf("unsettled job = %#v, found %v", unsettled, ok)
	}
	if acknowledged, err := manager.Acknowledge(job.JobID); err == nil || acknowledged {
		t.Fatalf("unsettled Acknowledge = %v, %v", acknowledged, err)
	}

	close(releaseFailure)
	finished := waitForTerminalJob(t, manager, job.JobID)
	if finished.State != JobStateSucceeded {
		t.Fatalf("finished = %#v", finished)
	}
	if failures.Load() < 2 {
		t.Fatalf("terminal persistence attempts = %d, want retry", failures.Load())
	}
}

func TestReadableJobSnapshotExposesUnsettledTerminalAsSettling(t *testing.T) {
	exitCode := 0
	finished := time.Now().UTC()
	job := &Job{
		JobID:           "settling",
		State:           JobStateSucceeded,
		FinishedAt:      &finished,
		ExitCode:        &exitCode,
		SettlementError: "disk temporarily unavailable",
	}
	snapshot := readableJobSnapshot(job)
	if snapshot.State != JobStateSettling {
		t.Fatalf("state = %q, want %q", snapshot.State, JobStateSettling)
	}
	if snapshot.FinishedAt != nil || snapshot.ExitCode != nil {
		t.Fatalf("unsettled snapshot exposed terminal fields: %#v", snapshot)
	}
	if snapshot.SettlementError == "" {
		t.Fatal("settlement failure was not observable")
	}
}

func TestJobManagerCloseCancelsJobsAndRejectsNewStarts(t *testing.T) {
	manager := newTestJobManager(t, t.TempDir())
	job, err := manager.Start(Request{Command: sleepCommand()})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := manager.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	finished, ok := manager.Get(job.JobID)
	if !ok {
		t.Fatal("cancelled job disappeared")
	}
	if finished.State != JobStateStopped {
		t.Fatalf("state after Close = %q, error = %q", finished.State, finished.Error)
	}
	if _, err := manager.Start(Request{Command: printCommand("late")}); err == nil {
		t.Fatal("Start succeeded after Close")
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
		if job.State != JobStateRunning && job.State != JobStateStopping && job.State != JobStateSettling {
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

package shell

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	JobStateRunning     = "running"
	JobStateSucceeded   = "succeeded"
	JobStateFailed      = "failed"
	JobStateStopping    = "stopping"
	JobStateSettling    = "settling"
	JobStateStopped     = "stopped"
	JobStateInterrupted = "interrupted"
)

type Job struct {
	Description string     `json:"description,omitempty"`
	JobID       string     `json:"job_id"`
	Command     string     `json:"command"`
	Workdir     string     `json:"workdir,omitempty"`
	State       string     `json:"state"`
	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   time.Time  `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	ExitCode    *int       `json:"exit_code,omitempty"`
	Error       string     `json:"error,omitempty"`
	// Warning is a non-fatal degradation (currently output truncation). Unlike Error it never
	// changes State, so a job can legitimately be both succeeded and truncated.
	Warning         string `json:"warning,omitempty"`
	Output          string `json:"output"`
	OutputBytes     int64  `json:"output_bytes"`
	Truncated       bool   `json:"truncated"`
	SettlementError string `json:"settlement_error,omitempty"`
	// terminalSettled is process-local. Readers must not observe a terminal state until the
	// runner's final durable snapshot attempt has completed.
	terminalSettled bool
}

type JobManager struct {
	executor       *Executor
	dir            string
	retention      time.Duration
	maxRuntime     time.Duration
	maxOutputBytes int
	ctx            context.Context
	cancel         context.CancelFunc
	wg             sync.WaitGroup
	closeOnce      sync.Once
	lifecycleMu    sync.Mutex
	closing        bool

	mu          sync.RWMutex
	persistMu   sync.Mutex
	jobs        map[string]*Job
	cancels     map[string]context.CancelFunc
	lastPersist map[string]time.Time

	// persistInterceptor is test-only fault injection; production constructors leave it nil.
	persistInterceptor func(Job) error
}

func NewJobManager(
	executor *Executor,
	dir string,
	retention time.Duration,
	maxRuntime time.Duration,
	maxOutputBytes int,
	_ int, // Retained for source compatibility; count eviction is no longer applied.
) (*JobManager, error) {
	if executor == nil {
		return nil, errors.New("executor is required")
	}
	if dir == "" {
		return nil, errors.New("job directory is required")
	}
	if maxRuntime <= 0 || maxOutputBytes <= 0 {
		return nil, errors.New("job limits must be positive")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	manager := &JobManager{
		executor:       executor,
		dir:            dir,
		retention:      retention,
		maxRuntime:     maxRuntime,
		maxOutputBytes: maxOutputBytes,
		ctx:            ctx,
		cancel:         cancel,
		jobs:           make(map[string]*Job),
		cancels:        make(map[string]context.CancelFunc),
		lastPersist:    make(map[string]time.Time),
	}
	if err := manager.load(); err != nil {
		cancel()
		return nil, err
	}
	manager.cleanup()
	return manager, nil
}

// Close rejects new jobs, cancels every active process tree, and waits for runners to complete
// their final persistence attempt. The caller controls the maximum shutdown grace period.
func (m *JobManager) Close(ctx context.Context) error {
	m.closeOnce.Do(func() {
		m.lifecycleMu.Lock()
		m.closing = true
		m.cancel()
		m.lifecycleMu.Unlock()
	})

	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *JobManager) Start(req Request) (Job, error) {
	return m.start(req, m.ctx, m.maxRuntime, nil)
}

// Execute records foreground streams in the same store as background jobs. Disconnecting
// still cancels the process; the runner drains and persists its final state independently.
func (m *JobManager) Execute(ctx context.Context, req Request) <-chan LineEvent {
	events := make(chan LineEvent, 10)
	if req.TimeoutMs <= 0 {
		req.TimeoutMs = int(m.executor.DefaultTimeout / time.Millisecond)
	}
	if _, err := m.start(req, ctx, m.executor.MaxTimeout, events); err != nil {
		events <- LineEvent{Error: err.Error()}
		close(events)
	}
	return events
}

func (m *JobManager) start(req Request, parent context.Context, maxTimeout time.Duration, events chan LineEvent) (Job, error) {
	if strings.TrimSpace(req.Command) == "" {
		return Job{}, errors.New("command is required")
	}
	if len(req.Command) > MaxCommandBytes {
		return Job{}, errors.New("command exceeds 64KB limit")
	}
	if len(req.Workdir) > MaxWorkdirBytes {
		return Job{}, errors.New("workdir exceeds 32KB limit")
	}
	if len(req.Description) > MaxDescriptionBytes {
		return Job{}, errors.New("description exceeds 4KB limit")
	}
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	if m.closing {
		return Job{}, errors.New("job manager is shutting down")
	}
	m.cleanup()
	m.mu.Lock()
	if m.runningCountLocked() >= MaxConcurrentCommands {
		m.mu.Unlock()
		return Job{}, errors.New("too many running jobs")
	}
	id, err := newJobID()
	if err != nil {
		m.mu.Unlock()
		return Job{}, err
	}
	now := time.Now().UTC()
	job := &Job{
		Description: req.Description,
		JobID:       id,
		Command:     req.Command,
		Workdir:     req.Workdir,
		State:       JobStateRunning,
		CreatedAt:   now,
		StartedAt:   now,
		Output:      "",
	}
	ctx, cancel := context.WithCancel(m.ctx)
	stopParent := context.AfterFunc(parent, cancel)
	m.jobs[id] = job
	m.cancels[id] = cancel
	snapshot := *job
	m.mu.Unlock()
	if err := m.persist(snapshot); err != nil {
		m.mu.Lock()
		delete(m.jobs, id)
		delete(m.cancels, id)
		m.mu.Unlock()
		stopParent()
		cancel()
		return Job{}, err
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer stopParent()
		defer cancel()
		if events != nil {
			defer close(events)
		}
		m.run(ctx, req, id, maxTimeout, events)
	}()
	return snapshot, nil
}

func (m *JobManager) List() []Job {
	m.cleanup()
	m.mu.RLock()
	jobs := make([]Job, 0, len(m.jobs))
	for _, job := range m.jobs {
		jobs = append(jobs, readableJobSnapshot(job))
	}
	m.mu.RUnlock()
	sort.Slice(jobs, func(i, j int) bool {
		return jobs[i].CreatedAt.After(jobs[j].CreatedAt)
	})
	return jobs
}

func (m *JobManager) Get(id string) (Job, bool) {
	m.cleanup()
	m.mu.RLock()
	defer m.mu.RUnlock()
	job, ok := m.jobs[id]
	if !ok {
		return Job{}, false
	}
	return readableJobSnapshot(job), true
}

func (m *JobManager) Stop(id string) (Job, bool) {
	m.mu.Lock()
	job, ok := m.jobs[id]
	if !ok {
		m.mu.Unlock()
		return Job{}, false
	}
	cancel := m.cancels[id]
	if job.State == JobStateRunning {
		job.State = JobStateStopping
	}
	snapshot := readableJobSnapshot(job)
	m.mu.Unlock()
	if err := m.persist(snapshot); err != nil {
		log.Printf("WARNING: failed to persist stopping job %s: %v", id, err)
	}
	if cancel != nil {
		cancel()
	}
	return snapshot, true
}

// Acknowledge confirms that a terminal result is durably settled. Results remain queryable
// until time retention expires, including after acknowledgement and server restart.
func (m *JobManager) Acknowledge(id string) (bool, error) {
	if id == "" {
		return false, errors.New("job_id is required")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	job, ok := m.jobs[id]
	if !ok {
		return true, nil
	}
	if job.State == JobStateRunning || job.State == JobStateStopping || !job.terminalSettled {
		return false, errors.New("job result is not durably settled")
	}
	return true, nil
}

func (m *JobManager) run(ctx context.Context, req Request, id string, maxTimeout time.Duration, output chan LineEvent) {
	forward := func(event LineEvent) {
		if output != nil {
			event.JobID = id
			select {
			case output <- event:
			case <-ctx.Done():
			}
		}
	}
	var terminal *LineEvent
	if req.TimeoutMs <= 0 {
		req.TimeoutMs = int(m.maxRuntime / time.Millisecond)
	}
	events := m.executor.ExecuteWithMaxTimeout(ctx, req, maxTimeout)
	for event := range events {
		m.mu.Lock()
		job := m.jobs[id]
		if job == nil {
			m.mu.Unlock()
			return
		}
		if event.Line != "" {
			m.appendOutputLocked(job, event.Line+"\n")
		}
		if event.Warning != "" {
			// Recorded without touching State: the command keeps running and its exit code still
			// decides success. A truncated line must not turn a succeeding job into a failed one.
			if job.Warning == "" {
				job.Warning = event.Warning
			}
		}
		if event.Error != "" {
			job.Error = event.Error
			job.State = JobStateFailed
		}
		if event.ExitCode != nil {
			code := *event.ExitCode
			job.ExitCode = &code
			if ctx.Err() != nil || job.State == JobStateStopping {
				job.State = JobStateStopped
			} else if job.State == JobStateFailed {
				// Preserve an earlier executor/read failure even if the process itself exited zero.
			} else if code == 0 {
				job.State = JobStateSucceeded
			} else {
				job.State = JobStateFailed
			}
		}
		shouldPersist := time.Since(m.lastPersist[id]) >= time.Second
		snapshot := *job
		if shouldPersist {
			m.lastPersist[id] = time.Now()
		}
		m.mu.Unlock()
		if shouldPersist {
			if err := m.persist(snapshot); err != nil {
				log.Printf("WARNING: failed to persist running job %s: %v", id, err)
			}
		}
		if event.Error != "" || event.ExitCode != nil {
			copy := event
			terminal = &copy
		} else {
			forward(event)
		}
	}

	m.mu.Lock()
	job := m.jobs[id]
	if job == nil {
		m.mu.Unlock()
		return
	}
	if ctx.Err() != nil || job.State == JobStateStopping {
		job.State = JobStateStopped
	} else if job.State == JobStateRunning {
		job.State = JobStateFailed
		job.Error = "executor ended without a result"
	}
	finished := time.Now().UTC()
	job.FinishedAt = &finished
	delete(m.cancels, id)
	delete(m.lastPersist, id)
	snapshot := *job
	m.mu.Unlock()

	// A terminal result is not externally settled until its snapshot is durable. Retry transient
	// storage failures while the manager is running; during a shutdown, one final attempt is made
	// and recovery will honestly mark the last durable running snapshot as interrupted if it fails.
	retryDelay := 50 * time.Millisecond
	for {
		err := m.persist(snapshot)
		if err == nil {
			m.mu.Lock()
			if current := m.jobs[id]; current != nil {
				current.SettlementError = ""
				current.terminalSettled = true
			}
			m.mu.Unlock()
			m.cleanup()
			if terminal != nil {
				forward(*terminal)
			}
			return
		}

		log.Printf("ERROR: failed to persist terminal job %s; retrying: %v", id, err)
		m.mu.Lock()
		if current := m.jobs[id]; current != nil {
			current.SettlementError = err.Error()
		}
		m.mu.Unlock()

		timer := time.NewTimer(retryDelay)
		select {
		case <-timer.C:
			if retryDelay < 5*time.Second {
				retryDelay *= 2
				if retryDelay > 5*time.Second {
					retryDelay = 5 * time.Second
				}
			}
		case <-m.ctx.Done():
			timer.Stop()
			return
		}
	}
}

func (m *JobManager) appendOutputLocked(job *Job, delta string) {
	job.OutputBytes += int64(len(delta))
	combined := job.Output + delta
	if len(combined) > m.maxOutputBytes {
		start := len(combined) - m.maxOutputBytes
		for start < len(combined) && !utf8.RuneStart(combined[start]) {
			start++
		}
		combined = strings.ToValidUTF8(combined[start:], "\uFFFD")
		job.Truncated = true
	}
	job.Output = combined
}

func (m *JobManager) runningCountLocked() int {
	count := 0
	for _, job := range m.jobs {
		if job.State == JobStateRunning || job.State == JobStateStopping {
			count++
		}
	}
	return count
}

func (m *JobManager) load() error {
	if err := m.recoverSnapshots(); err != nil {
		return err
	}
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(m.dir, entry.Name()))
		if err != nil {
			continue
		}
		var job Job
		expectedID := strings.TrimSuffix(entry.Name(), ".json")
		if json.Unmarshal(data, &job) != nil || job.JobID != expectedID || !validJobID(job.JobID) {
			log.Printf("WARNING: ignoring invalid job snapshot %s", entry.Name())
			continue
		}
		if job.State == JobStateRunning || job.State == JobStateStopping {
			job.State = JobStateInterrupted
			job.Error = "conch restarted while the job was running"
			job.FinishedAt = &now
			if err := m.persist(job); err != nil {
				return fmt.Errorf("persist interrupted job %s: %w", job.JobID, err)
			}
		}
		job.terminalSettled = true
		m.jobs[job.JobID] = &job
	}
	return nil
}

func readableJobSnapshot(job *Job) Job {
	snapshot := *job
	if snapshot.terminalSettled ||
		snapshot.State == JobStateRunning || snapshot.State == JobStateStopping {
		return snapshot
	}
	// The process has ended, but exposing its terminal result before the final snapshot succeeds
	// lets a client commit/ACK a state that process recovery cannot yet reproduce.
	snapshot.State = JobStateSettling
	snapshot.FinishedAt = nil
	snapshot.ExitCode = nil
	return snapshot
}

func (m *JobManager) cleanup() {
	// Retention runs only after terminal persistence has completed.
	m.persistMu.Lock()
	defer m.persistMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().UTC()
	toRemove := make(map[string]struct{})
	for id, job := range m.jobs {
		if job.State == JobStateRunning || job.State == JobStateStopping || !job.terminalSettled {
			continue
		}
		terminalAt := job.CreatedAt
		if job.FinishedAt != nil {
			terminalAt = *job.FinishedAt
		}
		if m.retention > 0 && now.Sub(terminalAt) > m.retention {
			toRemove[id] = struct{}{}
		}
	}

	removed := false
	for id := range toRemove {
		if err := os.Remove(m.path(id)); err != nil && !os.IsNotExist(err) {
			log.Printf("WARNING: failed to remove expired job %s: %v", id, err)
			continue
		}
		delete(m.jobs, id)
		delete(m.cancels, id)
		delete(m.lastPersist, id)
		removed = true
	}
	if removed {
		if err := syncDirectory(m.dir); err != nil {
			log.Printf("WARNING: failed to flush job retention changes: %v", err)
		}
	}
}

func (m *JobManager) persist(job Job) error {
	if m.persistInterceptor != nil {
		if err := m.persistInterceptor(job); err != nil {
			return err
		}
	}
	m.persistMu.Lock()
	defer m.persistMu.Unlock()

	data, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return err
	}
	target := m.path(job.JobID)
	temp := target + ".tmp"
	backup := target + ".bak"
	file, err := os.OpenFile(temp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(temp)
		return err
	}

	_ = os.Remove(backup)
	if _, statErr := os.Stat(target); statErr == nil {
		if err := os.Rename(target, backup); err != nil {
			_ = os.Remove(temp)
			return err
		}
	}
	if err := os.Rename(temp, target); err != nil {
		_ = os.Rename(backup, target)
		_ = os.Remove(temp)
		return err
	}
	if err := syncDirectory(m.dir); err != nil {
		return err
	}
	_ = os.Remove(backup)
	return syncDirectory(m.dir)
}

func (m *JobManager) recoverSnapshots() error {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return err
	}
	changed := false
	// A complete .tmp contains the newest durable snapshot. Recover those before
	// considering .bak files so directory iteration order cannot resurrect stale data.
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".json.tmp") {
			continue
		}
		temp := filepath.Join(m.dir, name)
		target := strings.TrimSuffix(temp, ".tmp")
		if _, statErr := os.Stat(target); statErr != nil {
			if !os.IsNotExist(statErr) {
				return fmt.Errorf("stat recovery target %s: %w", target, statErr)
			}
			if err := os.Rename(temp, target); err != nil {
				return err
			}
			changed = true
		} else if err := os.Remove(temp); err != nil {
			return fmt.Errorf("remove stale recovery temp %s: %w", temp, err)
		} else {
			changed = true
		}
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".json.bak") {
			continue
		}
		backup := filepath.Join(m.dir, name)
		target := strings.TrimSuffix(backup, ".bak")
		if _, statErr := os.Stat(target); statErr != nil {
			if !os.IsNotExist(statErr) {
				return fmt.Errorf("stat recovery target %s: %w", target, statErr)
			}
			if err := os.Rename(backup, target); err != nil {
				return err
			}
			changed = true
		} else if err := os.Remove(backup); err != nil {
			return fmt.Errorf("remove stale recovery backup %s: %w", backup, err)
		} else {
			changed = true
		}
	}
	if changed {
		return syncDirectory(m.dir)
	}
	return nil
}

func (m *JobManager) path(id string) string {
	return filepath.Join(m.dir, id+".json")
}

func validJobID(id string) bool {
	if len(id) != 24 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func newJobID() (string, error) {
	bytes := make([]byte, 12)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

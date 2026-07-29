package shell

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	JobStateStopped     = "stopped"
	JobStateInterrupted = "interrupted"
)

type Job struct {
	JobID       string     `json:"job_id"`
	Command     string     `json:"command"`
	Workdir     string     `json:"workdir,omitempty"`
	State       string     `json:"state"`
	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   time.Time  `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	ExitCode    *int       `json:"exit_code,omitempty"`
	Error       string     `json:"error,omitempty"`
	Output      string     `json:"output"`
	OutputBytes int64      `json:"output_bytes"`
	Truncated   bool       `json:"truncated"`
}

type JobManager struct {
	executor       *Executor
	dir            string
	retention      time.Duration
	maxRuntime     time.Duration
	maxOutputBytes int
	maxJobs        int

	mu          sync.RWMutex
	persistMu   sync.Mutex
	jobs        map[string]*Job
	cancels     map[string]context.CancelFunc
	lastPersist map[string]time.Time
}

func NewJobManager(
	executor *Executor,
	dir string,
	retention time.Duration,
	maxRuntime time.Duration,
	maxOutputBytes int,
	maxJobs int,
) (*JobManager, error) {
	if executor == nil {
		return nil, errors.New("executor is required")
	}
	if dir == "" {
		return nil, errors.New("job directory is required")
	}
	if maxRuntime <= 0 || maxOutputBytes <= 0 || maxJobs <= 0 {
		return nil, errors.New("job limits must be positive")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	manager := &JobManager{
		executor:       executor,
		dir:            dir,
		retention:      retention,
		maxRuntime:     maxRuntime,
		maxOutputBytes: maxOutputBytes,
		maxJobs:        maxJobs,
		jobs:           make(map[string]*Job),
		cancels:        make(map[string]context.CancelFunc),
		lastPersist:    make(map[string]time.Time),
	}
	if err := manager.load(); err != nil {
		return nil, err
	}
	manager.cleanup()
	return manager, nil
}

func (m *JobManager) Start(req Request) (Job, error) {
	if strings.TrimSpace(req.Command) == "" {
		return Job{}, errors.New("command is required")
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
		JobID:     id,
		Command:   req.Command,
		Workdir:   req.Workdir,
		State:     JobStateRunning,
		CreatedAt: now,
		StartedAt: now,
		Output:    "",
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.jobs[id] = job
	m.cancels[id] = cancel
	snapshot := *job
	m.mu.Unlock()
	if err := m.persist(snapshot); err != nil {
		m.mu.Lock()
		delete(m.jobs, id)
		delete(m.cancels, id)
		m.mu.Unlock()
		cancel()
		return Job{}, err
	}
	go m.run(ctx, req, id)
	return snapshot, nil
}

func (m *JobManager) List() []Job {
	m.cleanup()
	m.mu.RLock()
	jobs := make([]Job, 0, len(m.jobs))
	for _, job := range m.jobs {
		jobs = append(jobs, *job)
	}
	m.mu.RUnlock()
	sort.Slice(jobs, func(i, j int) bool {
		return jobs[i].CreatedAt.After(jobs[j].CreatedAt)
	})
	return jobs
}

func (m *JobManager) Get(id string) (Job, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	job, ok := m.jobs[id]
	if !ok {
		return Job{}, false
	}
	return *job, true
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
	snapshot := *job
	m.mu.Unlock()
	_ = m.persist(snapshot)
	if cancel != nil {
		cancel()
	}
	return snapshot, true
}

func (m *JobManager) run(ctx context.Context, req Request, id string) {
	if req.TimeoutMs <= 0 {
		req.TimeoutMs = int(m.maxRuntime / time.Millisecond)
	}
	events := m.executor.ExecuteWithMaxTimeout(ctx, req, m.maxRuntime)
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
		if event.Error != "" {
			job.Error = event.Error
			job.State = JobStateFailed
		}
		if event.ExitCode != nil {
			code := *event.ExitCode
			job.ExitCode = &code
			if ctx.Err() != nil || job.State == JobStateStopping {
				job.State = JobStateStopped
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
			_ = m.persist(snapshot)
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
	_ = m.persist(snapshot)
	m.cleanup()
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
		if json.Unmarshal(data, &job) != nil || job.JobID == "" {
			continue
		}
		if job.State == JobStateRunning || job.State == JobStateStopping {
			job.State = JobStateInterrupted
			job.Error = "conch restarted while the job was running"
			job.FinishedAt = &now
			_ = m.persist(job)
		}
		m.jobs[job.JobID] = &job
	}
	return nil
}

func (m *JobManager) cleanup() {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	type candidate struct {
		id      string
		created time.Time
	}
	completed := make([]candidate, 0)
	for id, job := range m.jobs {
		if job.State == JobStateRunning || job.State == JobStateStopping {
			continue
		}
		if m.retention > 0 && now.Sub(job.CreatedAt) > m.retention {
			delete(m.jobs, id)
			_ = os.Remove(m.path(id))
			continue
		}
		completed = append(completed, candidate{id: id, created: job.CreatedAt})
	}
	if len(m.jobs) <= m.maxJobs {
		return
	}
	sort.Slice(completed, func(i, j int) bool {
		return completed[i].created.Before(completed[j].created)
	})
	for _, item := range completed {
		if len(m.jobs) <= m.maxJobs {
			break
		}
		delete(m.jobs, item.id)
		_ = os.Remove(m.path(item.id))
	}
}

func (m *JobManager) persist(job Job) error {
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
	_ = os.Remove(backup)
	return nil
}

func (m *JobManager) recoverSnapshots() error {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return err
	}
	// A complete .tmp contains the newest durable snapshot. Recover those before
	// considering .bak files so directory iteration order cannot resurrect stale data.
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".json.tmp") {
			continue
		}
		temp := filepath.Join(m.dir, name)
		target := strings.TrimSuffix(temp, ".tmp")
		if _, err := os.Stat(target); os.IsNotExist(err) {
			if err := os.Rename(temp, target); err != nil {
				return err
			}
		} else {
			_ = os.Remove(temp)
		}
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".json.bak") {
			continue
		}
		backup := filepath.Join(m.dir, name)
		target := strings.TrimSuffix(backup, ".bak")
		if _, err := os.Stat(target); os.IsNotExist(err) {
			if err := os.Rename(backup, target); err != nil {
				return err
			}
		} else {
			_ = os.Remove(backup)
		}
	}
	return nil
}

func (m *JobManager) path(id string) string {
	return filepath.Join(m.dir, id+".json")
}

func newJobID() (string, error) {
	bytes := make([]byte, 12)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

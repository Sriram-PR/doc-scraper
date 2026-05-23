package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

type JobStatus string

const (
	JobStatusPending   JobStatus = "pending"
	JobStatusRunning   JobStatus = "running"
	JobStatusCompleted JobStatus = "completed"
	JobStatusFailed    JobStatus = "failed"
	JobStatusCancelled JobStatus = "cancelled"
)

const (
	jobsFilename     = "mcp_jobs.json"
	jobsFileVersion  = 1
	flushInterval    = 2 * time.Second
	restartFailedMsg = "MCP server restarted during crawl"
)

type Job struct {
	ID             string    `json:"id"`
	SiteKey        string    `json:"site_key"`
	Status         JobStatus `json:"status"`
	StartedAt      time.Time `json:"started_at"`
	CompletedAt    time.Time `json:"completed_at,omitempty"`
	PagesProcessed int64     `json:"pages_processed"`
	PagesQueued    int64     `json:"pages_queued"`
	ErrorMessage   string    `json:"error_message,omitempty"`
	Incremental    bool      `json:"incremental"`

	ctx    context.Context
	cancel context.CancelFunc
}

type jobsFile struct {
	Version int    `json:"version"`
	Jobs    []*Job `json:"jobs"`
}

type JobManager struct {
	jobs   map[string]*Job
	mu     sync.RWMutex
	bysite map[string]string // siteKey -> jobID for running jobs

	persistPath string // empty disables persistence
	log         *logrus.Entry

	flushMu   sync.Mutex // serializes disk writes; never held with mu
	dirty     atomic.Bool
	stopFlush chan struct{}
	flushDone chan struct{}
}

// NewJobManager returns a JobManager. With an empty persistPath the manager is
// purely in-memory. With a path it loads existing jobs (in-flight ones reload
// as failed) and persists state changes; state mutations flush immediately while
// UpdateProgress is debounced. Callers must Stop() during shutdown to drain the
// background flusher.
func NewJobManager(persistPath string, log *logrus.Entry) *JobManager {
	m := &JobManager{
		jobs:        make(map[string]*Job),
		bysite:      make(map[string]string),
		persistPath: persistPath,
		log:         log,
	}
	if persistPath == "" {
		return m
	}
	m.load()
	m.stopFlush = make(chan struct{})
	m.flushDone = make(chan struct{})
	go m.flushLoop()
	return m
}

// load reads the persisted jobs and reclassifies in-flight ones as Failed.
// Errors are non-fatal; a missing file is normal on first run.
func (m *JobManager) load() {
	data, err := os.ReadFile(m.persistPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) && m.log != nil {
			m.log.Warnf("failed to read jobs file %s: %v", m.persistPath, err)
		}
		return
	}
	var file jobsFile
	if err := json.Unmarshal(data, &file); err != nil {
		if m.log != nil {
			m.log.Warnf("failed to parse jobs file %s: %v", m.persistPath, err)
		}
		return
	}
	now := time.Now()
	// Shared already-cancelled context: loaded jobs are terminal and only need
	// a non-nil ctx for GetContext.
	deadCtx, deadCancel := context.WithCancel(context.Background())
	deadCancel()
	for _, job := range file.Jobs {
		if job == nil || job.ID == "" {
			continue
		}
		if job.Status == JobStatusPending || job.Status == JobStatusRunning {
			job.Status = JobStatusFailed
			job.ErrorMessage = restartFailedMsg
			if job.CompletedAt.IsZero() {
				job.CompletedAt = now
			}
		}
		job.ctx = deadCtx //nolint:fatcontext // shared dead context, see above
		job.cancel = deadCancel
		m.jobs[job.ID] = job
	}
	if m.log != nil {
		m.log.Infof("loaded %d MCP jobs from %s", len(m.jobs), m.persistPath)
	}
}

// snapshotLocked returns a deep copy of the jobs for serialization. Caller
// must hold m.mu.
func (m *JobManager) snapshotLocked() []*Job {
	out := make([]*Job, 0, len(m.jobs))
	for _, job := range m.jobs {
		copy := *job
		copy.ctx = nil
		copy.cancel = nil
		out = append(out, &copy)
	}
	return out
}

// flush atomically writes the current job state to disk. Serialized by flushMu.
func (m *JobManager) flush() {
	if m.persistPath == "" {
		return
	}
	m.mu.RLock()
	file := jobsFile{Version: jobsFileVersion, Jobs: m.snapshotLocked()}
	m.mu.RUnlock()

	data, err := json.MarshalIndent(&file, "", "  ")
	if err != nil {
		if m.log != nil {
			m.log.Warnf("failed to marshal jobs: %v", err)
		}
		return
	}

	m.flushMu.Lock()
	defer m.flushMu.Unlock()

	if err := os.MkdirAll(filepath.Dir(m.persistPath), 0o755); err != nil {
		if m.log != nil {
			m.log.Warnf("failed to create state dir for jobs file: %v", err)
		}
		return
	}
	tmp := m.persistPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		if m.log != nil {
			m.log.Warnf("failed to write jobs tmp file: %v", err)
		}
		return
	}
	if err := os.Rename(tmp, m.persistPath); err != nil {
		if m.log != nil {
			m.log.Warnf("failed to rename jobs tmp file: %v", err)
		}
		_ = os.Remove(tmp)
		return
	}
	m.dirty.Store(false)
}

func (m *JobManager) flushLoop() {
	defer close(m.flushDone)
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if m.dirty.Load() {
				m.flush()
			}
		case <-m.stopFlush:
			return
		}
	}
}

// Stop halts the background flusher and performs a final flush. Idempotent.
func (m *JobManager) Stop() {
	if m.persistPath == "" || m.stopFlush == nil {
		return
	}
	select {
	case <-m.stopFlush:
	default:
		close(m.stopFlush)
		<-m.flushDone
	}
	m.flush()
}

// CreateJob returns the existing pending/running job for siteKey if one exists,
// otherwise creates and returns a new one.
func (m *JobManager) CreateJob(siteKey string, incremental bool) (*Job, error) {
	m.mu.Lock()

	if existingJobID, exists := m.bysite[siteKey]; exists {
		existingJob := m.jobs[existingJobID]
		if existingJob != nil && (existingJob.Status == JobStatusPending || existingJob.Status == JobStatusRunning) {
			m.mu.Unlock()
			return existingJob, nil
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	job := &Job{
		ID:          uuid.New().String(),
		SiteKey:     siteKey,
		Status:      JobStatusPending,
		StartedAt:   time.Now(),
		Incremental: incremental,
		ctx:         ctx,
		cancel:      cancel,
	}

	m.jobs[job.ID] = job
	m.bysite[siteKey] = job.ID
	m.mu.Unlock()

	m.flush()
	return job, nil
}

func (m *JobManager) GetJob(jobID string) *Job {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.jobs[jobID]
}

func (m *JobManager) GetJobBySite(siteKey string) *Job {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if jobID, exists := m.bysite[siteKey]; exists {
		return m.jobs[jobID]
	}
	return nil
}

func (m *JobManager) IsRunning(siteKey string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if jobID, exists := m.bysite[siteKey]; exists {
		job := m.jobs[jobID]
		return job != nil && (job.Status == JobStatusPending || job.Status == JobStatusRunning)
	}
	return false
}

func (m *JobManager) UpdateStatus(jobID string, status JobStatus, errorMsg string) {
	m.mu.Lock()
	changed := false
	if job, exists := m.jobs[jobID]; exists {
		job.Status = status
		if status == JobStatusCompleted || status == JobStatusFailed || status == JobStatusCancelled {
			job.CompletedAt = time.Now()
			delete(m.bysite, job.SiteKey)
		}
		if errorMsg != "" {
			job.ErrorMessage = errorMsg
		}
		changed = true
	}
	m.mu.Unlock()

	if changed {
		m.flush()
	}
}

// UpdateProgress only marks the manager dirty; the background flusher writes
// to disk a few seconds later so per-page calls do not hammer disk.
func (m *JobManager) UpdateProgress(jobID string, processed, queued int64) {
	m.mu.Lock()
	if job, exists := m.jobs[jobID]; exists {
		job.PagesProcessed = processed
		job.PagesQueued = queued
		m.dirty.Store(true)
	}
	m.mu.Unlock()
}

func (m *JobManager) CancelJob(jobID string) bool {
	m.mu.Lock()
	cancelled := false
	if job, exists := m.jobs[jobID]; exists {
		if job.Status == JobStatusPending || job.Status == JobStatusRunning {
			job.cancel()
			job.Status = JobStatusCancelled
			job.CompletedAt = time.Now()
			delete(m.bysite, job.SiteKey)
			cancelled = true
		}
	}
	m.mu.Unlock()

	if cancelled {
		m.flush()
	}
	return cancelled
}

func (m *JobManager) CancelAll() {
	m.mu.Lock()
	for _, job := range m.jobs {
		if job.Status == JobStatusPending || job.Status == JobStatusRunning {
			job.cancel()
			job.Status = JobStatusCancelled
			job.CompletedAt = time.Now()
		}
	}
	m.bysite = make(map[string]string)
	m.mu.Unlock()

	m.flush()
}

func (m *JobManager) ListJobs() []*Job {
	m.mu.RLock()
	defer m.mu.RUnlock()

	jobs := make([]*Job, 0, len(m.jobs))
	for _, job := range m.jobs {
		jobs = append(jobs, job)
	}
	return jobs
}

func (m *JobManager) GetContext(jobID string) context.Context {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if job, exists := m.jobs[jobID]; exists {
		return job.ctx
	}
	return context.Background()
}


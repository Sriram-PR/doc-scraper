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

// JobStatus represents the current state of a crawl job
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

// Job represents a background crawl job
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

	// Internal fields
	ctx    context.Context
	cancel context.CancelFunc
}

// jobsFile is the on-disk representation of the JobManager state.
type jobsFile struct {
	Version int    `json:"version"`
	Jobs    []*Job `json:"jobs"`
}

// JobManager manages background crawl jobs
type JobManager struct {
	jobs   map[string]*Job
	mu     sync.RWMutex
	bysite map[string]string // siteKey -> jobID for running jobs

	persistPath string // empty disables persistence
	log         *logrus.Entry

	flushMu   sync.Mutex   // serializes disk writes; never held with mu
	dirty     atomic.Bool  // set by UpdateProgress; cleared by flush
	stopFlush chan struct{}
	flushDone chan struct{}
}

// NewJobManager creates a job manager. A blank persistPath disables persistence
// entirely and the manager runs purely in-memory. With a path, the manager loads
// any existing jobs file (treating in-flight jobs as failed since their goroutines
// did not survive the restart) and persists state changes back to it. State-changing
// operations (create, status, cancel) flush immediately; progress updates are
// debounced and written by a background flusher. Callers must invoke Stop() during
// shutdown to flush a final time and stop the goroutine. A nil logger silences
// load and flush warnings.
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

// load reads persisted jobs from disk. Pending/Running jobs are marked
// Failed since their goroutines did not survive the restart. Errors are
// logged but never fatal: a missing file is normal on first run.
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
		// Reattach a cancelled context so GetContext always returns a usable value.
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		job.ctx = ctx
		job.cancel = cancel
		m.jobs[job.ID] = job
	}
	if m.log != nil {
		m.log.Infof("loaded %d MCP jobs from %s", len(m.jobs), m.persistPath)
	}
}

// snapshotLocked returns a copy of the jobs slice for serialization. Caller
// must hold m.mu (read or write).
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

// flush writes the current job state to disk atomically. Safe to call from
// any goroutine; serialized by flushMu. No-op if persistence is disabled.
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

// flushLoop periodically flushes dirty state set by UpdateProgress.
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

// Stop halts the background flusher and performs a final flush. Safe to
// call multiple times. No-op if persistence is disabled.
func (m *JobManager) Stop() {
	if m.persistPath == "" || m.stopFlush == nil {
		return
	}
	select {
	case <-m.stopFlush:
		// already stopped
	default:
		close(m.stopFlush)
		<-m.flushDone
	}
	m.flush()
}

// CreateJob creates a new job for a site
func (m *JobManager) CreateJob(siteKey string, incremental bool) (*Job, error) {
	m.mu.Lock()

	// Check if a job is already running for this site
	if existingJobID, exists := m.bysite[siteKey]; exists {
		existingJob := m.jobs[existingJobID]
		if existingJob != nil && (existingJob.Status == JobStatusPending || existingJob.Status == JobStatusRunning) {
			m.mu.Unlock()
			return existingJob, nil // Return existing running job
		}
	}

	// Create new job
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

// GetJob retrieves a job by ID
func (m *JobManager) GetJob(jobID string) *Job {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.jobs[jobID]
}

// GetJobBySite retrieves the current job for a site
func (m *JobManager) GetJobBySite(siteKey string) *Job {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if jobID, exists := m.bysite[siteKey]; exists {
		return m.jobs[jobID]
	}
	return nil
}

// IsRunning checks if a job is currently running for a site
func (m *JobManager) IsRunning(siteKey string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if jobID, exists := m.bysite[siteKey]; exists {
		job := m.jobs[jobID]
		return job != nil && (job.Status == JobStatusPending || job.Status == JobStatusRunning)
	}
	return false
}

// UpdateStatus updates the status of a job
func (m *JobManager) UpdateStatus(jobID string, status JobStatus, errorMsg string) {
	m.mu.Lock()
	changed := false
	if job, exists := m.jobs[jobID]; exists {
		job.Status = status
		if status == JobStatusCompleted || status == JobStatusFailed || status == JobStatusCancelled {
			job.CompletedAt = time.Now()
			// Remove from bysite to allow new jobs
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

// UpdateProgress updates the progress counters of a job. Persistence is
// debounced: the change is written by the background flusher within a few
// seconds, so per-page calls do not hammer disk.
func (m *JobManager) UpdateProgress(jobID string, processed, queued int64) {
	m.mu.Lock()
	if job, exists := m.jobs[jobID]; exists {
		job.PagesProcessed = processed
		job.PagesQueued = queued
		m.dirty.Store(true)
	}
	m.mu.Unlock()
}

// CancelJob cancels a running job
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

// CancelAll cancels all running jobs
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

// ListJobs returns all jobs
func (m *JobManager) ListJobs() []*Job {
	m.mu.RLock()
	defer m.mu.RUnlock()

	jobs := make([]*Job, 0, len(m.jobs))
	for _, job := range m.jobs {
		jobs = append(jobs, job)
	}
	return jobs
}

// GetContext returns the context for a job (for running the crawler)
func (m *JobManager) GetContext(jobID string) context.Context {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if job, exists := m.jobs[jobID]; exists {
		return job.ctx
	}
	return context.Background()
}


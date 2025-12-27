package mcp

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
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

// JobManager manages background crawl jobs
type JobManager struct {
	jobs   map[string]*Job
	mu     sync.RWMutex
	bysite map[string]string // siteKey -> jobID for running jobs
}

// NewJobManager creates a new job manager
func NewJobManager() *JobManager {
	return &JobManager{
		jobs:   make(map[string]*Job),
		bysite: make(map[string]string),
	}
}

// CreateJob creates a new job for a site
func (m *JobManager) CreateJob(siteKey string, incremental bool) (*Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if a job is already running for this site
	if existingJobID, exists := m.bysite[siteKey]; exists {
		existingJob := m.jobs[existingJobID]
		if existingJob != nil && (existingJob.Status == JobStatusPending || existingJob.Status == JobStatusRunning) {
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
	defer m.mu.Unlock()

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
	}
}

// UpdateProgress updates the progress counters of a job
func (m *JobManager) UpdateProgress(jobID string, processed, queued int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if job, exists := m.jobs[jobID]; exists {
		job.PagesProcessed = processed
		job.PagesQueued = queued
	}
}

// CancelJob cancels a running job
func (m *JobManager) CancelJob(jobID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	if job, exists := m.jobs[jobID]; exists {
		if job.Status == JobStatusPending || job.Status == JobStatusRunning {
			job.cancel()
			job.Status = JobStatusCancelled
			job.CompletedAt = time.Now()
			delete(m.bysite, job.SiteKey)
			return true
		}
	}
	return false
}

// CancelAll cancels all running jobs
func (m *JobManager) CancelAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, job := range m.jobs {
		if job.Status == JobStatusPending || job.Status == JobStatusRunning {
			job.cancel()
			job.Status = JobStatusCancelled
			job.CompletedAt = time.Now()
		}
	}
	m.bysite = make(map[string]string)
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

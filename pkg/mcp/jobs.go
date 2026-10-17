package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
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
	// maxTerminalJobs bounds retained finished jobs so a long-lived server does
	// not grow m.jobs (and the persisted file) without limit. Active jobs are
	// never pruned.
	maxTerminalJobs = 100
)

func isTerminalStatus(s JobStatus) bool {
	return s == JobStatusCompleted || s == JobStatusFailed || s == JobStatusCancelled
}

type Job struct {
	ID      string    `json:"id"`
	SiteKey string    `json:"site_key"`
	Status  JobStatus `json:"status"`
	// Seq is a per-manager creation counter. StartedAt alone cannot order jobs
	// on a platform whose clock is too coarse to separate two CreateJob calls
	// (Windows), and job IDs are random UUIDs, so it takes a monotonic key to
	// keep "newest first" meaning what it says.
	Seq            uint64    `json:"seq"`
	StartedAt      time.Time `json:"started_at"`
	CompletedAt    time.Time `json:"completed_at,omitempty"`
	PagesProcessed int64     `json:"pages_processed"`
	PagesQueued    int64     `json:"pages_queued"`
	PagesUnchanged int64     `json:"pages_unchanged"` // incremental re-checks that matched; not in PagesProcessed
	ErrorMessage   string    `json:"error_message,omitempty"`
	Incremental    bool      `json:"incremental"`

	ctx    context.Context
	cancel context.CancelFunc
	// progress reads the running crawler's counters; nil once the crawl ends.
	progress func() JobProgress
}

// JobProgress is a point-in-time read of a crawl's counters.
type JobProgress struct {
	Processed int64
	Queued    int64
	Unchanged int64
}

type siteWorker struct {
	job  *Job
	done chan struct{}
}

type jobsFile struct {
	Version int    `json:"version"`
	Jobs    []*Job `json:"jobs"`
}

type JobManager struct {
	jobs   map[string]*Job
	mu     sync.RWMutex
	bysite map[string]string // siteKey -> jobID for running jobs
	// workers holds each site whose crawl goroutine has not returned yet. It
	// outlives bysite after a cancel, while the crawl unwinds with the site's
	// store still open.
	workers map[string]*siteWorker

	persistPath string // empty disables persistence
	log         *slog.Logger

	seq atomic.Uint64 // monotonic creation counter; see Job.Seq

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
func NewJobManager(persistPath string, log *slog.Logger) *JobManager {
	m := &JobManager{
		jobs:        make(map[string]*Job),
		bysite:      make(map[string]string),
		workers:     make(map[string]*siteWorker),
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
			m.log.Warn("failed to read jobs file", "path", m.persistPath, "err", err)
		}
		return
	}
	var file jobsFile
	if err := json.Unmarshal(data, &file); err != nil {
		if m.log != nil {
			m.log.Warn("failed to parse jobs file", "path", m.persistPath, "err", err)
		}
		return
	}
	now := time.Now()
	// Shared already-cancelled context: loaded jobs are terminal and only need
	// a non-nil ctx for GetContext.
	deadCtx, deadCancel := context.WithCancel(context.Background())
	deadCancel()
	var maxSeq uint64
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
		if job.Seq > maxSeq {
			maxSeq = job.Seq
		}
	}
	// Resume numbering above the loaded jobs so a restart cannot hand out a
	// sequence that sorts below jobs already on disk.
	m.seq.Store(maxSeq)
	m.pruneTerminalLocked()
	if m.log != nil {
		m.log.Info("loaded MCP jobs", "count", len(m.jobs), "path", m.persistPath)
	}
}

// viewLocked returns a copy of job with live counters filled in, so readers
// never share memory with the running crawl. Caller must hold m.mu.
func viewLocked(job *Job) *Job {
	view := *job
	view.ctx = nil
	view.cancel = nil
	view.progress = nil
	if job.progress != nil {
		view.applyProgress(job.progress())
	}
	return &view
}

func (j *Job) applyProgress(p JobProgress) {
	j.PagesProcessed = p.Processed
	j.PagesQueued = p.Queued
	j.PagesUnchanged = p.Unchanged
}

// snapshotLocked returns a deep copy of the jobs for serialization. Caller
// must hold m.mu.
func (m *JobManager) snapshotLocked() []*Job {
	out := make([]*Job, 0, len(m.jobs))
	for _, job := range m.jobs {
		out = append(out, viewLocked(job))
	}
	return out
}

// pruneTerminalLocked keeps only the most recent maxTerminalJobs terminal jobs,
// deleting the oldest by CompletedAt. Active (pending/running) jobs are always
// retained. Caller must hold m.mu.
// sortJobsNewestFirst orders jobs for display, most recent first. ListJobs
// walks a map, so the comparator has to be a total order or that random order
// leaks into results whenever two jobs share a timestamp.
func sortJobsNewestFirst(jobs []*Job) {
	sort.Slice(jobs, func(i, j int) bool {
		a, b := jobs[i], jobs[j]
		if !a.StartedAt.Equal(b.StartedAt) {
			return a.StartedAt.After(b.StartedAt)
		}
		if a.Seq != b.Seq {
			return a.Seq > b.Seq
		}
		return a.ID < b.ID
	})
}

func (m *JobManager) pruneTerminalLocked() {
	var terminal []*Job
	for _, job := range m.jobs {
		if isTerminalStatus(job.Status) {
			terminal = append(terminal, job)
		}
	}
	if len(terminal) <= maxTerminalJobs {
		return
	}
	// Oldest first; the pruner deletes from the front, so a comparator that
	// leaves ties unordered would delete an arbitrary subset of jobs that
	// finished in the same clock tick. Seq then ID make this a total order.
	sort.Slice(terminal, func(i, j int) bool {
		a, b := terminal[i], terminal[j]
		if !a.CompletedAt.Equal(b.CompletedAt) {
			return a.CompletedAt.Before(b.CompletedAt)
		}
		if a.Seq != b.Seq {
			return a.Seq < b.Seq
		}
		return a.ID < b.ID
	})
	for _, job := range terminal[:len(terminal)-maxTerminalJobs] {
		delete(m.jobs, job.ID)
	}
}

// flush atomically writes the current job state to disk. flushMu is held across
// the whole snapshot-and-write so that when flushes overlap the last writer
// always captures the freshest state; otherwise a stale snapshot could win the
// write race and persist while dirty is cleared.
func (m *JobManager) flush() {
	if m.persistPath == "" {
		return
	}
	m.flushMu.Lock()
	defer m.flushMu.Unlock()

	m.mu.RLock()
	file := jobsFile{Version: jobsFileVersion, Jobs: m.snapshotLocked()}
	m.mu.RUnlock()

	data, err := json.MarshalIndent(&file, "", "  ")
	if err != nil {
		if m.log != nil {
			m.log.Warn("failed to marshal jobs", "err", err)
		}
		return
	}

	if err := os.MkdirAll(filepath.Dir(m.persistPath), 0o755); err != nil {
		if m.log != nil {
			m.log.Warn("failed to create state dir for jobs file", "err", err)
		}
		return
	}
	tmp := m.persistPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		if m.log != nil {
			m.log.Warn("failed to write jobs tmp file", "err", err)
		}
		return
	}
	if err := os.Rename(tmp, m.persistPath); err != nil {
		if m.log != nil {
			m.log.Warn("failed to rename jobs tmp file", "err", err)
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
	if job := m.activeJobLocked(siteKey); job != nil {
		m.mu.Unlock()
		return job, nil
	}
	job := m.newJobLocked(siteKey, incremental)
	m.mu.Unlock()

	m.flush()
	return job, nil
}

// StartJob creates a job for siteKey and reserves the site for its crawl until
// FinishJob. created is false when the site already has an active job, which
// is returned instead. If an earlier crawl is still shutting down, StartJob
// waits up to wait for it and then returns that job with created false.
func (m *JobManager) StartJob(ctx context.Context, siteKey string, incremental bool, wait time.Duration) (job *Job, created bool, err error) {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		m.mu.Lock()
		if active := m.activeJobLocked(siteKey); active != nil {
			view := viewLocked(active)
			m.mu.Unlock()
			return view, false, nil
		}
		w := m.workers[siteKey]
		if w == nil {
			job := m.newJobLocked(siteKey, incremental)
			m.workers[siteKey] = &siteWorker{job: job, done: make(chan struct{})}
			m.mu.Unlock()
			m.flush()
			return job, true, nil
		}
		m.mu.Unlock()

		select {
		case <-w.done:
		case <-timer.C:
			m.mu.RLock()
			view := viewLocked(w.job)
			m.mu.RUnlock()
			return view, false, nil
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
	}
}

// FinishJob releases the site reservation StartJob took for jobID. Call it once
// the crawl has closed everything it opened.
func (m *JobManager) FinishJob(jobID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for site, w := range m.workers {
		if w.job.ID == jobID {
			close(w.done)
			delete(m.workers, site)
			return
		}
	}
}

// WaitIdle waits until every crawl started with StartJob has called FinishJob,
// or ctx is done.
func (m *JobManager) WaitIdle(ctx context.Context) error {
	for {
		m.mu.RLock()
		var w *siteWorker
		for _, w = range m.workers {
			break
		}
		m.mu.RUnlock()
		if w == nil {
			return nil
		}
		select {
		case <-w.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// activeJobLocked returns siteKey's pending or running job, or nil. Caller must
// hold m.mu.
func (m *JobManager) activeJobLocked(siteKey string) *Job {
	job := m.jobs[m.bysite[siteKey]]
	if job != nil && (job.Status == JobStatusPending || job.Status == JobStatusRunning) {
		return job
	}
	return nil
}

// newJobLocked registers a pending job as siteKey's active job. Caller must
// hold m.mu.
func (m *JobManager) newJobLocked(siteKey string, incremental bool) *Job {
	ctx, cancel := context.WithCancel(context.Background())
	job := &Job{
		ID:          uuid.New().String(),
		SiteKey:     siteKey,
		Status:      JobStatusPending,
		Seq:         m.seq.Add(1),
		StartedAt:   time.Now(),
		Incremental: incremental,
		ctx:         ctx,
		cancel:      cancel,
	}
	m.jobs[job.ID] = job
	m.bysite[siteKey] = job.ID
	return job
}

// GetJob returns a copy of the job with live progress, or nil.
func (m *JobManager) GetJob(jobID string) *Job {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if job, exists := m.jobs[jobID]; exists {
		return viewLocked(job)
	}
	return nil
}

// GetJobBySite returns a copy of the site's active job with live progress, or nil.
func (m *JobManager) GetJobBySite(siteKey string) *Job {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if jobID, exists := m.bysite[siteKey]; exists {
		if job, ok := m.jobs[jobID]; ok {
			return viewLocked(job)
		}
	}
	return nil
}

func (m *JobManager) IsRunning(siteKey string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.activeJobLocked(siteKey) != nil
}

// releaseSiteLocked frees job's site unless a newer job has already taken it.
// Caller must hold m.mu.
func (m *JobManager) releaseSiteLocked(job *Job) {
	if m.bysite[job.SiteKey] == job.ID {
		delete(m.bysite, job.SiteKey)
	}
}

func (m *JobManager) UpdateStatus(jobID string, status JobStatus, errorMsg string) {
	m.mu.Lock()
	changed := false
	if job, exists := m.jobs[jobID]; exists {
		job.Status = status
		if status == JobStatusCompleted || status == JobStatusFailed || status == JobStatusCancelled {
			job.CompletedAt = time.Now()
			m.releaseSiteLocked(job)
		}
		if errorMsg != "" {
			job.ErrorMessage = errorMsg
		}
		if isTerminalStatus(status) {
			m.pruneTerminalLocked()
		}
		changed = true
	}
	m.mu.Unlock()

	if changed {
		m.flush()
	}
}

// AttachProgress makes reads of the job report fn's counters until
// DetachProgress, instead of the values last pushed through UpdateProgress.
func (m *JobManager) AttachProgress(jobID string, fn func() JobProgress) {
	m.mu.Lock()
	if job, exists := m.jobs[jobID]; exists {
		job.progress = fn
	}
	m.mu.Unlock()
}

// DetachProgress stores the attached source's final counters on the job and
// drops the source.
func (m *JobManager) DetachProgress(jobID string) {
	m.mu.Lock()
	if job, exists := m.jobs[jobID]; exists && job.progress != nil {
		job.applyProgress(job.progress())
		job.progress = nil
		m.dirty.Store(true)
	}
	m.mu.Unlock()
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
			m.releaseSiteLocked(job)
			cancelled = true
		}
	}
	if cancelled {
		m.pruneTerminalLocked()
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
	m.pruneTerminalLocked()
	m.mu.Unlock()

	m.flush()
}

func (m *JobManager) ListJobs() []*Job {
	m.mu.RLock()
	defer m.mu.RUnlock()

	jobs := make([]*Job, 0, len(m.jobs))
	for _, job := range m.jobs {
		jobs = append(jobs, viewLocked(job))
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

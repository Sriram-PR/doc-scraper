package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"log/slog"
)

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

func createTestJob(t *testing.T, jm *JobManager, siteKey string, incremental bool) *Job {
	t.Helper()
	job, err := jm.CreateJob(siteKey, incremental)
	require.NoError(t, err)
	require.NotNil(t, job)
	return job
}

func TestNewJobManager(t *testing.T) {
	jm := NewJobManager("", nil)
	require.NotNil(t, jm)
	assert.Empty(t, jm.ListJobs())
}

func TestJobRetention_CapsTerminalJobs(t *testing.T) {
	jm := NewJobManager("", newTestLogger())
	for i := range maxTerminalJobs + 10 {
		j := createTestJob(t, jm, "site-"+strconv.Itoa(i), false)
		jm.UpdateStatus(j.ID, JobStatusCompleted, "")
	}
	assert.Len(t, jm.ListJobs(), maxTerminalJobs)
}

func TestJobRetention_PrunesOneOverTheCap(t *testing.T) {
	jm := NewJobManager("", newTestLogger())
	var first *Job
	for i := range maxTerminalJobs + 1 {
		j := createTestJob(t, jm, "site-"+strconv.Itoa(i), false)
		if first == nil {
			first = j
		}
		jm.UpdateStatus(j.ID, JobStatusCompleted, "")
	}
	assert.Len(t, jm.ListJobs(), maxTerminalJobs)
	assert.Nil(t, jm.GetJob(first.ID), "the oldest job should be the one pruned")
}

func TestJobRetention_NeverPrunesActive(t *testing.T) {
	jm := NewJobManager("", newTestLogger())
	running := createTestJob(t, jm, "running-site", false)
	jm.UpdateStatus(running.ID, JobStatusRunning, "")

	for i := range maxTerminalJobs + 20 {
		j := createTestJob(t, jm, "done-"+strconv.Itoa(i), false)
		jm.UpdateStatus(j.ID, JobStatusCompleted, "")
	}

	assert.NotNil(t, jm.GetJob(running.ID), "active job must never be pruned")
	assert.Len(t, jm.ListJobs(), maxTerminalJobs+1)
}

func TestCreateJob(t *testing.T) {
	t.Run("new job fields correct", func(t *testing.T) {
		jm := NewJobManager("", nil)
		job := createTestJob(t, jm, "docs", true)

		assert.NotEmpty(t, job.ID)
		assert.Equal(t, "docs", job.SiteKey)
		assert.Equal(t, JobStatusPending, job.Status)
		assert.True(t, job.Incremental)
		assert.False(t, job.StartedAt.IsZero())
		assert.True(t, job.CompletedAt.IsZero())
		assert.Equal(t, int64(0), job.PagesProcessed)
		assert.Equal(t, int64(0), job.PagesQueued)
		assert.Empty(t, job.ErrorMessage)
	})

	t.Run("duplicate running site returns same job", func(t *testing.T) {
		jm := NewJobManager("", nil)
		job1 := createTestJob(t, jm, "docs", false)
		job2 := createTestJob(t, jm, "docs", false)
		assert.Equal(t, job1.ID, job2.ID)
	})

	t.Run("new job allowed after completion", func(t *testing.T) {
		jm := NewJobManager("", nil)
		job1 := createTestJob(t, jm, "docs", false)
		jm.UpdateStatus(job1.ID, JobStatusCompleted, "")

		job2 := createTestJob(t, jm, "docs", false)
		assert.NotEqual(t, job1.ID, job2.ID)
	})

	t.Run("different sites independent", func(t *testing.T) {
		jm := NewJobManager("", nil)
		job1 := createTestJob(t, jm, "site-a", false)
		job2 := createTestJob(t, jm, "site-b", false)
		assert.NotEqual(t, job1.ID, job2.ID)
	})
}

func TestGetJob(t *testing.T) {
	jm := NewJobManager("", nil)

	t.Run("exists returns job", func(t *testing.T) {
		job := createTestJob(t, jm, "docs", false)
		got := jm.GetJob(job.ID)
		require.NotNil(t, got)
		assert.Equal(t, job.ID, got.ID)
	})

	t.Run("missing returns nil", func(t *testing.T) {
		got := jm.GetJob("nonexistent-id")
		assert.Nil(t, got)
	})
}

func TestGetJobBySite(t *testing.T) {
	jm := NewJobManager("", nil)

	t.Run("exists returns job", func(t *testing.T) {
		job := createTestJob(t, jm, "docs", false)
		got := jm.GetJobBySite("docs")
		require.NotNil(t, got)
		assert.Equal(t, job.ID, got.ID)
	})

	t.Run("missing returns nil", func(t *testing.T) {
		got := jm.GetJobBySite("nonexistent")
		assert.Nil(t, got)
	})

	t.Run("returns nil after completion", func(t *testing.T) {
		job := createTestJob(t, jm, "finished-site", false)
		jm.UpdateStatus(job.ID, JobStatusCompleted, "")
		got := jm.GetJobBySite("finished-site")
		assert.Nil(t, got)
	})
}

func TestIsRunning(t *testing.T) {
	jm := NewJobManager("", nil)

	t.Run("true for pending", func(t *testing.T) {
		createTestJob(t, jm, "pending-site", false)
		assert.True(t, jm.IsRunning("pending-site"))
	})

	t.Run("true for running", func(t *testing.T) {
		job := createTestJob(t, jm, "running-site", false)
		jm.UpdateStatus(job.ID, JobStatusRunning, "")
		assert.True(t, jm.IsRunning("running-site"))
	})

	t.Run("false for completed", func(t *testing.T) {
		job := createTestJob(t, jm, "completed-site", false)
		jm.UpdateStatus(job.ID, JobStatusCompleted, "")
		assert.False(t, jm.IsRunning("completed-site"))
	})

	t.Run("false for failed", func(t *testing.T) {
		job := createTestJob(t, jm, "failed-site", false)
		jm.UpdateStatus(job.ID, JobStatusFailed, "something broke")
		assert.False(t, jm.IsRunning("failed-site"))
	})

	t.Run("false for cancelled", func(t *testing.T) {
		job := createTestJob(t, jm, "cancelled-site", false)
		jm.CancelJob(job.ID)
		assert.False(t, jm.IsRunning("cancelled-site"))
	})

	t.Run("false for nonexistent", func(t *testing.T) {
		assert.False(t, jm.IsRunning("ghost"))
	})
}

func TestUpdateStatus(t *testing.T) {
	t.Run("to running", func(t *testing.T) {
		jm := NewJobManager("", nil)
		job := createTestJob(t, jm, "docs", false)
		jm.UpdateStatus(job.ID, JobStatusRunning, "")
		assert.Equal(t, JobStatusRunning, jm.GetJob(job.ID).Status)
	})

	t.Run("to completed sets CompletedAt and cleans bysite", func(t *testing.T) {
		jm := NewJobManager("", nil)
		job := createTestJob(t, jm, "docs", false)
		jm.UpdateStatus(job.ID, JobStatusCompleted, "")

		got := jm.GetJob(job.ID)
		assert.Equal(t, JobStatusCompleted, got.Status)
		assert.False(t, got.CompletedAt.IsZero())
		assert.Nil(t, jm.GetJobBySite("docs"))
	})

	t.Run("to failed sets ErrorMessage", func(t *testing.T) {
		jm := NewJobManager("", nil)
		job := createTestJob(t, jm, "docs", false)
		jm.UpdateStatus(job.ID, JobStatusFailed, "out of memory")

		got := jm.GetJob(job.ID)
		assert.Equal(t, JobStatusFailed, got.Status)
		assert.Equal(t, "out of memory", got.ErrorMessage)
		assert.False(t, got.CompletedAt.IsZero())
	})

	t.Run("nonexistent is no-op", func(t *testing.T) {
		jm := NewJobManager("", nil)
		// Should not panic
		jm.UpdateStatus("fake-id", JobStatusRunning, "")
	})
}

func TestUpdateProgress(t *testing.T) {
	t.Run("sets counters", func(t *testing.T) {
		jm := NewJobManager("", nil)
		job := createTestJob(t, jm, "docs", false)
		jm.UpdateProgress(job.ID, 42, 100)

		got := jm.GetJob(job.ID)
		assert.Equal(t, int64(42), got.PagesProcessed)
		assert.Equal(t, int64(100), got.PagesQueued)
	})

	t.Run("nonexistent is no-op", func(t *testing.T) {
		jm := NewJobManager("", nil)
		// Should not panic
		jm.UpdateProgress("fake-id", 1, 2)
	})
}

func TestCancelJob(t *testing.T) {
	t.Run("running job cancelled", func(t *testing.T) {
		jm := NewJobManager("", nil)
		job := createTestJob(t, jm, "docs", false)
		jm.UpdateStatus(job.ID, JobStatusRunning, "")

		cancelled := jm.CancelJob(job.ID)
		assert.True(t, cancelled)

		got := jm.GetJob(job.ID)
		assert.Equal(t, JobStatusCancelled, got.Status)
		assert.False(t, got.CompletedAt.IsZero())

		// Context should be done
		ctx := jm.GetContext(job.ID)
		assert.Error(t, ctx.Err())
	})

	t.Run("completed job not cancellable", func(t *testing.T) {
		jm := NewJobManager("", nil)
		job := createTestJob(t, jm, "docs", false)
		jm.UpdateStatus(job.ID, JobStatusCompleted, "")

		cancelled := jm.CancelJob(job.ID)
		assert.False(t, cancelled)
	})

	t.Run("nonexistent returns false", func(t *testing.T) {
		jm := NewJobManager("", nil)
		assert.False(t, jm.CancelJob("nope"))
	})
}

func TestCancelAll(t *testing.T) {
	jm := NewJobManager("", nil)
	job1 := createTestJob(t, jm, "site-a", false)
	job2 := createTestJob(t, jm, "site-b", false)
	job3 := createTestJob(t, jm, "site-c", false)
	jm.UpdateStatus(job3.ID, JobStatusCompleted, "")

	jm.CancelAll()

	assert.Equal(t, JobStatusCancelled, jm.GetJob(job1.ID).Status)
	assert.Equal(t, JobStatusCancelled, jm.GetJob(job2.ID).Status)
	assert.Equal(t, JobStatusCompleted, jm.GetJob(job3.ID).Status) // completed stays completed
	assert.Equal(t, shutdownCancelledMsg, jm.GetJob(job1.ID).ErrorMessage, "a shutdown is not a user cancel")
	assert.Empty(t, jm.GetJob(job3.ID).ErrorMessage)

	// bysite cleared: new jobs allowed for cancelled sites
	newJob, err := jm.CreateJob("site-a", false)
	require.NoError(t, err)
	assert.NotEqual(t, job1.ID, newJob.ID)
}

func TestListJobs(t *testing.T) {
	jm := NewJobManager("", nil)
	job1 := createTestJob(t, jm, "a", false)
	job2 := createTestJob(t, jm, "b", false)
	job3 := createTestJob(t, jm, "c", false)

	jobs := jm.ListJobs()
	assert.Len(t, jobs, 3)

	// Order-independent: collect IDs into a set
	ids := make(map[string]bool)
	for _, j := range jobs {
		ids[j.ID] = true
	}
	assert.True(t, ids[job1.ID])
	assert.True(t, ids[job2.ID])
	assert.True(t, ids[job3.ID])
}

func TestGetContext(t *testing.T) {
	t.Run("valid job returns non-cancelled context", func(t *testing.T) {
		jm := NewJobManager("", nil)
		job := createTestJob(t, jm, "docs", false)
		ctx := jm.GetContext(job.ID)
		assert.NoError(t, ctx.Err())
	})

	t.Run("nonexistent returns background context", func(t *testing.T) {
		jm := NewJobManager("", nil)
		ctx := jm.GetContext("nope")
		// context.Background() never has an error
		require.NoError(t, ctx.Err())
		// Verify it's essentially background (not cancelled)
		assert.Equal(t, context.Background(), ctx)
	})
}

func TestPersistence_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	log := newTestLogger()

	jm1 := NewJobManager(filepath.Join(dir, jobsFilename), log)
	job1 := createTestJob(t, jm1, "docs", true)
	jm1.UpdateStatus(job1.ID, JobStatusCompleted, "")
	job2 := createTestJob(t, jm1, "other", false)
	jm1.UpdateStatus(job2.ID, JobStatusFailed, "bad thing")
	jm1.Stop()

	jm2 := NewJobManager(filepath.Join(dir, jobsFilename), log)
	defer jm2.Stop()

	got1 := jm2.GetJob(job1.ID)
	require.NotNil(t, got1)
	assert.Equal(t, JobStatusCompleted, got1.Status)
	assert.Equal(t, "docs", got1.SiteKey)
	assert.True(t, got1.Incremental)

	got2 := jm2.GetJob(job2.ID)
	require.NotNil(t, got2)
	assert.Equal(t, JobStatusFailed, got2.Status)
	assert.Equal(t, "bad thing", got2.ErrorMessage)
}

func TestPersistence_RestartFailsInFlightJobs(t *testing.T) {
	dir := t.TempDir()
	log := newTestLogger()

	jm1 := NewJobManager(filepath.Join(dir, jobsFilename), log)
	pending := createTestJob(t, jm1, "site-a", false)
	running := createTestJob(t, jm1, "site-b", false)
	jm1.UpdateStatus(running.ID, JobStatusRunning, "")
	jm1.UpdateProgress(running.ID, 7, 100)
	jm1.Stop()

	jm2 := NewJobManager(filepath.Join(dir, jobsFilename), log)
	defer jm2.Stop()

	gotPending := jm2.GetJob(pending.ID)
	require.NotNil(t, gotPending)
	assert.Equal(t, JobStatusFailed, gotPending.Status, "pending job should be failed after restart")
	assert.Equal(t, restartFailedMsg, gotPending.ErrorMessage)
	assert.False(t, gotPending.CompletedAt.IsZero())

	gotRunning := jm2.GetJob(running.ID)
	require.NotNil(t, gotRunning)
	assert.Equal(t, JobStatusFailed, gotRunning.Status, "running job should be failed after restart")
	assert.Equal(t, restartFailedMsg, gotRunning.ErrorMessage)
	assert.Equal(t, int64(7), gotRunning.PagesProcessed, "progress should survive restart")
	assert.Equal(t, int64(100), gotRunning.PagesQueued)

	// bysite must NOT be repopulated; the same site should accept a fresh job.
	fresh, err := jm2.CreateJob("site-a", false)
	require.NoError(t, err)
	assert.NotEqual(t, pending.ID, fresh.ID)
}

func TestPersistence_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	log := newTestLogger()

	jm := NewJobManager(filepath.Join(dir, jobsFilename), log)
	defer jm.Stop()

	createTestJob(t, jm, "docs", false)

	// The tmp file must never be left behind after a successful flush.
	_, err := os.Stat(filepath.Join(dir, jobsFilename+".tmp"))
	assert.True(t, os.IsNotExist(err), "tmp file should not exist after flush")

	// And the canonical file must exist with valid JSON.
	data, err := os.ReadFile(filepath.Join(dir, jobsFilename))
	require.NoError(t, err)
	var file jobsFile
	require.NoError(t, json.Unmarshal(data, &file))
	assert.Equal(t, jobsFileVersion, file.Version)
	assert.Len(t, file.Jobs, 1)
}

func TestPersistence_ProgressIsDebounced(t *testing.T) {
	dir := t.TempDir()
	log := newTestLogger()

	jm := NewJobManager(filepath.Join(dir, jobsFilename), log)
	job := createTestJob(t, jm, "docs", false)

	// CreateJob already wrote the file once. Capture mtime, then issue
	// many progress updates and confirm no immediate write.
	path := filepath.Join(dir, jobsFilename)
	st0, err := os.Stat(path)
	require.NoError(t, err)

	for i := range int64(50) {
		jm.UpdateProgress(job.ID, i, 100)
	}
	// No flush should have happened yet (interval is multi-second).
	st1, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, st0.ModTime(), st1.ModTime(), "progress updates should not trigger immediate flush")

	// Stop forces a final flush, which must capture the latest progress.
	jm.Stop()

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var file jobsFile
	require.NoError(t, json.Unmarshal(data, &file))
	require.Len(t, file.Jobs, 1)
	assert.Equal(t, int64(49), file.Jobs[0].PagesProcessed, "Stop should flush latest progress")
}

func TestPersistence_MissingFileIsFresh(t *testing.T) {
	dir := t.TempDir()
	log := newTestLogger()

	jm := NewJobManager(filepath.Join(dir, jobsFilename), log)
	defer jm.Stop()
	assert.Empty(t, jm.ListJobs())

	createTestJob(t, jm, "docs", false)
	assert.Len(t, jm.ListJobs(), 1)
}

func TestPersistence_DisabledOnEmptyStateDir(t *testing.T) {
	log := newTestLogger()
	jm := NewJobManager("", log)
	defer jm.Stop()

	// Should still work, just in-memory; no panics.
	job := createTestJob(t, jm, "docs", false)
	jm.UpdateProgress(job.ID, 5, 10)
	jm.UpdateStatus(job.ID, JobStatusCompleted, "")
	assert.Equal(t, JobStatusCompleted, jm.GetJob(job.ID).Status)
}

func TestPersistence_ConcurrentWritesSafe(t *testing.T) {
	dir := t.TempDir()
	log := newTestLogger()

	jm := NewJobManager(filepath.Join(dir, jobsFilename), log)
	defer jm.Stop()

	const workers = 8
	const perWorker = 25

	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := range perWorker {
				j, err := jm.CreateJob("site-"+strconv.Itoa(id*1000+i), false)
				if err == nil && j != nil {
					jm.UpdateProgress(j.ID, int64(i), int64(perWorker))
					jm.UpdateStatus(j.ID, JobStatusCompleted, "")
				}
			}
		}(w)
	}
	wg.Wait()

	// 200 completed jobs exceed the terminal-job retention cap, so exactly
	// maxTerminalJobs survive. The point of the test is that the concurrent
	// writes neither race nor corrupt the persisted file.
	require.Greater(t, workers*perWorker, maxTerminalJobs)
	assert.Len(t, jm.ListJobs(), maxTerminalJobs)

	// Force a flush and verify the file parses.
	jm.Stop()
	data, err := os.ReadFile(filepath.Join(dir, jobsFilename))
	require.NoError(t, err)
	var file jobsFile
	require.NoError(t, json.Unmarshal(data, &file))
	assert.Len(t, file.Jobs, maxTerminalJobs)
}

func TestPersistence_LoadIgnoresGarbage(t *testing.T) {
	dir := t.TempDir()
	log := newTestLogger()
	path := filepath.Join(dir, jobsFilename)
	require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o644))

	// Should not panic; should start with no jobs and overwrite on next flush.
	jm := NewJobManager(filepath.Join(dir, jobsFilename), log)
	defer jm.Stop()
	assert.Empty(t, jm.ListJobs())

	createTestJob(t, jm, "docs", false)
	jm.Stop()

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var file jobsFile
	require.NoError(t, json.Unmarshal(data, &file))
	assert.Len(t, file.Jobs, 1)
}

// forceTiedTimestamps simulates a clock too coarse to separate CreateJob calls,
// which is what Windows does and what Linux's nanosecond clock hides.
func forceTiedTimestamps(jobs []*Job, at time.Time) {
	for _, j := range jobs {
		j.StartedAt = at
		if !j.CompletedAt.IsZero() {
			j.CompletedAt = at
		}
	}
}

func TestListJobsOrderingIsTotalUnderTiedTimestamps(t *testing.T) {
	tied := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	var want []string

	for run := range 100 {
		m := NewJobManager("", nil)
		created := make([]*Job, 0, 4)
		for _, k := range []string{"alpha", "beta", "gamma", "delta"} {
			j, err := m.CreateJob(k, false)
			require.NoError(t, err, "CreateJob")
			created = append(created, j)
		}
		forceTiedTimestamps(created, tied)

		jobs := m.ListJobs()
		sortJobsNewestFirst(jobs)

		got := make([]string, len(jobs))
		for i, j := range jobs {
			got[i] = j.SiteKey
		}
		if run == 0 {
			want = got
			continue
		}
		require.Equal(t, want, got, "run %d ordering differs; comparator is not a total order", run)
	}

	// Creation order must survive, not merely be stable: newest first.
	assert.Equal(t, "delta", want[0], "tied timestamps lost creation order: got %v", want)
	assert.Equal(t, "alpha", want[3], "tied timestamps lost creation order: got %v", want)
}

// The pruner deletes jobs, so an unordered tie means deleting the wrong ones.
func TestPruneKeepsNewestUnderTiedTimestamps(t *testing.T) {
	tied := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)

	for range 50 {
		m := NewJobManager("", nil)
		all := make([]*Job, 0, maxTerminalJobs+5)
		for i := range maxTerminalJobs + 5 {
			j, err := m.CreateJob(fmt.Sprintf("site-%03d", i), false)
			require.NoError(t, err, "CreateJob")
			j.Status = JobStatusCompleted
			all = append(all, j)
		}
		forceTiedTimestamps(all, tied)

		m.mu.Lock()
		m.pruneTerminalLocked()
		m.mu.Unlock()

		kept := map[uint64]bool{}
		for _, j := range m.ListJobs() {
			kept[j.Seq] = true
		}
		require.Len(t, kept, maxTerminalJobs, "kept jobs")
		// The five lowest sequences are the oldest and must be the ones dropped.
		for _, j := range all[:5] {
			require.False(t, kept[j.Seq], "pruner kept an older job (seq %d) and dropped a newer one", j.Seq)
		}
	}
}

// Sequence numbering must continue above jobs already on disk, or a restart
// makes new jobs sort underneath old ones.
func TestSeqResumesAfterReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")

	m1 := NewJobManager(path, nil)
	var lastSeq uint64
	for i := range 3 {
		j, err := m1.CreateJob(fmt.Sprintf("site-%d", i), false)
		require.NoError(t, err, "CreateJob")
		j.Status = JobStatusCompleted
		lastSeq = j.Seq
	}
	m1.flush()
	m1.Stop()

	m2 := NewJobManager(path, nil)
	defer m2.Stop()
	j, err := m2.CreateJob("after-restart", false)
	require.NoError(t, err, "CreateJob after reload")
	assert.Greater(t, j.Seq, lastSeq, "seq must exceed the persisted max")
}

// Jobs persisted before Seq existed unmarshal to 0; ordering must stay total.
func TestLegacyJobsWithoutSeqStillOrderTotally(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	legacy := `{"version":1,"jobs":[
		{"id":"aaa","site_key":"a","status":"completed","started_at":"2026-08-30T12:00:00Z","completed_at":"2026-08-30T12:00:00Z"},
		{"id":"bbb","site_key":"b","status":"completed","started_at":"2026-08-30T12:00:00Z","completed_at":"2026-08-30T12:00:00Z"},
		{"id":"ccc","site_key":"c","status":"completed","started_at":"2026-08-30T12:00:00Z","completed_at":"2026-08-30T12:00:00Z"}]}`
	require.NoError(t, os.WriteFile(path, []byte(legacy), 0o644), "write legacy jobs")

	var want []string
	for run := range 50 {
		m := NewJobManager(path, nil)
		jobs := m.ListJobs()
		sortJobsNewestFirst(jobs)
		got := make([]string, len(jobs))
		for i, j := range jobs {
			got[i] = j.ID
		}
		m.Stop()
		if run == 0 {
			want = got
			continue
		}
		require.Equal(t, want, got, "legacy jobs ordering differs")
	}
	require.Len(t, want, 3, "legacy jobs")
}

// A cancelled crawl reports its final status after a new job has taken the
// site; that late report must not free the new job's slot.
func TestUpdateStatus_StaleJobKeepsNewerJobActive(t *testing.T) {
	jm := NewJobManager("", nil)
	old := createTestJob(t, jm, "docs", false)
	require.True(t, jm.CancelJob(old.ID))
	newer := createTestJob(t, jm, "docs", false)

	jm.UpdateStatus(old.ID, JobStatusCancelled, "")

	assert.True(t, jm.IsRunning("docs"))
	got := jm.GetJobBySite("docs")
	require.NotNil(t, got)
	assert.Equal(t, newer.ID, got.ID)
}

func startTestJob(t *testing.T, jm *JobManager, siteKey string, wait time.Duration) (*Job, bool) {
	t.Helper()
	job, created, err := jm.StartJob(context.Background(), siteKey, false, wait)
	require.NoError(t, err)
	require.NotNil(t, job)
	return job, created
}

func TestStartJob_ReturnsActiveJob(t *testing.T) {
	jm := NewJobManager("", nil)
	first, created := startTestJob(t, jm, "docs", time.Second)
	require.True(t, created)

	again, created := startTestJob(t, jm, "docs", time.Second)
	assert.False(t, created)
	assert.Equal(t, first.ID, again.ID)
}

// A cancelled crawl still holds the site's store until its goroutine exits, so
// the next job must wait for FinishJob instead of starting alongside it.
func TestStartJob_WaitsForCancelledCrawlToFinish(t *testing.T) {
	jm := NewJobManager("", nil)
	old, _ := startTestJob(t, jm, "docs", time.Second)
	require.True(t, jm.CancelJob(old.ID))

	type started struct {
		job     *Job
		created bool
	}
	result := make(chan started, 1)
	go func() {
		job, created, err := jm.StartJob(context.Background(), "docs", false, 10*time.Second)
		assert.NoError(t, err)
		result <- started{job, created}
	}()

	select {
	case <-result:
		t.Fatal("StartJob returned while the cancelled crawl was still running")
	case <-time.After(100 * time.Millisecond):
	}

	jm.FinishJob(old.ID)
	select {
	case r := <-result:
		assert.True(t, r.created)
		assert.NotEqual(t, old.ID, r.job.ID)
	case <-time.After(5 * time.Second):
		t.Fatal("StartJob did not proceed after FinishJob")
	}
}

func TestStartJob_GivesUpWhenCancelledCrawlOutlastsWait(t *testing.T) {
	jm := NewJobManager("", nil)
	old, _ := startTestJob(t, jm, "docs", time.Second)
	require.True(t, jm.CancelJob(old.ID))

	got, created := startTestJob(t, jm, "docs", 50*time.Millisecond)
	assert.False(t, created)
	assert.Equal(t, old.ID, got.ID)
	assert.Equal(t, JobStatusCancelled, got.Status)
}

func TestStartJob_HonoursContextWhileWaiting(t *testing.T) {
	jm := NewJobManager("", nil)
	old, _ := startTestJob(t, jm, "docs", time.Second)
	require.True(t, jm.CancelJob(old.ID))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := jm.StartJob(ctx, "docs", false, 10*time.Second)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestStartJob_ConcurrentCallsCreateOneJob(t *testing.T) {
	jm := NewJobManager("", nil)
	var wg sync.WaitGroup
	var mu sync.Mutex
	createdCount := 0
	ids := map[string]bool{}
	var errs []error
	for range 20 {
		wg.Go(func() {
			job, created, err := jm.StartJob(context.Background(), "docs", false, time.Second)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			ids[job.ID] = true
			if created {
				createdCount++
			}
		})
	}
	wg.Wait()
	require.Empty(t, errs)
	assert.Equal(t, 1, createdCount)
	assert.Len(t, ids, 1)
}

func TestFinishJob_LeavesNewerReservationAlone(t *testing.T) {
	jm := NewJobManager("", nil)
	old, _ := startTestJob(t, jm, "docs", time.Second)
	require.True(t, jm.CancelJob(old.ID))
	jm.FinishJob(old.ID)
	newer, created := startTestJob(t, jm, "docs", time.Second)
	require.True(t, created)
	require.True(t, jm.CancelJob(newer.ID))

	jm.FinishJob(old.ID)

	got, created := startTestJob(t, jm, "docs", 50*time.Millisecond)
	assert.False(t, created, "the newer crawl has not finished, so the site is still reserved")
	assert.Equal(t, newer.ID, got.ID)
}

func TestWaitIdle_GivesUpWhenContextEnds(t *testing.T) {
	jm := NewJobManager("", nil)
	startTestJob(t, jm, "docs", time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	assert.ErrorIs(t, jm.WaitIdle(ctx), context.DeadlineExceeded)
}

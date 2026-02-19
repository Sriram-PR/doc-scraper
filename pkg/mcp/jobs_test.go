package mcp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createTestJob(t *testing.T, jm *JobManager, siteKey string, incremental bool) *Job {
	t.Helper()
	job, err := jm.CreateJob(siteKey, incremental)
	require.NoError(t, err)
	require.NotNil(t, job)
	return job
}

func TestNewJobManager(t *testing.T) {
	jm := NewJobManager()
	require.NotNil(t, jm)
	assert.Empty(t, jm.ListJobs())
}

func TestCreateJob(t *testing.T) {
	t.Run("new job fields correct", func(t *testing.T) {
		jm := NewJobManager()
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
		jm := NewJobManager()
		job1 := createTestJob(t, jm, "docs", false)
		job2 := createTestJob(t, jm, "docs", false)
		assert.Equal(t, job1.ID, job2.ID)
	})

	t.Run("new job allowed after completion", func(t *testing.T) {
		jm := NewJobManager()
		job1 := createTestJob(t, jm, "docs", false)
		jm.UpdateStatus(job1.ID, JobStatusCompleted, "")

		job2 := createTestJob(t, jm, "docs", false)
		assert.NotEqual(t, job1.ID, job2.ID)
	})

	t.Run("different sites independent", func(t *testing.T) {
		jm := NewJobManager()
		job1 := createTestJob(t, jm, "site-a", false)
		job2 := createTestJob(t, jm, "site-b", false)
		assert.NotEqual(t, job1.ID, job2.ID)
	})
}

func TestGetJob(t *testing.T) {
	jm := NewJobManager()

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
	jm := NewJobManager()

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
	jm := NewJobManager()

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
		jm := NewJobManager()
		job := createTestJob(t, jm, "docs", false)
		jm.UpdateStatus(job.ID, JobStatusRunning, "")
		assert.Equal(t, JobStatusRunning, jm.GetJob(job.ID).Status)
	})

	t.Run("to completed sets CompletedAt and cleans bysite", func(t *testing.T) {
		jm := NewJobManager()
		job := createTestJob(t, jm, "docs", false)
		jm.UpdateStatus(job.ID, JobStatusCompleted, "")

		got := jm.GetJob(job.ID)
		assert.Equal(t, JobStatusCompleted, got.Status)
		assert.False(t, got.CompletedAt.IsZero())
		assert.Nil(t, jm.GetJobBySite("docs"))
	})

	t.Run("to failed sets ErrorMessage", func(t *testing.T) {
		jm := NewJobManager()
		job := createTestJob(t, jm, "docs", false)
		jm.UpdateStatus(job.ID, JobStatusFailed, "out of memory")

		got := jm.GetJob(job.ID)
		assert.Equal(t, JobStatusFailed, got.Status)
		assert.Equal(t, "out of memory", got.ErrorMessage)
		assert.False(t, got.CompletedAt.IsZero())
	})

	t.Run("nonexistent is no-op", func(t *testing.T) {
		jm := NewJobManager()
		// Should not panic
		jm.UpdateStatus("fake-id", JobStatusRunning, "")
	})
}

func TestUpdateProgress(t *testing.T) {
	t.Run("sets counters", func(t *testing.T) {
		jm := NewJobManager()
		job := createTestJob(t, jm, "docs", false)
		jm.UpdateProgress(job.ID, 42, 100)

		got := jm.GetJob(job.ID)
		assert.Equal(t, int64(42), got.PagesProcessed)
		assert.Equal(t, int64(100), got.PagesQueued)
	})

	t.Run("nonexistent is no-op", func(t *testing.T) {
		jm := NewJobManager()
		// Should not panic
		jm.UpdateProgress("fake-id", 1, 2)
	})
}

func TestCancelJob(t *testing.T) {
	t.Run("running job cancelled", func(t *testing.T) {
		jm := NewJobManager()
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
		jm := NewJobManager()
		job := createTestJob(t, jm, "docs", false)
		jm.UpdateStatus(job.ID, JobStatusCompleted, "")

		cancelled := jm.CancelJob(job.ID)
		assert.False(t, cancelled)
	})

	t.Run("nonexistent returns false", func(t *testing.T) {
		jm := NewJobManager()
		assert.False(t, jm.CancelJob("nope"))
	})
}

func TestCancelAll(t *testing.T) {
	jm := NewJobManager()
	job1 := createTestJob(t, jm, "site-a", false)
	job2 := createTestJob(t, jm, "site-b", false)
	job3 := createTestJob(t, jm, "site-c", false)
	jm.UpdateStatus(job3.ID, JobStatusCompleted, "")

	jm.CancelAll()

	assert.Equal(t, JobStatusCancelled, jm.GetJob(job1.ID).Status)
	assert.Equal(t, JobStatusCancelled, jm.GetJob(job2.ID).Status)
	assert.Equal(t, JobStatusCompleted, jm.GetJob(job3.ID).Status) // completed stays completed

	// bysite cleared: new jobs allowed for cancelled sites
	newJob, err := jm.CreateJob("site-a", false)
	require.NoError(t, err)
	assert.NotEqual(t, job1.ID, newJob.ID)
}

func TestListJobs(t *testing.T) {
	jm := NewJobManager()
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
		jm := NewJobManager()
		job := createTestJob(t, jm, "docs", false)
		ctx := jm.GetContext(job.ID)
		assert.NoError(t, ctx.Err())
	})

	t.Run("nonexistent returns background context", func(t *testing.T) {
		jm := NewJobManager()
		ctx := jm.GetContext("nope")
		// context.Background() never has an error
		require.NoError(t, ctx.Err())
		// Verify it's essentially background (not cancelled)
		assert.Equal(t, context.Background(), ctx)
	})
}

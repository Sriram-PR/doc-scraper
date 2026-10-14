package watch

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseInterval(t *testing.T) {
	tests := []struct {
		input    string
		expected time.Duration
		wantErr  bool
	}{
		{"30s", 30 * time.Second, false},
		{"5m", 5 * time.Minute, false},
		{"1h", time.Hour, false},
		{"24h", 24 * time.Hour, false},
		{"1d", 24 * time.Hour, false},
		{"7d", 7 * 24 * time.Hour, false},
		{"1d12h", 36 * time.Hour, false},
		{"2d6h", 54 * time.Hour, false},
		{"90m", 90 * time.Minute, false},
		{"+5d", 5 * 24 * time.Hour, false},
		{"1d+2h", 26 * time.Hour, false},
		{"1ns", time.Nanosecond, false},
		{"106751d", 106751 * 24 * time.Hour, false},
		{"invalid", 0, true},
		{"", 0, true},
		{"d", 0, true},
		{"5x", 0, true},
		{"1.5d", 0, true},
		{"1D", 0, true},
		{"7dabc", 0, true},
		{"0", 0, true},
		{"0s", 0, true},
		{"0d", 0, true},
		{"-5m", 0, true},
		{"-1d", 0, true},
		{"1d-2h", 0, true},
		{"106752d", 0, true},
		{"106751d24h", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseInterval(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestStateManager(t *testing.T) {
	// Create temp directory for state file
	tmpDir := t.TempDir()

	sm := NewStateManager(tmpDir)

	// Test initial state
	require.NoError(t, sm.Load())

	// Test ShouldRun for new site
	assert.True(t, sm.ShouldRun("test_site", time.Hour), "ShouldRun() should return true for new site")

	// Test UpdateSiteState
	sm.UpdateSiteState("test_site", true, PageCounts{Saved: 100, Unchanged: 40, Failed: 3}, "")

	// Test ShouldRun after update (should not run immediately)
	assert.False(t, sm.ShouldRun("test_site", time.Hour), "ShouldRun() should return false immediately after run")

	// Test GetSiteState
	state, ok := sm.GetSiteState("test_site")
	assert.True(t, ok, "GetSiteState() should return true for existing site")
	assert.True(t, state.LastRunSuccess)
	assert.Equal(t, int64(100), state.PagesSaved)
	assert.Equal(t, int64(40), state.PagesUnchanged)
	assert.Equal(t, int64(3), state.PagesFailed)

	// Test Save
	require.NoError(t, sm.Save())

	// Verify state file exists
	assert.FileExists(t, filepath.Join(tmpDir, stateFileName))

	// Test Load from saved state
	sm2 := NewStateManager(tmpDir)
	require.NoError(t, sm2.Load())

	state2, ok := sm2.GetSiteState("test_site")
	assert.True(t, ok, "GetSiteState() should return true after Load()")
	assert.Equal(t, int64(100), state2.PagesSaved)
	assert.Equal(t, int64(40), state2.PagesUnchanged)
	assert.Equal(t, int64(3), state2.PagesFailed)
}

func TestStateManagerGetNextRunTime(t *testing.T) {
	tmpDir := t.TempDir()
	sm := NewStateManager(tmpDir)
	_ = sm.Load()

	interval := time.Hour

	// New site should return now
	nextRun := sm.GetNextRunTime("new_site", interval)
	assert.LessOrEqual(t, time.Since(nextRun), time.Second, "GetNextRunTime() for new site should be approximately now")

	// Update site and check next run
	sm.UpdateSiteState("existing_site", true, PageCounts{Saved: 100}, "")
	state, _ := sm.GetSiteState("existing_site")

	expectedNextRun := state.LastRunTime.Add(interval)
	actualNextRun := sm.GetNextRunTime("existing_site", interval)

	assert.LessOrEqual(t, actualNextRun.Sub(expectedNextRun), time.Millisecond)
}

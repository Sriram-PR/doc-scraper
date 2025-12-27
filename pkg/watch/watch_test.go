package watch

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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
		{"invalid", 0, true},
		{"", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseInterval(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ParseInterval(%q) expected error, got nil", tt.input)
				}
				return
			}
			if err != nil {
				t.Errorf("ParseInterval(%q) unexpected error: %v", tt.input, err)
				return
			}
			if got != tt.expected {
				t.Errorf("ParseInterval(%q) = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

func TestFormatInterval(t *testing.T) {
	tests := []struct {
		input    time.Duration
		expected string
	}{
		{30 * time.Second, "30s"},
		{5 * time.Minute, "5m"},
		{time.Hour, "1h"},
		{90 * time.Minute, "1h30m"},
		{24 * time.Hour, "1d"},
		{36 * time.Hour, "1d12h"},
		{7 * 24 * time.Hour, "7d"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			got := FormatInterval(tt.input)
			if got != tt.expected {
				t.Errorf("FormatInterval(%v) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestStateManager(t *testing.T) {
	// Create temp directory for state file
	tmpDir := t.TempDir()

	sm := NewStateManager(tmpDir)

	// Test initial state
	if err := sm.Load(); err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	// Test ShouldRun for new site
	if !sm.ShouldRun("test_site", time.Hour) {
		t.Error("ShouldRun() should return true for new site")
	}

	// Test UpdateSiteState
	sm.UpdateSiteState("test_site", true, 100, "")

	// Test ShouldRun after update (should not run immediately)
	if sm.ShouldRun("test_site", time.Hour) {
		t.Error("ShouldRun() should return false immediately after run")
	}

	// Test GetSiteState
	state, ok := sm.GetSiteState("test_site")
	if !ok {
		t.Error("GetSiteState() should return true for existing site")
	}
	if !state.LastRunSuccess {
		t.Error("LastRunSuccess should be true")
	}
	if state.PagesProcessed != 100 {
		t.Errorf("PagesProcessed = %d, want 100", state.PagesProcessed)
	}

	// Test Save
	if err := sm.Save(); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}

	// Verify state file exists
	statePath := filepath.Join(tmpDir, stateFileName)
	if _, err := os.Stat(statePath); os.IsNotExist(err) {
		t.Error("State file should exist after Save()")
	}

	// Test Load from saved state
	sm2 := NewStateManager(tmpDir)
	if err := sm2.Load(); err != nil {
		t.Fatalf("Load() from saved state failed: %v", err)
	}

	state2, ok := sm2.GetSiteState("test_site")
	if !ok {
		t.Error("GetSiteState() should return true after Load()")
	}
	if state2.PagesProcessed != 100 {
		t.Errorf("Loaded PagesProcessed = %d, want 100", state2.PagesProcessed)
	}
}

func TestStateManagerGetAllSiteStates(t *testing.T) {
	tmpDir := t.TempDir()
	sm := NewStateManager(tmpDir)
	_ = sm.Load()

	sm.UpdateSiteState("site1", true, 50, "")
	sm.UpdateSiteState("site2", false, 0, "some error")
	sm.UpdateSiteState("site3", true, 200, "")

	states := sm.GetAllSiteStates()

	if len(states) != 3 {
		t.Errorf("GetAllSiteStates() returned %d states, want 3", len(states))
	}

	if states["site1"].PagesProcessed != 50 {
		t.Errorf("site1 PagesProcessed = %d, want 50", states["site1"].PagesProcessed)
	}

	if states["site2"].LastRunSuccess {
		t.Error("site2 LastRunSuccess should be false")
	}

	if states["site2"].ErrorMessage != "some error" {
		t.Errorf("site2 ErrorMessage = %q, want 'some error'", states["site2"].ErrorMessage)
	}
}

func TestStateManagerGetNextRunTime(t *testing.T) {
	tmpDir := t.TempDir()
	sm := NewStateManager(tmpDir)
	_ = sm.Load()

	interval := time.Hour

	// New site should return now
	nextRun := sm.GetNextRunTime("new_site", interval)
	if time.Since(nextRun) > time.Second {
		t.Error("GetNextRunTime() for new site should be approximately now")
	}

	// Update site and check next run
	sm.UpdateSiteState("existing_site", true, 100, "")
	state, _ := sm.GetSiteState("existing_site")

	expectedNextRun := state.LastRunTime.Add(interval)
	actualNextRun := sm.GetNextRunTime("existing_site", interval)

	if actualNextRun.Sub(expectedNextRun) > time.Millisecond {
		t.Errorf("GetNextRunTime() = %v, want %v", actualNextRun, expectedNextRun)
	}
}

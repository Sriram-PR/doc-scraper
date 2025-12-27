package watch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const stateFileName = "watch_state.json"

// SiteState contains the last run information for a site
type SiteState struct {
	LastRunTime    time.Time `json:"last_run_time"`
	LastRunSuccess bool      `json:"last_run_success"`
	PagesProcessed int64     `json:"pages_processed"`
	ErrorMessage   string    `json:"error_message,omitempty"`
}

// WatchState contains the persistent state for the watch scheduler
type WatchState struct {
	Sites     map[string]SiteState `json:"sites"`
	UpdatedAt time.Time            `json:"updated_at"`
}

// StateManager handles persisting and loading watch state
type StateManager struct {
	stateDir  string
	statePath string
	state     WatchState
	mu        sync.RWMutex
}

// NewStateManager creates a new state manager
func NewStateManager(stateDir string) *StateManager {
	return &StateManager{
		stateDir:  stateDir,
		statePath: filepath.Join(stateDir, stateFileName),
		state: WatchState{
			Sites: make(map[string]SiteState),
		},
	}
}

// Load loads the state from disk
func (m *StateManager) Load() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, err := os.ReadFile(m.statePath)
	if err != nil {
		if os.IsNotExist(err) {
			// No state file yet, start fresh
			m.state = WatchState{
				Sites: make(map[string]SiteState),
			}
			return nil
		}
		return fmt.Errorf("failed to read state file: %w", err)
	}

	if err := json.Unmarshal(data, &m.state); err != nil {
		return fmt.Errorf("failed to parse state file: %w", err)
	}

	if m.state.Sites == nil {
		m.state.Sites = make(map[string]SiteState)
	}

	return nil
}

// Save saves the state to disk
func (m *StateManager) Save() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.state.UpdatedAt = time.Now()

	// Ensure state directory exists
	if err := os.MkdirAll(m.stateDir, 0755); err != nil {
		return fmt.Errorf("failed to create state directory: %w", err)
	}

	data, err := json.MarshalIndent(m.state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}

	if err := os.WriteFile(m.statePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write state file: %w", err)
	}

	return nil
}

// GetSiteState returns the state for a specific site
func (m *StateManager) GetSiteState(siteKey string) (SiteState, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	state, ok := m.state.Sites[siteKey]
	return state, ok
}

// UpdateSiteState updates the state for a specific site
func (m *StateManager) UpdateSiteState(siteKey string, success bool, pagesProcessed int64, errorMsg string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.state.Sites[siteKey] = SiteState{
		LastRunTime:    time.Now(),
		LastRunSuccess: success,
		PagesProcessed: pagesProcessed,
		ErrorMessage:   errorMsg,
	}
}

// ShouldRun checks if a site should run based on the interval
func (m *StateManager) ShouldRun(siteKey string, interval time.Duration) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	state, ok := m.state.Sites[siteKey]
	if !ok {
		// Never run before, should run now
		return true
	}

	// Check if enough time has passed since last run
	return time.Since(state.LastRunTime) >= interval
}

// GetNextRunTime returns when the site should next run
func (m *StateManager) GetNextRunTime(siteKey string, interval time.Duration) time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()

	state, ok := m.state.Sites[siteKey]
	if !ok {
		return time.Now()
	}

	return state.LastRunTime.Add(interval)
}

// GetAllSiteStates returns all site states
func (m *StateManager) GetAllSiteStates() map[string]SiteState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Return a copy
	result := make(map[string]SiteState, len(m.state.Sites))
	for k, v := range m.state.Sites {
		result[k] = v
	}
	return result
}

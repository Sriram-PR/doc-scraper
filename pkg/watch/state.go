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

// SiteState records the last run result for a site.
type SiteState struct {
	LastRunTime    time.Time `json:"last_run_time"`
	LastRunSuccess bool      `json:"last_run_success"`
	PagesProcessed int64     `json:"pages_processed"`
	ErrorMessage   string    `json:"error_message,omitempty"`
}

// WatchState is the persistent state for the watch scheduler.
type WatchState struct {
	Sites     map[string]SiteState `json:"sites"`
	UpdatedAt time.Time            `json:"updated_at"`
}

// StateManager persists and loads watch state.
type StateManager struct {
	stateDir  string
	statePath string
	state     WatchState
	mu        sync.RWMutex
}

func NewStateManager(stateDir string) *StateManager {
	return &StateManager{
		stateDir:  stateDir,
		statePath: filepath.Join(stateDir, stateFileName),
		state: WatchState{
			Sites: make(map[string]SiteState),
		},
	}
}

// Load reads persisted state from disk.
func (m *StateManager) Load() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, err := os.ReadFile(m.statePath)
	if err != nil {
		if os.IsNotExist(err) {
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

// Save persists state to disk.
func (m *StateManager) Save() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.state.UpdatedAt = time.Now()

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

// GetSiteState returns the persisted state for a site.
func (m *StateManager) GetSiteState(siteKey string) (SiteState, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	state, ok := m.state.Sites[siteKey]
	return state, ok
}

// UpdateSiteState records the result of a crawl run for a site.
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

// ShouldRun reports whether enough time has elapsed since the last run.
func (m *StateManager) ShouldRun(siteKey string, interval time.Duration) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	state, ok := m.state.Sites[siteKey]
	if !ok {
		return true // never run before
	}
	return time.Since(state.LastRunTime) >= interval
}

// GetNextRunTime returns when the site should next run.
func (m *StateManager) GetNextRunTime(siteKey string, interval time.Duration) time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()

	state, ok := m.state.Sites[siteKey]
	if !ok {
		return time.Now()
	}

	return state.LastRunTime.Add(interval)
}

// GetAllSiteStates returns a snapshot copy of all site states.
func (m *StateManager) GetAllSiteStates() map[string]SiteState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]SiteState, len(m.state.Sites))
	for k, v := range m.state.Sites {
		result[k] = v
	}
	return result
}

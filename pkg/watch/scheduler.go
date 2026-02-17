package watch

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/Sriram-PR/doc-scraper/pkg/config"
	"github.com/Sriram-PR/doc-scraper/pkg/orchestrate"
)

// Scheduler manages periodic crawling of sites
type Scheduler struct {
	appCfg       *config.AppConfig
	siteKeys     []string
	interval     time.Duration
	log          *logrus.Entry
	stateManager *StateManager

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewScheduler creates a new watch scheduler
func NewScheduler(appCfg *config.AppConfig, siteKeys []string, interval time.Duration, log *logrus.Entry) *Scheduler {
	ctx, cancel := context.WithCancel(context.Background())

	return &Scheduler{
		appCfg:       appCfg,
		siteKeys:     siteKeys,
		interval:     interval,
		log:          log,
		stateManager: NewStateManager(appCfg.StateDir),
		ctx:          ctx,
		cancel:       cancel,
	}
}

// Run starts the watch scheduler and blocks until stopped
func (s *Scheduler) Run() error {
	// Load existing state
	if err := s.stateManager.Load(); err != nil {
		s.log.Warnf("Failed to load watch state: %v (starting fresh)", err)
	}

	s.log.Infof("Starting watch mode for %d sites with interval %v", len(s.siteKeys), s.interval)
	s.logSchedule()

	// Run initial crawl for sites that need it
	s.runDueSites()

	// Start the ticker for periodic checks
	ticker := time.NewTicker(s.calculateTickInterval())
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			s.log.Info("Watch scheduler shutting down...")
			s.wg.Wait()
			return nil
		case <-ticker.C:
			s.runDueSites()
		}
	}
}

// Stop stops the watch scheduler
func (s *Scheduler) Stop() {
	s.log.Info("Stopping watch scheduler...")
	s.cancel()
}

// runDueSites runs all sites that are due for a crawl
func (s *Scheduler) runDueSites() {
	dueSites := s.getDueSites()
	if len(dueSites) == 0 {
		s.logNextRun()
		return
	}

	s.log.Infof("Running crawl for %d due sites: %v", len(dueSites), dueSites)

	// Use the orchestrator for parallel crawling
	orch := orchestrate.NewOrchestrator(s.appCfg, dueSites, false, s.log)

	// Run in a goroutine so we can handle shutdown
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()

		results := orch.Run()

		// Update state for each site
		for _, result := range results {
			errorMsg := ""
			if result.Error != nil {
				errorMsg = result.Error.Error()
			}
			s.stateManager.UpdateSiteState(result.SiteKey, result.Success, result.PagesProcessed, errorMsg)
		}

		// Save state
		if err := s.stateManager.Save(); err != nil {
			s.log.Errorf("Failed to save watch state: %v", err)
		}

		s.logNextRun()
	}()
}

// getDueSites returns sites that are due for a crawl
func (s *Scheduler) getDueSites() []string {
	var due []string
	for _, siteKey := range s.siteKeys {
		if s.stateManager.ShouldRun(siteKey, s.interval) {
			due = append(due, siteKey)
		}
	}
	return due
}

// calculateTickInterval returns how often to check for due sites
func (s *Scheduler) calculateTickInterval() time.Duration {
	// Check at least every minute, or every 1/10th of the interval
	checkInterval := s.interval / 10
	if checkInterval < time.Minute {
		checkInterval = time.Minute
	}
	if checkInterval > 10*time.Minute {
		checkInterval = 10 * time.Minute
	}
	return checkInterval
}

// logSchedule logs the current schedule
func (s *Scheduler) logSchedule() {
	s.log.Info("Watch schedule:")
	for _, siteKey := range s.siteKeys {
		state, exists := s.stateManager.GetSiteState(siteKey)
		if exists {
			nextRun := s.stateManager.GetNextRunTime(siteKey, s.interval)
			status := "success"
			if !state.LastRunSuccess {
				status = "failed"
			}
			s.log.Infof("  %s: last run %v (%s, %d pages), next run %v",
				siteKey,
				state.LastRunTime.Format(time.RFC3339),
				status,
				state.PagesProcessed,
				nextRun.Format(time.RFC3339))
		} else {
			s.log.Infof("  %s: never run, will run immediately", siteKey)
		}
	}
}

// logNextRun logs when the next run will occur
func (s *Scheduler) logNextRun() {
	var nextRuns []struct {
		site string
		time time.Time
	}

	for _, siteKey := range s.siteKeys {
		nextRun := s.stateManager.GetNextRunTime(siteKey, s.interval)
		nextRuns = append(nextRuns, struct {
			site string
			time time.Time
		}{siteKey, nextRun})
	}

	// Sort by next run time
	sort.Slice(nextRuns, func(i, j int) bool {
		return nextRuns[i].time.Before(nextRuns[j].time)
	})

	if len(nextRuns) > 0 {
		next := nextRuns[0]
		until := time.Until(next.time)
		if until < 0 {
			until = 0
		}
		s.log.Infof("Next crawl: %s in %v (at %s)", next.site, until.Round(time.Second), next.time.Format("15:04:05"))
	}
}

// GetStatus returns the current status of all watched sites
func (s *Scheduler) GetStatus() map[string]SiteStatus {
	status := make(map[string]SiteStatus)

	for _, siteKey := range s.siteKeys {
		state, exists := s.stateManager.GetSiteState(siteKey)
		nextRun := s.stateManager.GetNextRunTime(siteKey, s.interval)

		status[siteKey] = SiteStatus{
			SiteKey:        siteKey,
			LastRunTime:    state.LastRunTime,
			LastRunSuccess: state.LastRunSuccess,
			PagesProcessed: state.PagesProcessed,
			ErrorMessage:   state.ErrorMessage,
			NextRunTime:    nextRun,
			NeverRun:       !exists,
		}
	}

	return status
}

// SiteStatus contains the status of a watched site
type SiteStatus struct {
	SiteKey        string
	LastRunTime    time.Time
	LastRunSuccess bool
	PagesProcessed int64
	ErrorMessage   string
	NextRunTime    time.Time
	NeverRun       bool
}

// FormatInterval formats a duration for display
func FormatInterval(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		hours := int(d.Hours())
		mins := int(d.Minutes()) % 60
		if mins > 0 {
			return fmt.Sprintf("%dh%dm", hours, mins)
		}
		return fmt.Sprintf("%dh", hours)
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	if hours > 0 {
		return fmt.Sprintf("%dd%dh", days, hours)
	}
	return fmt.Sprintf("%dd", days)
}

// ParseInterval parses a duration string with support for days
func ParseInterval(s string) (time.Duration, error) {
	// Try standard parsing first
	d, err := time.ParseDuration(s)
	if err == nil {
		return d, nil
	}

	// Check for day suffix
	var days int
	var remaining string
	n, _ := fmt.Sscanf(s, "%dd%s", &days, &remaining)
	if n >= 1 {
		d = time.Duration(days) * 24 * time.Hour
		if remaining != "" {
			extra, err := time.ParseDuration(remaining)
			if err != nil {
				return 0, fmt.Errorf("invalid interval format: %s", s)
			}
			d += extra
		}
		return d, nil
	}

	return 0, fmt.Errorf("invalid interval format: %s (examples: 30m, 1h, 24h, 7d)", s)
}

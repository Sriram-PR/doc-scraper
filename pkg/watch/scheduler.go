package watch

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"log/slog"

	"github.com/Sriram-PR/doc-scraper/pkg/config"
	"github.com/Sriram-PR/doc-scraper/pkg/orchestrate"
	"github.com/Sriram-PR/doc-scraper/pkg/storage/index"
)

// Scheduler manages periodic crawling of sites.
type Scheduler struct {
	appCfg       *config.AppConfig
	siteKeys     []string
	interval     time.Duration
	log          *slog.Logger
	stateManager *StateManager

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	idx    *index.Index
}

// WithIndex attaches a crawl-history index passed to every orchestrator the
// scheduler spawns. nil is safe and disables history capture.
func (s *Scheduler) WithIndex(idx *index.Index) *Scheduler {
	s.idx = idx
	return s
}

func NewScheduler(appCfg *config.AppConfig, siteKeys []string, interval time.Duration, log *slog.Logger) *Scheduler {
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

// Run starts the watch scheduler and blocks until stopped.
func (s *Scheduler) Run() error {
	if err := s.stateManager.Load(); err != nil {
		s.log.Warn(fmt.Sprintf("Failed to load watch state: %v (starting fresh)", err))
	}

	s.log.Info(fmt.Sprintf("Starting watch mode for %d sites with interval %v", len(s.siteKeys), s.interval))
	s.logSchedule()

	s.runDueSites()

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

func (s *Scheduler) Stop() {
	s.log.Info("Stopping watch scheduler...")
	s.cancel()
}

func (s *Scheduler) runDueSites() {
	dueSites := s.getDueSites()
	if len(dueSites) == 0 {
		s.logNextRun()
		return
	}

	s.log.Info(fmt.Sprintf("Running crawl for %d due sites: %v", len(dueSites), dueSites))

	// Watch is incremental by construction; pass resume=true so scheduled re-crawls
	// reuse the persisted visited DB instead of wiping it.
	orch := orchestrate.NewOrchestrator(s.ctx, s.appCfg, dueSites, true, s.log).WithIndex(s.idx)

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()

		results := orch.Run()

		for _, result := range results {
			errorMsg := ""
			if result.Error != nil {
				errorMsg = result.Error.Error()
			}
			s.stateManager.UpdateSiteState(result.SiteKey, result.Success, result.PagesProcessed, errorMsg)
		}

		if err := s.stateManager.Save(); err != nil {
			s.log.Error(fmt.Sprintf("Failed to save watch state: %v", err))
		}

		s.logNextRun()
	}()
}

func (s *Scheduler) getDueSites() []string {
	var due []string
	for _, siteKey := range s.siteKeys {
		if s.stateManager.ShouldRun(siteKey, s.interval) {
			due = append(due, siteKey)
		}
	}
	return due
}

// calculateTickInterval returns the polling interval (1/10th of crawl interval, clamped to 1-10 min).
func (s *Scheduler) calculateTickInterval() time.Duration {
	checkInterval := s.interval / 10
	if checkInterval < time.Minute {
		checkInterval = time.Minute
	}
	if checkInterval > 10*time.Minute {
		checkInterval = 10 * time.Minute
	}
	return checkInterval
}

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
			s.log.Info(fmt.Sprintf("  %s: last run %v (%s, %d pages), next run %v",
				siteKey,
				state.LastRunTime.Format(time.RFC3339),
				status,
				state.PagesProcessed,
				nextRun.Format(time.RFC3339)))
		} else {
			s.log.Info(fmt.Sprintf("  %s: never run, will run immediately", siteKey))
		}
	}
}

func (s *Scheduler) logNextRun() {
	nextRuns := make([]struct {
		site string
		time time.Time
	}, 0, len(s.siteKeys))

	for _, siteKey := range s.siteKeys {
		nextRun := s.stateManager.GetNextRunTime(siteKey, s.interval)
		nextRuns = append(nextRuns, struct {
			site string
			time time.Time
		}{siteKey, nextRun})
	}

	sort.Slice(nextRuns, func(i, j int) bool {
		return nextRuns[i].time.Before(nextRuns[j].time)
	})

	if len(nextRuns) > 0 {
		next := nextRuns[0]
		until := time.Until(next.time)
		if until < 0 {
			until = 0
		}
		s.log.Info(fmt.Sprintf("Next crawl: %s in %v (at %s)", next.site, until.Round(time.Second), next.time.Format("15:04:05")))
	}
}

// GetStatus returns the current status of all watched sites.
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

// SiteStatus contains the status of a watched site.
type SiteStatus struct {
	SiteKey        string
	LastRunTime    time.Time
	LastRunSuccess bool
	PagesProcessed int64
	ErrorMessage   string
	NextRunTime    time.Time
	NeverRun       bool
}

// FormatInterval formats a duration as a compact human-readable string.
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

// ParseInterval parses a duration string with added support for day suffixes (e.g. "7d", "1d12h").
func ParseInterval(s string) (time.Duration, error) {
	d, err := time.ParseDuration(s)
	if err == nil {
		return d, nil
	}

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

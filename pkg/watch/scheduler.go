package watch

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"time"

	"log/slog"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/orchestrate"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/storage/index"
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

// ParseInterval parses a positive duration: a Go duration ("30m", "24h") or
// whole days with an optional Go-duration remainder ("7d", "1d12h").
func ParseInterval(s string) (time.Duration, error) {
	d, ok := parseIntervalValue(s)
	if !ok {
		return 0, fmt.Errorf("invalid interval format: %s (examples: 30m, 1h, 24h, 7d)", s)
	}
	if d <= 0 {
		return 0, fmt.Errorf("interval must be positive, got %s", s)
	}
	return d, nil
}

var dayIntervalRe = regexp.MustCompile(`^\+?([0-9]+)d(.*)$`)

func parseIntervalValue(s string) (time.Duration, bool) {
	if d, err := time.ParseDuration(s); err == nil {
		return d, true
	}
	m := dayIntervalRe.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	const day = 24 * time.Hour
	days, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil || days > int64(math.MaxInt64/day) {
		return 0, false
	}
	d := time.Duration(days) * day
	if m[2] == "" {
		return d, true
	}
	extra, err := time.ParseDuration(m[2])
	if err != nil || extra < 0 || extra > math.MaxInt64-d {
		return 0, false
	}
	return d + extra, true
}

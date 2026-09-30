package crawler

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"time"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/storage"
)

const backupSuffix = ".old"

// Stage is where one site's crawl writes its output dir and visited DB. A fresh
// crawl (or the continuation of an interrupted one) builds in staging siblings
// of the live paths and Commit swaps them in; otherwise it works in place.
type Stage struct {
	Staged bool
	log    *slog.Logger

	liveOut, stageOut string
	liveDB, stageDB   string
}

// PlanStage decides where a crawl runs. A fresh crawl discards any stale
// staging and starts a new one; a resumed crawl continues staging if present
// and otherwise runs in place.
func PlanStage(appCfg *config.AppConfig, siteKey string, resume bool, log *slog.Logger) (*Stage, error) {
	s := &Stage{
		log:      log,
		liveOut:  appCfg.SiteOutputDir(siteKey),
		stageOut: appCfg.SiteStagingOutputDir(siteKey),
		liveDB:   storage.VisitedDBPath(appCfg.StateDir, siteKey),
		stageDB:  storage.StagingVisitedDBPath(appCfg.StateDir, siteKey),
	}
	s.recoverSwap()
	if !resume {
		if err := s.Discard(); err != nil {
			return nil, err
		}
		s.Staged = true
		return s, nil
	}
	out, db := pathExists(s.stageOut), pathExists(s.stageDB)
	if out != db {
		// Half a staging pair (a crash mid-Commit) would resume into an empty
		// output dir and could later swap it over the live corpus.
		log.Warn("Discarding incomplete staging", "output", s.stageOut, "visited_db", s.stageDB)
		if err := s.Discard(); err != nil {
			return nil, err
		}
	}
	s.Staged = out && db
	return s, nil
}

// OpenStagedStore plans the stage for a crawl and opens the visited DB it
// should use.
func OpenStagedStore(ctx context.Context, appCfg *config.AppConfig, siteKey string, resume bool, log *slog.Logger) (*Stage, *storage.BadgerStore, error) {
	stage, err := PlanStage(appCfg, siteKey, resume, log)
	if err != nil {
		return nil, nil, fmt.Errorf("preparing staging for '%s': %w", siteKey, err)
	}
	store, err := storage.NewBadgerStoreAt(ctx, stage.DBPath(), resume, log)
	if err != nil {
		return nil, nil, err
	}
	return stage, store, nil
}

func (s *Stage) OutputDir() string {
	if s.Staged {
		return s.stageOut
	}
	return s.liveOut
}

func (s *Stage) DBPath() string {
	if s.Staged {
		return s.stageDB
	}
	return s.liveDB
}

// Discard removes any staging output and visited DB.
func (s *Stage) Discard() error {
	for _, p := range []string{s.stageOut, s.stageDB} {
		if err := os.RemoveAll(p); err != nil {
			return fmt.Errorf("removing stale staging %s: %w", p, err)
		}
	}
	return nil
}

// Commit replaces the live output dir and visited DB with the staged ones. The
// caller must have closed every handle inside them. On failure the previous
// live state is restored and the staging is left for a retry.
func (s *Stage) Commit() error {
	type pair struct{ live, staging, backup string }
	var pairs []pair
	for _, p := range []pair{
		{s.liveOut, s.stageOut, s.liveOut + backupSuffix},
		{s.liveDB, s.stageDB, s.liveDB + backupSuffix},
	} {
		if pathExists(p.staging) {
			pairs = append(pairs, p)
		}
	}

	for _, p := range pairs {
		if err := os.RemoveAll(p.backup); err != nil {
			return fmt.Errorf("clearing %s: %w", p.backup, err)
		}
	}

	var movedAside, promoted []pair
	rollback := func() {
		for i := len(promoted) - 1; i >= 0; i-- {
			if err := renameRetry(promoted[i].live, promoted[i].staging); err != nil {
				s.log.Error("Staging rollback failed", "path", promoted[i].live, "err", err)
			}
		}
		for i := len(movedAside) - 1; i >= 0; i-- {
			if err := renameRetry(movedAside[i].backup, movedAside[i].live); err != nil {
				s.log.Error("Backup restore failed", "path", movedAside[i].live, "err", err)
			}
		}
	}

	for _, p := range pairs {
		if pathExists(p.live) {
			if err := renameRetry(p.live, p.backup); err != nil {
				rollback()
				return fmt.Errorf("moving %s aside: %w", p.live, err)
			}
			movedAside = append(movedAside, p)
		}
		if err := renameRetry(p.staging, p.live); err != nil {
			rollback()
			return fmt.Errorf("promoting %s: %w", p.staging, err)
		}
		promoted = append(promoted, p)
	}

	for _, p := range pairs {
		if err := os.RemoveAll(p.backup); err != nil {
			s.log.Warn("Could not remove previous corpus backup", "path", p.backup, "err", err)
		}
	}
	s.Staged = false
	return nil
}

// recoverSwap restores the live path from its backup when a previous Commit
// died between moving the live path aside and promoting the staging.
func (s *Stage) recoverSwap() {
	for _, live := range []string{s.liveOut, s.liveDB} {
		backup := live + backupSuffix
		if !pathExists(backup) {
			continue
		}
		if pathExists(live) {
			_ = os.RemoveAll(backup)
			continue
		}
		if err := renameRetry(backup, live); err != nil {
			s.log.Error("Could not restore interrupted swap", "path", live, "err", err)
		}
	}
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return !errors.Is(err, fs.ErrNotExist)
}

// renameRetry absorbs transient Windows sharing violations (scanners and
// indexers briefly holding a just-closed file).
func renameRetry(from, to string) error {
	var err error
	for i := range 5 {
		if err = os.Rename(from, to); err == nil {
			return nil
		}
		time.Sleep(time.Duration(i+1) * 50 * time.Millisecond)
	}
	return err
}

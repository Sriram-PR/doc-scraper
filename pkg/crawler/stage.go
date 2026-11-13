package crawler

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/storage"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/utils"
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
	journal           string

	pending *CommitRecord

	// Test hook: Commit stops with errSimulatedCrash after this many steps.
	crashAfter int
	steps      int
}

var errSimulatedCrash = errors.New("simulated crash")

type swapPair struct{ live, staging string }

func (p swapPair) backup() string { return p.live + backupSuffix }

// PlanStage decides where a crawl runs. It first finishes a commit a crash
// interrupted. A fresh crawl then discards any stale staging and starts a new
// one; a resumed crawl continues staging if present and otherwise runs in place.
func PlanStage(appCfg *config.AppConfig, siteKey string, resume bool, log *slog.Logger) (*Stage, error) {
	s := &Stage{
		log:      log,
		liveOut:  appCfg.SiteOutputDir(siteKey),
		stageOut: appCfg.SiteStagingOutputDir(siteKey),
		liveDB:   storage.VisitedDBPath(appCfg.StateDir, siteKey),
		stageDB:  storage.StagingVisitedDBPath(appCfg.StateDir, siteKey),
		journal:  journalPath(appCfg.StateDir, siteKey),
	}
	s.recover()
	if !resume {
		if err := s.Discard(); err != nil {
			return nil, err
		}
		s.Staged = true
		return s, nil
	}
	out, db := pathExists(s.stageOut), pathExists(s.stageDB)
	if out != db {
		// Half a staging pair would resume into an empty output dir and could
		// later swap it over the live corpus.
		log.Warn("Discarding incomplete staging", "output", s.stageOut, "visited_db", s.stageDB)
		if err := s.Discard(); err != nil {
			return nil, err
		}
	}
	s.Staged = out && db
	return s, nil
}

// PendingIndex is the crawl a recovered commit published to disk but not yet
// to the index; the caller publishes it and then calls Done. Nil otherwise.
func (s *Stage) PendingIndex() *CommitRecord { return s.pending }

// Done removes the commit journal once the index reflects the live corpus.
func (s *Stage) Done() error {
	s.pending = nil
	return removeJournal(s.journal)
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
// caller must have closed every handle inside them, and must call Done after
// updating the index: until then the journal lets the next crawl finish the
// commit after a crash. On failure the previous live state is restored and the
// staging is left for a retry.
func (s *Stage) Commit(rec CommitRecord) error {
	var pairs []swapPair
	for _, p := range []swapPair{{s.liveOut, s.stageOut}, {s.liveDB, s.stageDB}} {
		if pathExists(p.staging) {
			pairs = append(pairs, p)
		}
	}
	for _, p := range pairs {
		if err := os.RemoveAll(p.backup()); err != nil {
			return fmt.Errorf("clearing %s: %w", p.backup(), err)
		}
	}

	if err := writeJournal(s.journal, commitJournal{
		Version: journalVersion,
		Output:  s.liveOut, OutputStaging: s.stageOut,
		VisitedDB: s.liveDB, VisitedDBStaging: s.stageDB,
		CrawlStartedAt: rec.CrawlStartedAt, CrawlEndedAt: rec.CrawlEndedAt, Mode: rec.Mode,
	}); err != nil {
		return fmt.Errorf("writing commit journal: %w", err)
	}
	if err := s.step(); err != nil {
		return err
	}

	if err := s.rollForward(pairs); err != nil {
		if errors.Is(err, errSimulatedCrash) {
			return err
		}
		if s.rollBack(pairs) {
			if rmErr := removeJournal(s.journal); rmErr != nil {
				s.log.Warn("Could not remove commit journal", "path", s.journal, "err", rmErr)
			}
		}
		return err
	}
	s.finishSwap(pairs)
	if err := s.step(); err != nil {
		return err
	}
	s.Staged = false
	return nil
}

// rollForward moves each pair to its committed state, deciding every step from
// what is on disk so that it can run again after a crash part way through.
func (s *Stage) rollForward(pairs []swapPair) error {
	for _, p := range pairs {
		staging, live, backup := pathExists(p.staging), pathExists(p.live), pathExists(p.backup())
		switch {
		case !staging && !live && backup:
			return fmt.Errorf("can't finish the swap of %s: only its backup exists", p.live)
		case !staging:
			continue // already promoted, or never staged
		case live && backup:
			return fmt.Errorf("can't finish the swap of %s: staging, live and backup all exist", p.live)
		case live:
			if err := utils.RenameRetry(p.live, p.backup()); err != nil {
				return fmt.Errorf("moving %s aside: %w", p.live, err)
			}
			if err := s.step(); err != nil {
				return err
			}
		}
		if err := utils.RenameRetry(p.staging, p.live); err != nil {
			return fmt.Errorf("promoting %s: %w", p.staging, err)
		}
		if err := s.step(); err != nil {
			return err
		}
	}
	return nil
}

// rollBack undoes rollForward after a failed rename. It reports whether every
// rename back succeeded.
func (s *Stage) rollBack(pairs []swapPair) bool {
	ok := true
	for i := len(pairs) - 1; i >= 0; i-- {
		p := pairs[i]
		if !pathExists(p.staging) && pathExists(p.live) {
			if err := utils.RenameRetry(p.live, p.staging); err != nil {
				s.log.Error("Staging rollback failed", "path", p.live, "err", err)
				ok = false
			}
		}
	}
	for i := len(pairs) - 1; i >= 0; i-- {
		p := pairs[i]
		if pathExists(p.backup()) && !pathExists(p.live) {
			if err := utils.RenameRetry(p.backup(), p.live); err != nil {
				s.log.Error("Backup restore failed", "path", p.live, "err", err)
				ok = false
			}
		}
	}
	return ok
}

// finishSwap flushes the renames to disk and removes the previous corpus.
func (s *Stage) finishSwap(pairs []swapPair) {
	for _, p := range pairs {
		if err := utils.SyncDir(filepath.Dir(p.live)); err != nil {
			s.log.Warn("Could not flush directory", "path", filepath.Dir(p.live), "err", err)
		}
	}
	for _, p := range pairs {
		if err := os.RemoveAll(p.backup()); err != nil {
			s.log.Warn("Could not remove previous corpus backup", "path", p.backup(), "err", err)
		}
	}
}

func (s *Stage) step() error {
	s.steps++
	if s.crashAfter > 0 && s.steps == s.crashAfter {
		return errSimulatedCrash
	}
	return nil
}

// recover finishes a commit that a crash interrupted, or, with no journal,
// tidies backups left by versions that wrote none.
func (s *Stage) recover() {
	j, err := readJournal(s.journal)
	if err != nil {
		s.log.Error("Unreadable commit journal; leaving it in place", "path", s.journal, "err", err)
		return
	}
	if j == nil {
		s.recoverSwap()
		return
	}
	pairs := []swapPair{{j.Output, j.OutputStaging}, {j.VisitedDB, j.VisitedDBStaging}}
	if err := s.rollForward(pairs); err != nil {
		s.log.Error("Could not finish a commit interrupted by a crash", "journal", s.journal, "err", err)
		return
	}
	s.finishSwap(pairs)
	s.log.Warn("Finished a commit interrupted by a crash", "output", j.Output)
	s.pending = &CommitRecord{CrawlStartedAt: j.CrawlStartedAt, CrawlEndedAt: j.CrawlEndedAt, Mode: j.Mode}
}

// recoverSwap restores the live path from its backup when a Commit from a
// version without journals died between moving the live path aside and
// promoting the staging.
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
		if err := utils.RenameRetry(backup, live); err != nil {
			s.log.Error("Could not restore interrupted swap", "path", live, "err", err)
		}
	}
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return !errors.Is(err, fs.ErrNotExist)
}

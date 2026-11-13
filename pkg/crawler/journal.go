package crawler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/storage/index"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/utils"
)

const journalVersion = 1

// commitJournal is written before a staged commit's first rename and removed
// once the index has caught up, so a crash in between can be finished later.
type commitJournal struct {
	Version          int        `json:"version"`
	Output           string     `json:"output"`
	OutputStaging    string     `json:"output_staging"`
	VisitedDB        string     `json:"visited_db"`
	VisitedDBStaging string     `json:"visited_db_staging"`
	CrawlStartedAt   time.Time  `json:"crawl_started_at"`
	CrawlEndedAt     time.Time  `json:"crawl_ended_at"`
	Mode             index.Mode `json:"mode"`
}

func journalPath(stateDir, siteKey string) string {
	return filepath.Join(stateDir, utils.SanitizeFilename(siteKey)+"_commit.json")
}

func writeJournal(path string, j commitJournal) error {
	data, err := json.Marshal(j)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := utils.WriteFileAtomic(path, data, 0o644); err != nil {
		return err
	}
	return utils.SyncDir(dir)
}

// readJournal returns nil, nil when there is no journal.
func readJournal(path string) (*commitJournal, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var j commitJournal
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if j.Version != journalVersion {
		return nil, fmt.Errorf("%s has version %d, expected %d", path, j.Version, journalVersion)
	}
	return &j, nil
}

func removeJournal(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return utils.SyncDir(filepath.Dir(path))
}

// CommitPending reports whether a crash left the site's last commit unfinished;
// the site's next crawl finishes it.
func CommitPending(appCfg *config.AppConfig, siteKey string) bool {
	return pathExists(journalPath(appCfg.StateDir, siteKey))
}

func PendingCommitWarning(siteKey string) string {
	return fmt.Sprintf("site '%s' has an unfinished commit from a crash; its search results may be from the previous crawl until the next crawl of '%s'", siteKey, siteKey)
}

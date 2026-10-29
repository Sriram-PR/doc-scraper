package crawler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/models"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/parse"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/process"
)

// carryForward keeps a live page that a staged crawl failed to fetch for a
// temporary reason. Copying waits until the crawl is published, or an outage
// would pass for a crawl that saved pages.
type carryForward struct {
	liveDir   string
	jsonlPath string
	spans     map[string]pageSpan // normalized URL -> its last record in the live JSONL
	rawURLs   map[string]string
	log       *slog.Logger
	carried   atomic.Int64

	mu      sync.Mutex
	pending map[string]*url.URL
}

// newCarryForward indexes the live JSONL; nil when there is no live corpus.
func newCarryForward(liveDir, jsonlName string, log *slog.Logger) (*carryForward, error) {
	path := filepath.Join(liveDir, jsonlName)
	raw, err := indexLastPageOffsets(path)
	if err != nil {
		return nil, fmt.Errorf("indexing live JSONL: %w", err)
	}
	if len(raw) == 0 {
		return nil, nil
	}
	cf := &carryForward{liveDir: liveDir, jsonlPath: path, spans: map[string]pageSpan{}, rawURLs: map[string]string{}, log: log, pending: map[string]*url.URL{}}
	for rawURL, span := range raw {
		u, err := url.Parse(rawURL)
		if err != nil {
			continue
		}
		key := parse.NormalizeURL(u)
		cf.spans[key], cf.rawURLs[key] = span, rawURL
	}
	return cf, nil
}

// note marks u, which failed with a temporary error, for carrying if the live
// corpus has it.
func (cf *carryForward) note(u *url.URL) {
	key := parse.NormalizeURL(u)
	if _, ok := cf.spans[key]; !ok {
		return
	}
	cf.mu.Lock()
	defer cf.mu.Unlock()
	cf.pending[key] = u
}

// toCarry returns the noted pages the run has no fresh record of, sorted by key.
func (cf *carryForward) toCarry(om *OutputManager) []*url.URL {
	if cf == nil {
		return nil
	}
	cf.mu.Lock()
	defer cf.mu.Unlock()
	if len(cf.pending) == 0 {
		return nil
	}
	fresh := om.recordedPageKeys()
	keys := make([]string, 0, len(cf.pending))
	for k := range cf.pending {
		if _, ok := fresh[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	urls := make([]*url.URL, len(keys))
	for i, k := range keys {
		urls[i] = cf.pending[k]
	}
	return urls
}

func (cf *carryForward) carryable(om *OutputManager) int {
	return len(cf.toCarry(om))
}

func (cf *carryForward) carryAll(om *OutputManager, cp *process.ContentProcessor, siteCfg *config.SiteConfig, stagedDir string) {
	for _, u := range cf.toCarry(om) {
		cf.carry(u, om, cp, siteCfg, stagedDir)
	}
}

func (cf *carryForward) count() int64 {
	if cf == nil {
		return 0
	}
	return cf.carried.Load()
}

// carry copies u's live record, Markdown file and local images into the staged
// output. It copies nothing and returns false when the live corpus has no
// record or Markdown file for u.
func (cf *carryForward) carry(u *url.URL, om *OutputManager, cp *process.ContentProcessor, siteCfg *config.SiteConfig, stagedDir string) bool {
	key := parse.NormalizeURL(u)
	span, ok := cf.spans[key]
	if !ok {
		return false
	}
	recURL, err := url.Parse(cf.rawURLs[key])
	if err != nil {
		return false
	}
	liveMD, ok := cp.OutputPathForURL(recURL, siteCfg, cf.liveDir)
	if !ok {
		return false
	}
	rel, err := filepath.Rel(cf.liveDir, liveMD)
	if err != nil {
		return false
	}
	line, err := readSpan(cf.jsonlPath, span)
	if err != nil {
		cf.log.Warn("Could not read the live record to carry forward", "url", u.String(), "err", err)
		return false
	}
	var rec models.PageJSONL
	if err := json.Unmarshal(line, &rec); err != nil {
		return false
	}
	stagedMD := filepath.Join(stagedDir, rel)
	if err := copyFile(liveMD, stagedMD); err != nil {
		if !os.IsNotExist(err) {
			cf.log.Warn("Could not carry a page's Markdown forward", "url", u.String(), "err", err)
		}
		return false
	}
	for _, img := range rec.Images {
		cf.copyImage(img, filepath.Dir(liveMD), stagedDir)
	}
	if !om.appendRaw(line) {
		return false
	}
	cf.carried.Add(1)
	return true
}

func (cf *carryForward) copyImage(ref, liveMDDir, stagedDir string) {
	if strings.Contains(ref, "://") || strings.HasPrefix(ref, "/") || strings.HasPrefix(ref, "data:") {
		return
	}
	src := filepath.Join(liveMDDir, filepath.FromSlash(ref))
	rel, err := filepath.Rel(cf.liveDir, src)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return
	}
	dst := filepath.Join(stagedDir, rel)
	if _, err := os.Stat(dst); err == nil {
		return
	}
	if err := copyFile(src, dst); err != nil && !os.IsNotExist(err) {
		cf.log.Warn("Could not carry an image forward", "path", src, "err", err)
	}
}

func readSpan(path string, span pageSpan) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, span.length)
	if _, err := f.ReadAt(buf, span.offset); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf, nil
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

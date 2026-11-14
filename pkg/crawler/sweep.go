package crawler

import (
	"bufio"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/parse"
)

const maxFrontmatterBytes = 64 << 10

// sweepInput is what a finished in-place run knows about the pages it keeps.
type sweepInput struct {
	dir     string                          // site output dir
	retired map[string]struct{}             // normalized URLs retired this run
	claimed map[string]string               // Markdown path -> page URL that saved or kept it this run
	corpus  map[string]struct{}             // normalized page URLs in the finalized JSONL; nil skips the corpus rule
	stored  func(normalizedURL string) bool // the visited DB holds the URL as a success
}

// sweepOrphans deletes Markdown files no page of the corpus uses any more: a
// retired page's, one under an older name of a page saved elsewhere this run,
// and one whose page is in neither the JSONL nor the visited DB. Only files
// with doc-scraper page front matter are considered.
func sweepOrphans(in sweepInput, log *slog.Logger) int {
	dir := filepath.Clean(in.dir)
	// Root-scoped, so a symlinked directory swapped in mid-walk can't redirect a removal.
	root, err := os.OpenRoot(dir)
	if err != nil {
		log.Warn("Could not open the output directory to remove orphaned files", "dir", dir, "err", err)
		return 0
	}
	defer func() { _ = root.Close() }()
	claimedPaths := make(map[string]struct{}, len(in.claimed))
	claimedURLs := make(map[string]struct{}, len(in.claimed))
	for p, raw := range in.claimed {
		claimedPaths[filepath.Clean(p)] = struct{}{}
		if u, err := url.Parse(raw); err == nil {
			claimedURLs[parse.NormalizeURL(u)] = struct{}{}
		}
	}

	var removed []string
	_ = fs.WalkDir(root.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		name := filepath.FromSlash(rel)
		p := filepath.Join(dir, name)
		if err != nil {
			log.Warn("Could not read part of the output directory while removing orphaned files", "path", p, "err", err)
			return nil //nolint:nilerr // an unreadable part is left alone, the rest is still swept
		}
		if !d.Type().IsRegular() || !strings.HasSuffix(rel, ".md") {
			return nil
		}
		if _, ok := claimedPaths[p]; !ok && in.removeIfOrphan(root, name, p, claimedURLs, log) {
			removed = append(removed, name)
		}
		return nil
	})
	for _, name := range removed {
		removeEmptyParents(root, filepath.Dir(name))
	}
	return len(removed)
}

// removeIfOrphan removes the file when it is a doc-scraper page file that no
// page of the corpus uses, and reports whether it did.
func (in sweepInput) removeIfOrphan(root *os.Root, name, p string, claimedURLs map[string]struct{}, log *slog.Logger) bool {
	pageURL, err := frontmatterURL(root, name)
	if err != nil {
		log.Warn("Could not read a Markdown file's front matter; leaving it", "path", p, "err", err)
		return false
	}
	if pageURL == "" {
		return false
	}
	u, err := url.Parse(pageURL)
	if err != nil || !in.orphan(parse.NormalizeURL(u), claimedURLs) {
		return false
	}
	if err := root.Remove(name); err != nil {
		log.Warn("Could not remove an orphaned Markdown file", "path", p, "err", err)
		return false
	}
	log.Info("Removed an orphaned Markdown file", "path", p, "url", pageURL)
	return true
}

func (in sweepInput) orphan(key string, claimedURLs map[string]struct{}) bool {
	if _, ok := in.retired[key]; ok {
		return true
	}
	if _, ok := claimedURLs[key]; ok {
		return true
	}
	if in.corpus == nil {
		return false
	}
	if _, ok := in.corpus[key]; ok {
		return false
	}
	return !in.stored(key)
}

// frontmatterURL returns the url of a doc-scraper page file, or "" when the
// file does not start with front matter carrying url, crawled_at and content_hash.
func frontmatterURL(root *os.Root, name string) (string, error) {
	f, err := root.Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	r := bufio.NewReader(io.LimitReader(f, maxFrontmatterBytes))
	first, err := r.ReadString('\n')
	if strings.TrimRight(first, "\r\n") != "---" {
		return "", ignoreEOF(err)
	}
	var b strings.Builder
	for {
		line, err := r.ReadString('\n')
		if strings.TrimRight(line, "\r\n") == "---" {
			break
		}
		if err != nil {
			return "", ignoreEOF(err)
		}
		b.WriteString(line)
	}
	var fm struct {
		URL         string `yaml:"url"`
		CrawledAt   string `yaml:"crawled_at"`
		ContentHash string `yaml:"content_hash"`
	}
	if yaml.Unmarshal([]byte(b.String()), &fm) != nil || fm.URL == "" || fm.CrawledAt == "" || fm.ContentHash == "" {
		return "", nil
	}
	return fm.URL, nil
}

func ignoreEOF(err error) error {
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

func removeEmptyParents(root *os.Root, dir string) {
	for dir != "." && dir != string(filepath.Separator) {
		if root.Remove(dir) != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

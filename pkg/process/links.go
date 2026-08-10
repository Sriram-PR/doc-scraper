package process

import (
	"fmt"
	"log/slog"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"

	"github.com/PuerkitoBio/goquery"

	"github.com/Sriram-PR/doc-scraper/pkg/config"
	"github.com/Sriram-PR/doc-scraper/pkg/models"
	"github.com/Sriram-PR/doc-scraper/pkg/parse"
	"github.com/Sriram-PR/doc-scraper/pkg/queue"
	"github.com/Sriram-PR/doc-scraper/pkg/storage"
	"github.com/Sriram-PR/doc-scraper/pkg/utils"
)

// resourceExtensions is the set of file extensions that are never crawlable pages.
var resourceExtensions = map[string]struct{}{
	".svg": {}, ".png": {}, ".jpg": {}, ".jpeg": {}, ".gif": {}, ".webp": {}, ".ico": {}, ".bmp": {}, ".tiff": {}, ".avif": {},
	".woff": {}, ".woff2": {}, ".ttf": {}, ".eot": {}, ".otf": {},
	".css": {}, ".js": {}, ".mjs": {},
	".pdf": {}, ".zip": {}, ".tar": {}, ".gz": {}, ".tgz": {},
	".mp4": {}, ".mp3": {}, ".wav": {}, ".ogg": {}, ".webm": {},
}

func isResourceURL(u *url.URL) bool {
	ext := strings.ToLower(path.Ext(u.Path))
	_, ok := resourceExtensions[ext]
	return ok
}

// LinkProcessor handles extracting and queueing links found on a page.
type LinkProcessor struct {
	store                      storage.PageStore
	pq                         *queue.ThreadSafePriorityQueue
	compiledDisallowedPatterns []*regexp.Regexp
	log                        *slog.Logger
}

func NewLinkProcessor(
	store storage.PageStore,
	pq *queue.ThreadSafePriorityQueue,
	compiledDisallowedPatterns []*regexp.Regexp,
	log *slog.Logger,
) *LinkProcessor {
	return &LinkProcessor{
		store:                      store,
		pq:                         pq,
		compiledDisallowedPatterns: compiledDisallowedPatterns,
		log:                        log,
	}
}

// ExtractAndQueueLinks finds crawlable links in the original (pre-conversion) document, filters
// them against scope and disallowed patterns, and enqueues newly-seen URLs. Returns the count
// queued and the first non-fatal DB error encountered.
func (lp *LinkProcessor) ExtractAndQueueLinks( //nolint:gocyclo // link extraction with many filtering and normalization steps
	originalDoc *goquery.Document,
	finalURL *url.URL,
	currentDepth int,
	siteCfg *config.SiteConfig,
	wg *sync.WaitGroup,
	taskLog *slog.Logger,
) (queuedCount int, err error) {

	nextDepth := currentDepth + 1
	taskLog = taskLog.With("next_depth", nextDepth)
	taskLog.Debug("Extracting and queueing links...")
	var firstDBError error

	if siteCfg.MaxDepth > 0 && nextDepth > siteCfg.MaxDepth {
		taskLog.Debug(fmt.Sprintf("Max depth (%d) reached/exceeded for next level (%d), skipping link extraction.", siteCfg.MaxDepth, nextDepth))
		return 0, nil
	}

	foundLinks := make(map[string]struct{}) // set of normalized URLs

	selectorsToSearch := siteCfg.LinkExtractionSelectors
	if len(selectorsToSearch) == 0 {
		selectorsToSearch = []string{"body"}
		taskLog.Debug("No link_extraction_selectors defined, defaulting to 'body'")
	} else {
		taskLog.Debug(fmt.Sprintf("Using link_extraction_selectors: %v", selectorsToSearch))
	}

	for _, selector := range selectorsToSearch {
		taskLog.Debug(fmt.Sprintf("Searching for links within selector: '%s'", selector))
		originalDoc.Find(selector).Find("a[href]").Each(func(index int, element *goquery.Selection) {
			href, exists := element.Attr("href")
			if !exists || href == "" {
				return
			}

			if siteCfg.RespectNofollow {
				if rel, _ := element.Attr("rel"); strings.Contains(strings.ToLower(rel), "nofollow") {
					taskLog.Debug(fmt.Sprintf("Skipping nofollow link: %s", href))
					return
				}
			}

			linkURL, parseErr := finalURL.Parse(href)
			if parseErr != nil {
				taskLog.Warn(fmt.Sprintf("Skipping invalid link href '%s' in selector '%s': %v", href, selector, parseErr))
				return
			}
			absoluteLinkURL := linkURL.String()

			if isResourceURL(linkURL) {
				return
			}
			if linkURL.Scheme != "http" && linkURL.Scheme != "https" {
				return
			}

			if linkURL.Hostname() != siteCfg.AllowedDomain {
				return
			}

			targetPath := linkURL.Path
			if targetPath == "" {
				targetPath = "/"
			}
			if !strings.HasPrefix(targetPath, siteCfg.AllowedPathPrefix) {
				return
			}

			isDisallowed := false
			for _, pattern := range lp.compiledDisallowedPatterns {
				if pattern.MatchString(linkURL.Path) {
					isDisallowed = true
					taskLog.Debug(fmt.Sprintf("Link '%s' disallowed by pattern: %s", absoluteLinkURL, pattern.String()))
					break
				}
			}
			if isDisallowed {
				return
			}

			normalizedLink, _, errNorm := parse.ParseAndNormalize(absoluteLinkURL)
			if errNorm != nil {
				taskLog.Warn(fmt.Sprintf("Cannot normalize extracted link '%s': %v", absoluteLinkURL, errNorm))
				return
			}

			foundLinks[normalizedLink] = struct{}{}
		})
	}

	if len(foundLinks) > 0 {
		taskLog.Debug(fmt.Sprintf("Found %d unique, valid, in-scope links across all specified selectors.", len(foundLinks)))
		for normalizedLink := range foundLinks {
			added, visitErr := lp.store.MarkPageVisited(normalizedLink)
			if visitErr != nil {
				dbErr := fmt.Errorf("%w: checking/marking link '%s' visited: %w", utils.ErrDatabase, normalizedLink, visitErr)
				taskLog.Error(dbErr.Error())
				if firstDBError == nil {
					firstDBError = dbErr
				}
				continue
			}

			if added {
				wg.Add(1)
				// Enqueue the normalized URL: the DB key and WorkItem.URL then
				// agree, which closes a dedup-escape race where a same-page
				// anchor (e.g. index.html#foo) could be enqueued and later
				// processed as a distinct page if the parent's deferred
				// UpdatePageStatus had not yet marked the bare URL Success.
				nextWorkItem := models.WorkItem{URL: normalizedLink, Depth: nextDepth}
				lp.pq.Add(&nextWorkItem)
				queuedCount++
				taskLog.Debug(fmt.Sprintf("Queued new link (normalized): %s", normalizedLink))
			} else {
				taskLog.Debug(fmt.Sprintf("Link already visited/pending, skipping queue: %s", normalizedLink))
			}
		}
	} else {
		taskLog.Debug("No new valid links found to queue.")
	}

	taskLog.Info(fmt.Sprintf("Finished link extraction. Queued %d NEW links.", queuedCount))
	return queuedCount, firstDBError
}

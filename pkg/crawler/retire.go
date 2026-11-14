package crawler

import (
	"errors"
	"log/slog"
	"net/url"
	"sync"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/parse"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/utils"
)

const (
	retireGone       = "gone"
	retireRobots     = "robots"
	retireScope      = "scope"
	retireNonHTML    = "non-html"
	retireMaxDepth   = "max-depth"
	retireNoindex    = "noindex"
	retireRedirected = "redirected"
)

// retireReason says why a failed page leaves the corpus, or "" when it keeps
// its last good copy. A blocked address is an environment problem, not the
// site dropping the page.
func retireReason(err error, gone bool) string {
	switch {
	case gone:
		return retireGone
	case errors.Is(err, utils.ErrRobotsDisallowed):
		return retireRobots
	case errors.Is(err, utils.ErrScopeViolation):
		return retireScope
	case errors.Is(err, utils.ErrNonHTMLContent):
		return retireNonHTML
	case errors.Is(err, utils.ErrMaxDepthExceeded):
		return retireMaxDepth
	}
	return ""
}

// deletesVisitedKey: a redirected URL is how the next run reaches its new
// URL, and a noindex entry records that the page is noindex.
func deletesVisitedKey(reason string) bool {
	return reason != retireNoindex && reason != retireRedirected
}

type retiredPage struct {
	u      *url.URL
	reason string
}

// retireList collects the pages a completed run removes from the corpus.
type retireList struct {
	mu      sync.Mutex
	entries []retiredPage
}

func (r *retireList) add(u *url.URL, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, retiredPage{u: u, reason: reason})
}

// pending returns one entry per URL, leaving out URLs this run saved or kept:
// two tasks can reach one URL, and the one that saved it wins.
func (r *retireList) pending(claimed map[string]struct{}) []retiredPage {
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := make(map[string]struct{}, len(r.entries))
	var out []retiredPage
	for _, e := range r.entries {
		k := parse.NormalizeURL(e.u)
		if _, ok := claimed[k]; ok {
			continue
		}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, e)
	}
	return out
}

func retiredURLs(entries []retiredPage) []*url.URL {
	out := make([]*url.URL, len(entries))
	for i, e := range entries {
		out[i] = e.u
	}
	return out
}

type pageDropper interface{ DropPage(u *url.URL) }

type pageDeleter interface {
	DeletePage(normalizedPageURL string) error
}

// applyRetirements drops each entry's JSONL record and, where deletesKey says
// so, its visited-DB entry. present holds the normalized URLs the corpus had
// before (nil: unknown, treat all as present); only those are logged as removed.
// Returns the normalized URLs of every entry.
func applyRetirements(entries []retiredPage, present map[string]struct{}, out pageDropper, store pageDeleter, deletesKey func(reason string) bool, log *slog.Logger) map[string]struct{} {
	retired := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		k := parse.NormalizeURL(e.u)
		retired[k] = struct{}{}
		out.DropPage(e.u)
		if deletesKey(e.reason) {
			if err := store.DeletePage(k); err != nil {
				log.Warn("Could not forget a removed page in the visited DB; the next run checks it again", "url", e.u.String(), "err", err)
			}
		}
		if _, ok := present[k]; ok || present == nil {
			log.Info("Removed a page from the corpus", "url", e.u.String(), "reason", e.reason)
		} else {
			log.Debug("Page left out of the corpus", "url", e.u.String(), "reason", e.reason)
		}
	}
	return retired
}

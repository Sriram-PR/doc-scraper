package crawler

import (
	"context"
	"errors"
	"sort"
	"sync"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/utils"
)

const (
	FailureTemporary = "temporary"
	FailureGone      = "gone"
	FailureSelector  = "selector"
	FailurePolicy    = "policy"
	FailureOther     = "other"
)

// FailureClass counts one kind of page failure, with the first page that hit it.
type FailureClass struct {
	Class       string `json:"class"`
	Count       int64  `json:"count"`
	SampleURL   string `json:"sample_url"`
	SampleError string `json:"sample_error"`
}

// Failures summarizes a crawl's failed pages by class, most frequent first.
// Noindex pages are counted apart: they are neither saved nor failed.
type Failures struct {
	Classes []FailureClass `json:"classes"`
	Noindex int64          `json:"noindex"`
}

func (f Failures) Count(class string) int64 {
	for _, c := range f.Classes {
		if c.Class == class {
			return c.Count
		}
	}
	return 0
}

// classify sorts a failed task's error into a class. Temporary failures are the
// ones a later run can be expected to fix; only they count toward refusing a
// crawl, because steady 404s, selector misses and scope skips are normal on
// healthy sites (pinia: 57% 404s on every run).
func classify(err error, gone bool) string {
	switch {
	case gone:
		return FailureGone
	case errors.Is(err, utils.ErrPageTooLarge):
		return FailureOther
	case errors.Is(err, utils.ErrRetryFailed),
		errors.Is(err, utils.ErrResponseBodyRead),
		errors.Is(err, utils.ErrSemaphoreTimeout),
		errors.Is(err, context.DeadlineExceeded):
		return FailureTemporary
	case errors.Is(err, utils.ErrContentSelector):
		return FailureSelector
	case errors.Is(err, utils.ErrRobotsDisallowed),
		errors.Is(err, utils.ErrScopeViolation),
		errors.Is(err, utils.ErrNonHTMLContent),
		errors.Is(err, utils.ErrMaxDepthExceeded),
		errors.Is(err, utils.ErrBlockedAddress):
		return FailurePolicy
	default:
		return FailureOther
	}
}

type failureTally struct {
	mu      sync.Mutex
	classes map[string]*FailureClass
	noindex int64
}

func (t *failureTally) record(u string, err error, class string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.classes == nil {
		t.classes = map[string]*FailureClass{}
	}
	c, ok := t.classes[class]
	if !ok {
		c = &FailureClass{Class: class, SampleURL: u, SampleError: err.Error()}
		t.classes[class] = c
	}
	c.Count++
}

func (t *failureTally) markNoindex() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.noindex++
}

func (t *failureTally) snapshot() Failures {
	t.mu.Lock()
	defer t.mu.Unlock()
	f := Failures{Noindex: t.noindex, Classes: make([]FailureClass, 0, len(t.classes))}
	for _, c := range t.classes {
		f.Classes = append(f.Classes, *c)
	}
	sort.Slice(f.Classes, func(i, j int) bool {
		if f.Classes[i].Count != f.Classes[j].Count {
			return f.Classes[i].Count > f.Classes[j].Count
		}
		return f.Classes[i].Class < f.Classes[j].Class
	})
	return f
}

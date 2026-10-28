// Package pagebudget bounds the memory that parsing and converting HTML pages
// can take across the whole process.
package pagebudget

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"
	"golang.org/x/sync/semaphore"
)

// One unit is roughly 0.5-0.9 KB of peak RSS through parse, readability and
// Markdown conversion, measured on real and hostile pages.
const (
	// MaxPageUnits is the heaviest page accepted; the WHATWG HTML spec weighs
	// about 1,000,000.
	MaxPageUnits      = 1_500_000
	sharedBudgetUnits = 1_600_000
	bytesPerUnit      = 64
	attrsPerUnit      = 3
)

// ErrPageTooComplex reports a page that is too heavy to parse.
var ErrPageTooComplex = errors.New("page too complex")

// Budget is a pool of units that page parses draw from.
type Budget struct {
	sem      *semaphore.Weighted
	capacity int64
	noWait   bool
}

// NewBudget returns a budget of the given capacity whose Parse waits for room.
func NewBudget(units int64) *Budget {
	return &Budget{sem: semaphore.NewWeighted(units), capacity: units}
}

// Shared is the process-wide budget every HTML page parse draws from.
var Shared = NewBudget(sharedBudgetUnits)

// Parse weighs body, refuses it with ErrPageTooComplex when it exceeds
// MaxPageUnits or b's capacity, waits for room in b, and parses it. release
// must be called once the document and everything derived from it are no
// longer used; calling it again does nothing. A goroutine must not call Parse
// while it holds an unreleased document from the same budget.
func (b *Budget) Parse(ctx context.Context, body []byte) (doc *goquery.Document, release func(), err error) {
	w := weigh(body)
	if limit := min(MaxPageUnits, b.capacity); w > limit {
		return nil, nil, fmt.Errorf("%w: weight %d is over the limit of %d", ErrPageTooComplex, w, limit)
	}
	if b.noWait {
		if !b.sem.TryAcquire(w) {
			return nil, nil, fmt.Errorf("%w: weight %d does not fit in what is left of this reservation", ErrPageTooComplex, w)
		}
	} else if err := b.sem.Acquire(ctx, w); err != nil {
		return nil, nil, err
	}
	release = releaseOnce(b.sem, w)
	doc, err = goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		release()
		return nil, nil, err
	}
	return doc, release, nil
}

// Reserve waits for units from b and returns a budget of that capacity whose
// Parse never waits: a page that does not fit in what is left fails with
// ErrPageTooComplex. It is for callers that keep several documents alive at
// once. release returns the units to b.
func (b *Budget) Reserve(ctx context.Context, units int64) (child *Budget, release func(), err error) {
	if units > b.capacity {
		return nil, nil, fmt.Errorf("%w: reservation of %d is over the capacity of %d", ErrPageTooComplex, units, b.capacity)
	}
	if err := b.sem.Acquire(ctx, units); err != nil {
		return nil, nil, err
	}
	child = &Budget{sem: semaphore.NewWeighted(units), capacity: units, noWait: true}
	return child, releaseOnce(b.sem, units), nil
}

func releaseOnce(sem *semaphore.Weighted, n int64) func() {
	var once sync.Once
	return func() { once.Do(func() { sem.Release(n) }) }
}

// weigh scans body with the tokenizer, which allocates almost nothing, so a
// page can be refused before it is parsed.
func weigh(body []byte) int64 {
	var tags, attrs int64
	z := html.NewTokenizer(bytes.NewReader(body))
	for {
		switch z.Next() {
		case html.ErrorToken:
			return tags + attrs/attrsPerUnit + int64(len(body))/bytesPerUnit
		case html.StartTagToken, html.SelfClosingTagToken:
			tags++
			attrs += countAttrs(z.Raw())
		case html.EndTagToken:
			tags++
		}
	}
}

// countAttrs counts the attributes in a start tag's raw bytes by the
// tokenizer's rules. TagAttr copies every key and value, which on a large page
// is enough garbage to raise the peak of the parse that follows. Duplicate
// names, which the tokenizer drops, are counted.
func countAttrs(raw []byte) int64 {
	n := len(raw)
	i := 1
	for i < n && raw[i] != '/' && raw[i] != '>' {
		i++
		if isSpace(raw[i-1]) {
			break
		}
	}
	var count int64
	i = skipSpace(raw, i)
	for i < n && raw[i] != '>' {
		start := i
		for i < n {
			c := raw[i]
			if c == '=' && i == start {
				i++
				continue
			}
			if c == '=' || c == '/' || c == '>' || isSpace(c) {
				break
			}
			i++
		}
		if i > start {
			count++
		}
		i = skipAttrValue(raw, skipSpace(raw, i))
		i = skipSpace(raw, i)
	}
	return count
}

// skipAttrValue skips an optional "=value" at i, mirroring readTagAttrVal.
func skipAttrValue(raw []byte, i int) int {
	n := len(raw)
	if i >= n {
		return i
	}
	switch raw[i] {
	case '/':
		return i + 1
	case '=':
	default:
		return i
	}
	i = skipSpace(raw, i+1)
	if i >= n {
		return i
	}
	switch q := raw[i]; q {
	case '>':
		return i
	case '\'', '"':
		i++
		for i < n && raw[i] != q {
			i++
		}
		return min(i+1, n)
	default:
		for i < n && raw[i] != '>' && !isSpace(raw[i]) {
			i++
		}
		return i
	}
}

func skipSpace(raw []byte, i int) int {
	for i < len(raw) && isSpace(raw[i]) {
		i++
	}
	return i
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\n' || c == '\r' || c == '\t' || c == '\f'
}

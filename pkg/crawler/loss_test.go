package crawler

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDecideLoss(t *testing.T) {
	cases := []struct {
		name   string
		in     lossInput
		reason string // "" means ok
	}{
		{"fresh crawl, no baseline", lossInput{NewPages: 21, Attempted: 21, Processed: 21, Succeeded: 21}, ""},
		{"empty fresh crawl", lossInput{NewPages: 0, Attempted: 4, Processed: 5}, RefusedEmpty},
		{"empty ignores allow-shrink", lossInput{NewPages: 0, Processed: 1, AllowShrink: true}, RefusedEmpty},
		{"nothing processed is not empty", lossInput{NewPages: 0, Processed: 0}, ""},
		{"resume retrying only failed pages", lossInput{NewPages: 1, Processed: 1, HasBaseline: true, Baseline: 1}, ""},
		{"incremental with nothing checked", lossInput{Incremental: true, NewPages: 21, Processed: 21, Attempted: 21, Transient: 21}, RefusedEmpty},
		{"incremental partial 404", lossInput{Incremental: true, NewPages: 1, Processed: 1, Unchanged: 1, Attempted: 1, HasBaseline: true, Baseline: 2}, ""},
		{"15 of 21 transient", lossInput{NewPages: 6, Attempted: 21, Transient: 15, Processed: 21, Succeeded: 6, HasBaseline: true, Baseline: 21}, RefusedTemporaryFailures},
		{"2 transient is under the floor", lossInput{NewPages: 2, Attempted: 4, Transient: 2, Processed: 4, Succeeded: 2}, ""},
		{"3 of 12 transient is exactly 25%", lossInput{NewPages: 9, Attempted: 12, Transient: 3, Processed: 12, Succeeded: 9}, RefusedTemporaryFailures},
		{"6 of 25 transient is 24%", lossInput{NewPages: 19, Attempted: 25, Transient: 6, Processed: 25, Succeeded: 19}, ""},
		{"3 of 13 transient is under 25%", lossInput{NewPages: 10, Attempted: 13, Transient: 3, Processed: 13, Succeeded: 10}, ""},
		{"allow-shrink accepts transient loss", lossInput{NewPages: 6, Attempted: 21, Transient: 15, Processed: 21, Succeeded: 6, AllowShrink: true}, ""},
		{"shrink to 70% refused", lossInput{NewPages: 14, Attempted: 14, Processed: 14, Succeeded: 14, HasBaseline: true, Baseline: 20}, RefusedShrink},
		{"shrink to 71% ok", lossInput{NewPages: 71, Attempted: 71, Processed: 71, Succeeded: 71, HasBaseline: true, Baseline: 100}, ""},
		{"baseline of 19 is too small to judge", lossInput{NewPages: 1, Attempted: 1, Processed: 1, Succeeded: 1, HasBaseline: true, Baseline: 19}, ""},
		{"allow-shrink accepts shrink", lossInput{NewPages: 6, Attempted: 6, Processed: 6, Succeeded: 6, HasBaseline: true, Baseline: 21, AllowShrink: true}, ""},
		// Measured healthy re-crawls (docs/research/v2.25.0-lossy-crawl.md) must pass.
		{"pinia: 57% 404s", lossInput{NewPages: 145, Attempted: 145, Processed: 338, Succeeded: 145, HasBaseline: true, Baseline: 145}, ""},
		{"furo root: 60% selector misses", lossInput{NewPages: 48, Attempted: 124, Processed: 126, Succeeded: 48, HasBaseline: true, Baseline: 48}, ""},
		{"go.dev: redirects", lossInput{NewPages: 100, Attempted: 123, Processed: 129, Succeeded: 123, HasBaseline: true, Baseline: 100}, ""},
		{"gitbook: 23% noindex", lossInput{NewPages: 247, Attempted: 256, Processed: 333, Succeeded: 251, HasBaseline: true, Baseline: 247}, ""},
		{"outage: only carried pages", lossInput{NewPages: 0, Carryable: 1, Attempted: 1, Transient: 1, Processed: 1, AllowShrink: true}, RefusedEmpty},
		{"one 503 carried keeps the corpus whole", lossInput{NewPages: 20, Carryable: 1, Attempted: 21, Transient: 1, Processed: 21, Succeeded: 20, HasBaseline: true, Baseline: 21}, ""},
		{"carried pages count against shrink", lossInput{NewPages: 13, Carryable: 2, Attempted: 15, Transient: 2, Processed: 15, Succeeded: 13, HasBaseline: true, Baseline: 20}, ""},
		{"resend: 2 other failures", lossInput{NewPages: 422, Attempted: 432, Processed: 432, Succeeded: 430, HasBaseline: true, Baseline: 422}, ""},
		{"click 429 storm", lossInput{NewPages: 2, Attempted: 38, Transient: 36, Processed: 38, Succeeded: 2, HasBaseline: true, Baseline: 38}, RefusedTemporaryFailures},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := decideLoss(tc.in)
			assert.Equal(t, tc.reason != "", v.Refused)
			assert.Equal(t, tc.reason, v.Reason)
		})
	}
}

func TestLossyCrawlErrorText(t *testing.T) {
	failures := Failures{Classes: []FailureClass{{Class: FailureTemporary, Count: 15, SampleURL: "http://h/docs/p7.html", SampleError: "server HTTP error (5xx): status 500"}}}
	transient := &LossyCrawlError{SiteKey: "s", Reason: RefusedTemporaryFailures, NewPages: 6, Baseline: 21, Transient: 15, Attempted: 21, Failures: failures, StagingKept: true}
	assert.Equal(t, "crawl kept back: 15 of 21 pages failed with temporary errors (e.g. http://h/docs/p7.html: server HTTP error (5xx): status 500)", transient.Error())
	assert.Contains(t, transient.Hint("cli"), "doc-scraper crawl -site s --resume`")
	assert.Contains(t, transient.Hint("cli"), "--resume -allow-shrink")
	assert.Contains(t, transient.Hint("mcp"), "incremental:true and allow_shrink:true")

	shrink := &LossyCrawlError{SiteKey: "s", Reason: RefusedShrink, NewPages: 6, Baseline: 21}
	assert.Equal(t, "crawl kept back: this crawl has 6 pages, the last recorded crawl had 21 (-71%)", shrink.Error())

	empty := &LossyCrawlError{SiteKey: "s", Reason: RefusedEmpty, Processed: 5, StagingKept: true}
	assert.Equal(t, "crawl kept back: zero successful pages (5 attempted)", empty.Error())
	assert.NotContains(t, empty.Hint("cli"), "allow-shrink", "allow-shrink cannot publish an empty crawl")

	inPlace := &LossyCrawlError{SiteKey: "s", Reason: RefusedTemporaryFailures, Transient: 15, Attempted: 21}
	assert.Contains(t, inPlace.Hint("cli"), "not recorded in crawl history")
	assert.Contains(t, inPlace.Hint("mcp"), "not recorded in crawl history")
}

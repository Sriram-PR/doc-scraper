package crawler

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/utils"
)

func TestClassify(t *testing.T) {
	retried := func(inner error) error { return fmt.Errorf("%w: %w", utils.ErrRetryFailed, inner) }
	cases := []struct {
		name string
		err  error
		gone bool
		want string
	}{
		{"5xx after retries", retried(fmt.Errorf("%w: status 500", utils.ErrServerHTTPError)), false, FailureTemporary},
		{"429 after retries", retried(fmt.Errorf("%w: status 429", utils.ErrClientHTTPError)), false, FailureTemporary},
		{"network error after retries", retried(errors.New("connection refused")), false, FailureTemporary},
		{"per-page timeout", fmt.Errorf("fetch: %w", context.DeadlineExceeded), false, FailureTemporary},
		{"body read", fmt.Errorf("%w: reading body: %w", utils.ErrResponseBodyRead, errors.New("unexpected EOF")), false, FailureTemporary},
		{"semaphore timeout", fmt.Errorf("%w: acquire", utils.ErrSemaphoreTimeout), false, FailureTemporary},
		{"page too large", fmt.Errorf("%w: %w: 60MB", utils.ErrResponseBodyRead, utils.ErrPageTooLarge), false, FailureOther},
		{"404", fmt.Errorf("%w: status 404", utils.ErrClientHTTPError), true, FailureGone},
		{"403", fmt.Errorf("%w: status 403", utils.ErrClientHTTPError), false, FailureOther},
		{"selector", fmt.Errorf("%w: selector '.x'", utils.ErrContentSelector), false, FailureSelector},
		{"robots", fmt.Errorf("%w: /docs/x", utils.ErrRobotsDisallowed), false, FailurePolicy},
		{"scope", fmt.Errorf("%w: redirected", utils.ErrScopeViolation), false, FailurePolicy},
		{"non-HTML", fmt.Errorf("%w: image/png", utils.ErrNonHTMLContent), false, FailurePolicy},
		{"depth", utils.ErrMaxDepthExceeded, false, FailurePolicy},
		{"SSRF block", fmt.Errorf("dial: %w", utils.ErrBlockedAddress), false, FailurePolicy},
		{"parse", fmt.Errorf("%w: html", utils.ErrParsing), false, FailureOther},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, classify(tc.err, tc.gone))
		})
	}
}

func TestFailureTally(t *testing.T) {
	var ft failureTally
	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ft.record(fmt.Sprintf("http://h/docs/p%d.html", i), errors.New("boom"), FailureTemporary)
		}()
	}
	wg.Wait()
	ft.record("http://h/docs/gone.html", errors.New("404"), FailureGone)
	ft.markNoindex()

	f := ft.snapshot()
	assert.EqualValues(t, 10, f.Count(FailureTemporary))
	assert.EqualValues(t, 1, f.Count(FailureGone))
	assert.EqualValues(t, 0, f.Count(FailureSelector))
	assert.EqualValues(t, 1, f.Noindex)
	assert.Equal(t, FailureTemporary, f.Classes[0].Class, "most frequent class first")
	assert.NotEmpty(t, f.Classes[0].SampleURL)
	assert.Equal(t, "boom", f.Classes[0].SampleError)
}

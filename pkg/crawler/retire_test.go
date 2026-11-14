package crawler

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/utils"
)

type fakeDropper struct{ dropped []string }

func (f *fakeDropper) DropPage(u *url.URL) { f.dropped = append(f.dropped, u.String()) }

type fakeDeleter struct {
	deleted []string
	err     error
}

func (f *fakeDeleter) DeletePage(k string) error {
	f.deleted = append(f.deleted, k)
	return f.err
}

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	require.NoError(t, err)
	return u
}

func TestRetireList_PendingSkipsClaimedAndDuplicates(t *testing.T) {
	var r retireList
	r.add(mustURL(t, "https://example.com/docs/a"), retireGone)
	r.add(mustURL(t, "https://example.com/docs/a"), retireGone)
	r.add(mustURL(t, "https://example.com/docs/saved"), retireGone)

	got := r.pending(keys("https://example.com/docs/saved"))
	require.Len(t, got, 1)
	assert.Equal(t, "https://example.com/docs/a", got[0].u.String())
}

func TestApplyRetirements_DropsRecordAndKey(t *testing.T) {
	out, store := &fakeDropper{}, &fakeDeleter{}
	entries := []retiredPage{{u: mustURL(t, "https://example.com/docs/a"), reason: retireGone}}

	retired, removed := applyRetirements(entries, nil, out, store, silentLogger())

	assert.Equal(t, 1, removed)
	assert.Equal(t, []string{"https://example.com/docs/a"}, out.dropped)
	assert.Equal(t, []string{"https://example.com/docs/a"}, store.deleted)
	assert.Contains(t, retired, "https://example.com/docs/a")
}

func TestApplyRetirements_DeleteFailureOnlyWarns(t *testing.T) {
	out, store := &fakeDropper{}, &fakeDeleter{err: errors.New("disk")}
	entries := []retiredPage{{u: mustURL(t, "https://example.com/docs/a"), reason: retireGone}}

	retired, _ := applyRetirements(entries, nil, out, store, silentLogger())

	assert.Len(t, out.dropped, 1, "the record still goes")
	assert.Contains(t, retired, "https://example.com/docs/a")
}

func TestRetireReason(t *testing.T) {
	for _, tc := range []struct {
		err  error
		gone bool
		want string
	}{
		{errors.New("404"), true, retireGone},
		{fmt.Errorf("x: %w", utils.ErrRobotsDisallowed), false, retireRobots},
		{fmt.Errorf("x: %w", utils.ErrScopeViolation), false, retireScope},
		{fmt.Errorf("x: %w", utils.ErrNonHTMLContent), false, retireNonHTML},
		{utils.ErrMaxDepthExceeded, false, retireMaxDepth},
		{fmt.Errorf("x: %w", utils.ErrBlockedAddress), false, ""},
		{fmt.Errorf("x: %w", utils.ErrRetryFailed), false, ""},
		{fmt.Errorf("x: %w", utils.ErrContentSelector), false, ""},
		{context.DeadlineExceeded, false, ""},
		{errors.New("other"), false, ""},
	} {
		assert.Equal(t, tc.want, retireReason(tc.err, tc.gone), "%v", tc.err)
	}
}

func TestDeletesVisitedKey(t *testing.T) {
	for _, r := range []string{retireGone, retireRobots, retireScope, retireNonHTML, retireMaxDepth} {
		assert.True(t, deletesVisitedKey(r), r)
	}
	for _, r := range []string{retireNoindex, retireRedirected} {
		assert.False(t, deletesVisitedKey(r), r)
	}
}

func TestApplyRetirements_KeepsKeyForRedirectAndNoindex(t *testing.T) {
	out, store := &fakeDropper{}, &fakeDeleter{}
	entries := []retiredPage{
		{u: mustURL(t, "https://example.com/docs/r"), reason: retireRedirected},
		{u: mustURL(t, "https://example.com/docs/n"), reason: retireNoindex},
		{u: mustURL(t, "https://example.com/docs/b"), reason: retireRobots},
	}
	_, removed := applyRetirements(entries, keys("https://example.com/docs/r"), out, store, silentLogger())
	assert.Equal(t, 1, removed, "only pages the corpus held count as removed")
	assert.Len(t, out.dropped, 3)
	assert.Equal(t, []string{"https://example.com/docs/b"}, store.deleted)
}

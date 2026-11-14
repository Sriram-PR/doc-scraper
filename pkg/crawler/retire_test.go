package crawler

import (
	"errors"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	retired := applyRetirements(entries, nil, out, store, func(string) bool { return true }, silentLogger())

	assert.Equal(t, []string{"https://example.com/docs/a"}, out.dropped)
	assert.Equal(t, []string{"https://example.com/docs/a"}, store.deleted)
	assert.Contains(t, retired, "https://example.com/docs/a")
}

func TestApplyRetirements_DeleteFailureOnlyWarns(t *testing.T) {
	out, store := &fakeDropper{}, &fakeDeleter{err: errors.New("disk")}
	entries := []retiredPage{{u: mustURL(t, "https://example.com/docs/a"), reason: retireGone}}

	retired := applyRetirements(entries, nil, out, store, func(string) bool { return true }, silentLogger())

	assert.Len(t, out.dropped, 1, "the record still goes")
	assert.Contains(t, retired, "https://example.com/docs/a")
}

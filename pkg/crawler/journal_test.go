package crawler

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/storage/index"
)

func TestCommitJournal_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := journalPath(dir, "my/site")
	assert.Equal(t, filepath.Join(dir, "my_site_commit.json"), p)

	got, err := readJournal(p)
	require.NoError(t, err)
	assert.Nil(t, got, "no journal is not an error")

	t0 := time.Date(2026, 11, 14, 1, 2, 3, 456789, time.UTC)
	want := commitJournal{Version: 1, Output: "/o", OutputStaging: "/o.staging", VisitedDB: "/d", VisitedDBStaging: "/d.staging",
		CrawlStartedAt: t0, CrawlEndedAt: t0.Add(time.Minute), Mode: index.ModeFull}
	require.NoError(t, writeJournal(p, want))
	got, err = readJournal(p)
	require.NoError(t, err)
	assert.Equal(t, &want, got)

	require.NoError(t, removeJournal(p))
	assert.NoFileExists(t, p)
	require.NoError(t, removeJournal(p), "removing a missing journal is fine")
}

func TestCommitJournal_UnknownVersionOrGarbage(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x_commit.json")
	require.NoError(t, os.WriteFile(p, []byte(`{"version":2}`), 0o644))
	_, err := readJournal(p)
	require.ErrorContains(t, err, "version 2")
	require.NoError(t, os.WriteFile(p, []byte(`{`), 0o644))
	_, err = readJournal(p)
	assert.Error(t, err)
}

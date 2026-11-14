package crawler

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sweepHost = "https://example.com"

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

func pageFile(pageURL string) string {
	return "---\ntitle: T\nurl: " + pageURL + "\ncrawled_at: \"2026-11-14T00:00:00Z\"\ncontent_hash: abc123\ndepth: 1\n---\n\nbody\n"
}

func keys(urls ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(urls))
	for _, u := range urls {
		m[u] = struct{}{}
	}
	return m
}

func storedNone(string) bool { return false }

func TestSweepOrphans(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "docs", "a.md")
	writeFile(t, live, pageFile(sweepHost+"/docs/a"))
	oldName := filepath.Join(dir, "legacy", "v1", "index.md") // (b): a.md is a's file this run
	writeFile(t, oldName, pageFile(sweepHost+"/docs/a"))
	retiredFile := filepath.Join(dir, "docs", "r.md") // (a)
	writeFile(t, retiredFile, pageFile(sweepHost+"/docs/r"))
	leftover := filepath.Join(dir, "docs", "gone.md") // (c)
	writeFile(t, leftover, pageFile(sweepHost+"/docs/gone"))
	kept := filepath.Join(dir, "docs", "k.md") // failed this run, still in the JSONL
	writeFile(t, kept, pageFile(sweepHost+"/docs/k"))
	storedOnly := filepath.Join(dir, "docs", "s.md") // missing from the JSONL, success in the DB
	writeFile(t, storedOnly, pageFile(sweepHost+"/docs/s"))
	notes := filepath.Join(dir, "notes.md")
	writeFile(t, notes, "# my notes\n")
	partial := filepath.Join(dir, "partial.md")
	writeFile(t, partial, "---\ntitle: T\nurl: "+sweepHost+"/docs/gone\n---\n")
	unclosed := filepath.Join(dir, "unclosed.md")
	writeFile(t, unclosed, "---\nurl: "+sweepHost+"/docs/gone\ncrawled_at: x\ncontent_hash: y\n")
	llms := filepath.Join(dir, "llms.txt")
	writeFile(t, llms, pageFile(sweepHost+"/docs/gone"))
	outside := filepath.Join(t.TempDir(), "target.md")
	writeFile(t, outside, pageFile(sweepHost+"/docs/gone"))
	link := filepath.Join(dir, "link.md")
	require.NoError(t, os.Symlink(outside, link))

	n := sweepOrphans(sweepInput{
		dir:     dir,
		retired: keys(sweepHost + "/docs/r"),
		claimed: map[string]string{live: sweepHost + "/docs/a"},
		corpus:  keys(sweepHost+"/docs/a", sweepHost+"/docs/k"),
		stored:  func(u string) bool { return u == sweepHost+"/docs/s" },
	}, silentLogger())

	assert.Equal(t, 3, n)
	for _, p := range []string{oldName, retiredFile, leftover} {
		assert.NoFileExists(t, p)
	}
	for _, p := range []string{live, kept, storedOnly, notes, partial, unclosed, llms, outside, link} {
		_, err := os.Lstat(p)
		assert.NoError(t, err, "%s must stay", p)
	}
	assert.NoDirExists(t, filepath.Join(dir, "legacy"), "directories left empty are removed")
	assert.DirExists(t, dir)
}

func TestSweepOrphans_KeepsAClaimedFileEvenIfItsURLIsRetired(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.md")
	writeFile(t, p, pageFile(sweepHost+"/docs/a"))
	n := sweepOrphans(sweepInput{
		dir:     dir,
		retired: keys(sweepHost + "/docs/a"),
		claimed: map[string]string{p: sweepHost + "/docs/a"},
		corpus:  keys(),
		stored:  storedNone,
	}, silentLogger())
	assert.Zero(t, n)
	assert.FileExists(t, p)
}

func TestSweepOrphans_WithoutJSONLOnlyRetiredAndRenamedGo(t *testing.T) {
	dir := t.TempDir()
	unknown := filepath.Join(dir, "u.md")
	writeFile(t, unknown, pageFile(sweepHost+"/docs/u"))
	retired := filepath.Join(dir, "r.md")
	writeFile(t, retired, pageFile(sweepHost+"/docs/r"))
	n := sweepOrphans(sweepInput{dir: dir, retired: keys(sweepHost + "/docs/r"), stored: storedNone}, silentLogger())
	assert.Equal(t, 1, n)
	assert.FileExists(t, unknown)
	assert.NoFileExists(t, retired)
}

func TestSweepOrphans_MatchesNormalizedURLs(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.md")
	writeFile(t, p, pageFile("HTTPS://Example.com/docs/a/"))
	n := sweepOrphans(sweepInput{dir: dir, retired: keys(sweepHost + "/docs/a"), stored: storedNone}, silentLogger())
	assert.Equal(t, 1, n)
}

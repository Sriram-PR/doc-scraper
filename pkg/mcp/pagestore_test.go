package mcp

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/crawler"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/models"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/parse"
)

// oldFindPageRecord is the scan read_page used before pageStore; Page must
// return what it returns.
func oldFindPageRecord(jsonlPath, wantURL string) (*models.PageJSONL, error) {
	file, err := os.Open(jsonlPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	wantNorm, _, _, normErr := parse.ParseURL(wantURL)

	scanner := crawler.NewJSONLScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.Contains(line, `"record_type":"page"`) {
			continue
		}
		var p models.PageJSONL
		if err := json.Unmarshal([]byte(line), &p); err != nil || p.RecordType != models.RecordTypePage {
			continue
		}
		if p.URL == wantURL {
			return &p, nil
		}
		if normErr == nil {
			if gotNorm, _, _, err := parse.ParseURL(p.URL); err == nil && gotNorm == wantNorm {
				return &p, nil
			}
		}
	}
	return nil, scanner.Err()
}

func pageLine(t *testing.T, url, title string) string {
	t.Helper()
	b, err := json.Marshal(models.PageJSONL{
		RecordType: models.RecordTypePage, URL: url, Title: title,
		Content: "Body of " + title + ".", CrawledAt: "2026-05-23T10:00:00Z",
	})
	require.NoError(t, err)
	return string(b)
}

func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644))
}

// setModTime gives path the mod time of info, so only content or identity differ.
func setModTime(t *testing.T, path string, info os.FileInfo) {
	t.Helper()
	require.NoError(t, os.Chtimes(path, time.Time{}, info.ModTime()))
}

func lookupFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pages.jsonl")
	writeLines(t, path,
		pageLine(t, "https://d.example/a", "A1"),
		pageLine(t, "https://d.example/b/", "B"),
		pageLine(t, "https://d.example/a", "A2"),
		`{"record_type":"crawl_meta","site_key":"s"}`,
		"",
		`{"record_type":"page","url":"https://d.example/broken"`,
		pageLine(t, "https://d.example/x/", "X-slash"),
		pageLine(t, "https://d.example/x", "X-exact"),
		pageLine(t, "https://d.example/y", "Y-exact"),
		pageLine(t, "https://d.example/y/", "Y-slash"),
		pageLine(t, "/relative", "R1"),
		pageLine(t, "/relative", "R2"),
	)
	return path
}

func TestPageStorePage_MatchesTheOldScan(t *testing.T) {
	path := lookupFixture(t)
	var ps pageStore
	for _, u := range []string{
		"https://d.example/a",
		"https://D.example/a#top",
		"https://d.example/b",
		"https://d.example/b/",
		"https://d.example/x",
		"https://d.example/y",
		"https://d.example/broken",
		"https://d.example/none",
		"/relative",
		"not a url",
	} {
		want, err := oldFindPageRecord(path, u)
		require.NoError(t, err)
		got, err := ps.Page(path, u)
		require.NoError(t, err, u)
		assert.Equal(t, want, got, u)
	}
	got, err := ps.Page(path, "https://d.example/x")
	require.NoError(t, err)
	assert.Equal(t, "X-slash", got.Title, "an earlier normalized match wins over a later exact one")
}

func TestPageStorePage_MissingFile(t *testing.T) {
	var ps pageStore
	_, err := ps.Page(filepath.Join(t.TempDir(), "pages.jsonl"), "https://d.example/a")
	assert.ErrorIs(t, err, fs.ErrNotExist)
}

func TestPageStorePage_SeesARenameSwap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pages.jsonl")
	writeLines(t, path, pageLine(t, "https://d.example/a", "old"))
	var ps pageStore
	got, err := ps.Page(path, "https://d.example/a")
	require.NoError(t, err)
	require.Equal(t, "old", got.Title)

	info, err := os.Stat(path)
	require.NoError(t, err)
	next := filepath.Join(dir, "pages.jsonl.tmp")
	writeLines(t, next, pageLine(t, "https://d.example/a", "new"))
	setModTime(t, next, info)
	require.NoError(t, os.Rename(next, path))

	got, err = ps.Page(path, "https://d.example/a")
	require.NoError(t, err)
	assert.Equal(t, "new", got.Title)
	list, err := ps.List(path)
	require.NoError(t, err)
	assert.Equal(t, "new", list[0].Title, "same size and mod time: only the file identity changed")
}

func TestPageStorePage_SeesAnAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pages.jsonl")
	writeLines(t, path, pageLine(t, "https://d.example/a", "A"))
	var ps pageStore
	got, err := ps.Page(path, "https://d.example/b")
	require.NoError(t, err)
	require.Nil(t, got)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString(pageLine(t, "https://d.example/b", "B") + "\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	got, err = ps.Page(path, "https://d.example/b")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "B", got.Title)
}

func TestPageStorePage_RecoversFromAnInPlaceChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pages.jsonl")
	writeLines(t, path, pageLine(t, "https://d.example/a", "A1"), pageLine(t, "https://d.example/b", "B1"))
	var ps pageStore
	_, err := ps.Page(path, "https://d.example/a")
	require.NoError(t, err)

	info, err := os.Stat(path)
	require.NoError(t, err)
	writeLines(t, path, pageLine(t, "https://d.example/b", "B2"), pageLine(t, "https://d.example/a", "A2"))
	setModTime(t, path, info)

	want, err := oldFindPageRecord(path, "https://d.example/a")
	require.NoError(t, err)
	got, err := ps.Page(path, "https://d.example/a")
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Equal(t, "A2", got.Title)
}

func TestPageStorePage_ConcurrentFirstReads(t *testing.T) {
	path := lookupFixture(t)
	var ps pageStore
	var wg sync.WaitGroup
	titles := make([]string, 8)
	for i := range titles {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if p, err := ps.Page(path, "https://d.example/a"); err == nil && p != nil {
				titles[i] = p.Title
			}
		}()
	}
	wg.Wait()
	for _, title := range titles {
		assert.Equal(t, "A1", title)
	}
}

// oldListPages is the scan list_pages used before pageStore.
func oldListPages(t *testing.T, jsonlPath string) []pageListEntry {
	t.Helper()
	file, err := os.Open(jsonlPath)
	require.NoError(t, err)
	defer file.Close()

	pages := make([]pageListEntry, 0, 256)
	scanner := crawler.NewJSONLScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.Contains(line, `"record_type":"page"`) {
			continue
		}
		var p models.PageJSONL
		if err := json.Unmarshal([]byte(line), &p); err != nil || p.RecordType != models.RecordTypePage {
			continue
		}
		pages = append(pages, pageListEntry{
			URL:           p.URL,
			Title:         p.Title,
			Depth:         p.Depth,
			CrawledAt:     utcTimestamp(p.CrawledAt),
			ContentLength: len(p.Content),
		})
	}
	require.NoError(t, scanner.Err())
	return pages
}

func TestPageStoreList_MatchesTheOldScan(t *testing.T) {
	path := lookupFixture(t)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	b, err := json.Marshal(models.PageJSONL{
		RecordType: models.RecordTypePage, URL: "https://d.example/0", Title: "Zero",
		Content: "héllo", CrawledAt: "2026-05-23T12:00:00+02:00", Depth: 3,
	})
	require.NoError(t, err)
	_, err = f.Write(append(b, '\n'))
	require.NoError(t, err)
	require.NoError(t, f.Close())

	var ps pageStore
	got, err := ps.List(path)
	require.NoError(t, err)
	assert.ElementsMatch(t, oldListPages(t, path), got)
	assert.Len(t, got, 10, "duplicate URLs are kept")
	assert.True(t, sort.SliceIsSorted(got, func(i, j int) bool { return got[i].URL < got[j].URL }))
	assert.Contains(t, got, pageListEntry{
		URL: "https://d.example/0", Title: "Zero", Depth: 3,
		CrawledAt: "2026-05-23T10:00:00Z", ContentLength: len("héllo"),
	})
}

func TestPageStoreList_MissingFile(t *testing.T) {
	var ps pageStore
	_, err := ps.List(filepath.Join(t.TempDir(), "pages.jsonl"))
	assert.ErrorIs(t, err, fs.ErrNotExist)
}

func TestPageStoreList_ReusesTheCatalog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pages.jsonl")
	writeLines(t, path, pageLine(t, "https://d.example/a", "old"))
	var ps pageStore
	_, err := ps.List(path)
	require.NoError(t, err)

	info, err := os.Stat(path)
	require.NoError(t, err)
	writeLines(t, path, pageLine(t, "https://d.example/a", "new"))
	setModTime(t, path, info)

	got, err := ps.List(path)
	require.NoError(t, err)
	assert.Equal(t, "old", got[0].Title, "same file, size and mod time: no rescan")
}

func TestPageStoreList_SeesANewModTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pages.jsonl")
	writeLines(t, path, pageLine(t, "https://d.example/a", "old"))
	var ps pageStore
	_, err := ps.List(path)
	require.NoError(t, err)

	info, err := os.Stat(path)
	require.NoError(t, err)
	writeLines(t, path, pageLine(t, "https://d.example/a", "new"))
	require.NoError(t, os.Chtimes(path, time.Time{}, info.ModTime().Add(time.Second)))

	got, err := ps.List(path)
	require.NoError(t, err)
	assert.Equal(t, "new", got[0].Title)
}

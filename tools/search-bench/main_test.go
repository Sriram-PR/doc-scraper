package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runBench(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	full := append([]string{"-queries", "testdata/queries", "-corpora", "testdata"}, args...)
	err := run(context.Background(), full, &stdout, &stderr)
	return stdout.String(), err
}

func TestRun_ScorecardAndRunFile(t *testing.T) {
	out := filepath.Join(t.TempDir(), "runs", "r.json")
	stdout, err := runBench(t, "-out", out)
	require.NoError(t, err)
	assert.Contains(t, stdout, "site:mini")
	assert.Contains(t, stdout, "tag:mismatch")
	assert.Contains(t, stdout, `mini mini-03 "uninstall" -> (no results)`)

	r, err := readRun(out)
	require.NoError(t, err)
	assert.Equal(t, runFileVersion, r.Version)
	require.Len(t, r.Corpora, 1)
	assert.Equal(t, "mini", r.Corpora[0].Site)
	assert.Equal(t, 2, r.Corpora[0].Fingerprint.Pages)
	require.Len(t, r.Queries, 3)
	byID := map[string]queryResult{}
	for _, q := range r.Queries {
		byID[q.ID] = q
	}
	assert.Equal(t, 1, byID["mini-01"].PageRank)
	assert.Equal(t, []string{"keyword"}, byID["mini-01"].Tags)
	assert.True(t, byID["mini-03"].Zero)
}

func TestRun_RunFileRoundTrip(t *testing.T) {
	want := runFile{
		Version: runFileVersion, Created: "2026-10-17T00:00:00Z", GitCommit: "abc",
		Corpora: []corpusInfo{{Site: "s", Fingerprint: fingerprint{Pages: 2, Chunks: 3, SHA256: "ff"}}},
		Queries: []queryResult{{Site: "s", ID: "1", Query: "q", Tags: []string{"api"}, PageRank: 2, SectionRank: 4,
			HasSection: true, ChunkRank: 3, TopURLs: []string{"u"}}},
	}
	path := filepath.Join(t.TempDir(), "r.json")
	require.NoError(t, writeRun(path, want))
	got, err := readRun(path)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestReadRun_RejectsOtherVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"version":2}`), 0o644))
	_, err := readRun(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "version 2")
}

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		path := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	}
	return dir
}

func TestRun_Errors(t *testing.T) {
	pages, err := os.ReadFile("testdata/mini/pages.jsonl")
	require.NoError(t, err)
	cases := map[string]struct {
		files map[string]string
		args  []string
		want  string
	}{
		"stale expected url": {
			files: map[string]string{
				"q/mini.json":        `{"site_key":"mini","queries":[{"id":"a","query":"install","tags":["keyword"],"expected":[{"url":"https://mini.example/gone"}]}]}`,
				"c/mini/pages.jsonl": string(pages),
			},
			want: "a: https://mini.example/gone",
		},
		"no searchable terms": {
			files: map[string]string{
				"q/mini.json":        `{"site_key":"mini","queries":[{"id":"a","query":"???","tags":["keyword"],"expected":[{"url":"https://mini.example/install"}]}]}`,
				"c/mini/pages.jsonl": string(pages),
			},
			want: "no searchable terms",
		},
		"missing corpus": {
			files: map[string]string{
				"q/mini.json": `{"site_key":"mini","queries":[{"id":"a","query":"install","tags":["keyword"],"expected":[{"url":"u"}]}]}`,
			},
			want: "corpus mini",
		},
		"unknown site": {
			files: map[string]string{
				"q/mini.json":        `{"site_key":"mini","queries":[{"id":"a","query":"install","tags":["keyword"],"expected":[{"url":"https://mini.example/install"}]}]}`,
				"c/mini/pages.jsonl": string(pages),
			},
			args: []string{"-site", "other"},
			want: `no query file for site "other"`,
		},
		"duplicate site_key": {
			files: map[string]string{
				"q/a.json":           `{"site_key":"mini","queries":[{"id":"a","query":"install","tags":["keyword"],"expected":[{"url":"https://mini.example/install"}]}]}`,
				"q/b.json":           `{"site_key":"mini","queries":[{"id":"b","query":"install","tags":["keyword"],"expected":[{"url":"https://mini.example/install"}]}]}`,
				"c/mini/pages.jsonl": string(pages),
			},
			want: `site_key "mini" is also used by`,
		},
		"no query files": {
			files: map[string]string{"q/readme.txt": "x"},
			want:  "no query files",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := writeTree(t, tc.files)
			args := append([]string{"-queries", filepath.Join(dir, "q"), "-corpora", filepath.Join(dir, "c")}, tc.args...)
			var stdout, stderr bytes.Buffer
			err := run(context.Background(), args, &stdout, &stderr)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestRun_BaselineAgainstItself(t *testing.T) {
	out := filepath.Join(t.TempDir(), "r.json")
	_, err := runBench(t, "-out", out)
	require.NoError(t, err)
	stdout, err := runBench(t, "-baseline", out)
	require.NoError(t, err)
	assert.Contains(t, stdout, "compared 3 queries")
	assert.NotContains(t, stdout, "WARNING")
	assert.Contains(t, stdout, "1.0000")
}

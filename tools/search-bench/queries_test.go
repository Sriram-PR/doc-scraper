package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeQueryFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "q.json")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	return path
}

func TestLoadQueries_Valid(t *testing.T) {
	path := writeQueryFile(t, `{"site_key":"s","queries":[
		{"id":"s-01","query":"retry backoff","tags":["keyword","drafted"],
		 "expected":[{"url":"https://x.example/a"},{"url":"https://x.example/b","anchor":"setup"}]}]}`)
	qs, err := loadQueries(path)
	require.NoError(t, err)
	assert.Equal(t, "s", qs.SiteKey)
	require.Len(t, qs.Queries, 1)
	assert.Equal(t, []target{{URL: "https://x.example/a"}, {URL: "https://x.example/b", Anchor: "setup"}}, qs.Queries[0].Expected)
}

func TestLoadQueries_Rejections(t *testing.T) {
	cases := map[string]struct {
		body string
		want string
	}{
		"bad json":      {`{"site_key":`, "unexpected EOF"},
		"unknown field": {`{"site_key":"s","extra":1,"queries":[]}`, `unknown field "extra"`},
		"empty site":    {`{"site_key":"","queries":[{"id":"a","query":"q","tags":["api"],"expected":[{"url":"u"}]}]}`, "site_key is empty"},
		"no queries":    {`{"site_key":"s","queries":[]}`, "no queries"},
		"empty id":      {`{"site_key":"s","queries":[{"id":"","query":"q","tags":["api"],"expected":[{"url":"u"}]}]}`, "queries[0]: id is empty"},
		"duplicate id": {`{"site_key":"s","queries":[
			{"id":"a","query":"q","tags":["api"],"expected":[{"url":"u"}]},
			{"id":"a","query":"r","tags":["api"],"expected":[{"url":"u"}]}]}`, "a: duplicate id"},
		"empty query":    {`{"site_key":"s","queries":[{"id":"a","query":"  ","tags":["api"],"expected":[{"url":"u"}]}]}`, "a: query is empty"},
		"unknown tag":    {`{"site_key":"s","queries":[{"id":"a","query":"q","tags":["api","hard"],"expected":[{"url":"u"}]}]}`, `a: unknown tag "hard"`},
		"no type tag":    {`{"site_key":"s","queries":[{"id":"a","query":"q","tags":["drafted"],"expected":[{"url":"u"}]}]}`, "has 0"},
		"two type tags":  {`{"site_key":"s","queries":[{"id":"a","query":"q","tags":["api","keyword"],"expected":[{"url":"u"}]}]}`, "has 2"},
		"empty expected": {`{"site_key":"s","queries":[{"id":"a","query":"q","tags":["api"],"expected":[]}]}`, "a: expected is empty"},
		"empty url":      {`{"site_key":"s","queries":[{"id":"a","query":"q","tags":["api"],"expected":[{"url":""}]}]}`, "a: expected url is empty"},
		"fragment":       {`{"site_key":"s","queries":[{"id":"a","query":"q","tags":["api"],"expected":[{"url":"u#x"}]}]}`, "has a fragment"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := loadQueries(writeQueryFile(t, tc.body))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestLoadQueries_ReportsAllProblems(t *testing.T) {
	_, err := loadQueries(writeQueryFile(t, `{"site_key":"s","queries":[
		{"id":"a","query":"","tags":["api"],"expected":[{"url":"u"}]},
		{"id":"b","query":"q","tags":[],"expected":[{"url":"u"}]}]}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "a: query is empty")
	assert.Contains(t, err.Error(), "b: needs exactly one")
}

func TestValidate_ListsEveryMissingURL(t *testing.T) {
	qs := querySet{SiteKey: "s", Queries: []query{
		{ID: "a", Expected: []target{{URL: "https://x.example/a"}, {URL: "https://x.example/gone1"}}},
		{ID: "b", Expected: []target{{URL: "https://x.example/gone2"}}},
	}}
	pages := map[string]struct{}{"https://x.example/a": {}}
	err := qs.validate(pages)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "2 expected URLs")
	assert.Contains(t, err.Error(), "a: https://x.example/gone1")
	assert.Contains(t, err.Error(), "b: https://x.example/gone2")

	pages["https://x.example/gone1"] = struct{}{}
	pages["https://x.example/gone2"] = struct{}{}
	assert.NoError(t, qs.validate(pages))
}

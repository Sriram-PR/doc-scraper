package config

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mergeBase() SiteConfig {
	return SiteConfig{
		StartURLs:       []string{"https://docs.example.com/"},
		AllowedDomain:   "docs.example.com",
		ContentSelector: "main",
		MaxDepth:        3,
	}
}

func TestMergeSiteFields_SetsByYAMLNameAndKeepsTheRest(t *testing.T) {
	base := mergeBase()
	got, err := MergeSiteFields(base, map[string]any{
		"content_selector":         "article",
		"max_depth":                float64(5),
		"disallowed_path_patterns": []any{"^/v1/"},
		"delay_per_host":           "2s",
		"respect_noindex":          false,
	})
	require.NoError(t, err)
	assert.Equal(t, "article", got.ContentSelector)
	assert.Equal(t, 5, got.MaxDepth)
	assert.Equal(t, []string{"^/v1/"}, got.DisallowedPathPatterns)
	assert.Equal(t, 2*time.Second, got.DelayPerHost)
	require.NotNil(t, got.RespectNoindex)
	assert.False(t, *got.RespectNoindex)
	assert.Equal(t, []string{"https://docs.example.com/"}, got.StartURLs)
	assert.Equal(t, "docs.example.com", got.AllowedDomain)
	assert.Equal(t, "main", base.ContentSelector, "base is not modified")
}

func TestMergeSiteFields_DoesNotWriteThroughBasePointers(t *testing.T) {
	base := mergeBase()
	respect := true
	base.RespectNoindex = &respect
	got, err := MergeSiteFields(base, map[string]any{"respect_noindex": false})
	require.NoError(t, err)
	assert.True(t, *base.RespectNoindex)
	assert.False(t, *got.RespectNoindex)
}

func TestMergeSiteFields_Rejects(t *testing.T) {
	cases := []struct {
		name   string
		fields map[string]any
		want   []string
	}{
		{"unknown field", map[string]any{"content_selectr": "x"}, []string{`"content_selectr"`, "content_selector"}},
		{"jsonl switch", map[string]any{"enable_jsonl_output": false}, []string{`"enable_jsonl_output"`}},
		{"jsonl filename", map[string]any{"jsonl_output_filename": "x.jsonl"}, []string{`"jsonl_output_filename"`}},
		{"string for an integer", map[string]any{"max_depth": "four"}, []string{`"max_depth"`}},
		{"fraction for an integer", map[string]any{"max_depth": 4.5}, []string{`"max_depth"`, "integer"}},
		{"number for a duration", map[string]any{"delay_per_host": float64(2)}, []string{`"delay_per_host"`, "duration"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := MergeSiteFields(mergeBase(), tc.fields)
			require.Error(t, err)
			for _, w := range tc.want {
				assert.Contains(t, err.Error(), w)
			}
		})
	}
}

func TestSiteFieldMap_RoundTripsThroughJSON(t *testing.T) {
	noindex := false
	maxImg := int64(1_000_000)
	want := SiteConfig{
		StartURLs:              []string{"https://docs.example.com/guide/"},
		AllowedDomain:          "docs.example.com",
		AllowedPathPrefix:      "/guide/",
		ContentSelector:        "article",
		DisallowedPathPatterns: []string{"^/guide/v1/"},
		RespectNoindex:         &noindex,
		DelayPerHost:           1500 * time.Millisecond,
		MaxDepth:               4,
		MaxImageSizeBytes:      &maxImg,
	}
	m, err := SiteFieldMap(&want)
	require.NoError(t, err)
	raw, err := json.Marshal(m)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	assert.Equal(t, "1.5s", decoded["delay_per_host"])

	got, err := MergeSiteFields(SiteConfig{}, decoded)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestChangedSiteFields(t *testing.T) {
	a := mergeBase()
	b := mergeBase()
	b.ContentSelector = "article"
	b.MaxDepth = 1
	assert.Equal(t, []string{"content_selector", "max_depth"}, ChangedSiteFields(a, b))
	assert.Equal(t, []string{"allowed_domain", "content_selector", "max_depth", "start_urls"}, ChangedSiteFields(SiteConfig{}, a))
	assert.Empty(t, ChangedSiteFields(a, mergeBase()))
}

func TestSiteFieldMap_OmitsRefusedFields(t *testing.T) {
	on := true
	site := mergeBase()
	site.EnableJSONLOutput = &on
	site.JSONLOutputFilename = "custom.jsonl"
	m, err := SiteFieldMap(&site)
	require.NoError(t, err)
	assert.NotContains(t, m, "enable_jsonl_output")
	assert.NotContains(t, m, "jsonl_output_filename")
	assert.Equal(t, "main", m["content_selector"])
}

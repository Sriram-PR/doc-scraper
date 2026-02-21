package models

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestPageDBEntry_JSONRoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Second).UTC()
	entry := PageDBEntry{
		Status:      PageStatusSuccess,
		ErrorType:   "timeout",
		ProcessedAt: now,
		LastAttempt: now,
		Depth:       3,
		ContentHash: "abc123",
	}

	data, err := json.Marshal(entry)
	require.NoError(t, err)

	var got PageDBEntry
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, entry, got)
}

func TestPageDBEntry_OmitEmpty(t *testing.T) {
	entry := PageDBEntry{
		Status:      PageStatusPending,
		LastAttempt: time.Now().UTC(),
	}

	data, err := json.Marshal(entry)
	require.NoError(t, err)

	raw := string(data)
	assert.NotContains(t, raw, "error_type")
	assert.NotContains(t, raw, "content_hash")
}

func TestImageDBEntry_JSONRoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Second).UTC()
	entry := ImageDBEntry{
		Status:      ImageStatusSuccess,
		LocalPath:   "images/logo.png",
		Caption:     "Site logo",
		LastAttempt: now,
	}

	data, err := json.Marshal(entry)
	require.NoError(t, err)

	var got ImageDBEntry
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, entry, got)
}

func TestImageDBEntry_OmitEmpty(t *testing.T) {
	entry := ImageDBEntry{
		Status:      ImageStatusPending,
		LastAttempt: time.Now().UTC(),
	}

	data, err := json.Marshal(entry)
	require.NoError(t, err)

	raw := string(data)
	assert.NotContains(t, raw, "local_path")
	assert.NotContains(t, raw, "caption")
	assert.NotContains(t, raw, "error_type")
}

func TestPageJSONL_JSONRoundTrip(t *testing.T) {
	entry := PageJSONL{
		URL:         "https://example.com",
		Title:       "Example",
		Content:     "Hello world",
		Headings:    []string{"H1"},
		Links:       []string{"https://example.com/about"},
		Images:      []string{"logo.png"},
		ContentHash: "deadbeef",
		CrawledAt:   "2025-01-01T00:00:00Z",
		Depth:       1,
		TokenCount:  42,
	}

	data, err := json.Marshal(entry)
	require.NoError(t, err)

	var got PageJSONL
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, entry, got)
}

func TestChunkJSONL_JSONRoundTrip(t *testing.T) {
	entry := ChunkJSONL{
		URL:              "https://example.com",
		ChunkIndex:       2,
		Content:          "chunk content",
		HeadingHierarchy: []string{"H1", "H2"},
		TokenCount:       10,
		PageTitle:        "Example",
		CrawledAt:        "2025-01-01T00:00:00Z",
	}

	data, err := json.Marshal(entry)
	require.NoError(t, err)

	var got ChunkJSONL
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, entry, got)
}

func TestCrawlMetadata_YAMLRoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Second).UTC()
	meta := CrawlMetadata{
		SiteKey:         "example",
		AllowedDomain:   "example.com",
		CrawlStartTime:  now,
		CrawlEndTime:    now.Add(time.Minute),
		TotalPagesSaved: 5,
		Pages: []PageMetadata{
			{
				OriginalURL:   "https://example.com",
				NormalizedURL: "example.com",
				LocalFilePath: "index.md",
				Depth:         0,
				ProcessedAt:   now,
			},
		},
	}

	data, err := yaml.Marshal(meta)
	require.NoError(t, err)

	var got CrawlMetadata
	require.NoError(t, yaml.Unmarshal(data, &got))
	assert.Equal(t, meta, got)
}

func TestCrawlMetadata_OmitEmpty(t *testing.T) {
	meta := CrawlMetadata{
		SiteKey: "test",
	}

	data, err := yaml.Marshal(meta)
	require.NoError(t, err)

	raw := string(data)
	assert.NotContains(t, raw, "site_configuration")
}

func TestPageMetadata_YAMLRoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Second).UTC()
	page := PageMetadata{
		OriginalURL:   "https://example.com/page",
		NormalizedURL: "example.com/page",
		LocalFilePath: "page.md",
		Title:         "A Page",
		Depth:         2,
		ProcessedAt:   now,
		ContentHash:   "abc123",
		ImageCount:    3,
		TokenCount:    100,
	}

	data, err := yaml.Marshal(page)
	require.NoError(t, err)

	var got PageMetadata
	require.NoError(t, yaml.Unmarshal(data, &got))
	assert.Equal(t, page, got)
}

func TestPageMetadata_OmitEmpty(t *testing.T) {
	page := PageMetadata{
		OriginalURL:   "https://example.com",
		NormalizedURL: "example.com",
		LocalFilePath: "index.md",
		Depth:         0,
		ProcessedAt:   time.Now().UTC(),
	}

	data, err := yaml.Marshal(page)
	require.NoError(t, err)

	raw := string(data)
	assert.NotContains(t, raw, "title")
	assert.NotContains(t, raw, "content_hash")
	assert.NotContains(t, raw, "image_count")
	assert.NotContains(t, raw, "token_count")
}

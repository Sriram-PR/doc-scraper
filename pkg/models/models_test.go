package models

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
		RecordType:  RecordTypePage,
		URL:         "https://example.com",
		Title:       "Example",
		Content:     "Hello world",
		Headings:    []string{"H1"},
		Links:       []string{"https://example.com/about"},
		Images:      []string{"logo.png"},
		ContentHash: "deadbeef",
		CrawledAt:   "2025-01-01T00:00:00Z",
		Depth:       1,
	}

	data, err := json.Marshal(entry)
	require.NoError(t, err)

	var got PageJSONL
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, entry, got)
}

func TestCrawlMetaJSONL_JSONRoundTrip(t *testing.T) {
	entry := CrawlMetaJSONL{
		RecordType:     RecordTypeCrawlMeta,
		SiteKey:        "example",
		AllowedDomain:  "example.com",
		CrawlStartedAt: "2025-01-01T00:00:00Z",
		CrawlEndedAt:   "2025-01-01T00:05:00Z",
		TotalPages:     42,
	}

	data, err := json.Marshal(entry)
	require.NoError(t, err)

	var got CrawlMetaJSONL
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, entry, got)
}

// TestJSONLRecordTypeDiscriminator confirms a crawl_meta line and a page line
// can be told apart by record_type after being marshaled into the same stream.
func TestJSONLRecordTypeDiscriminator(t *testing.T) {
	pageData, err := json.Marshal(PageJSONL{RecordType: RecordTypePage, URL: "https://example.com"})
	require.NoError(t, err)
	metaData, err := json.Marshal(CrawlMetaJSONL{RecordType: RecordTypeCrawlMeta, SiteKey: "example"})
	require.NoError(t, err)

	var probe struct {
		RecordType string `json:"record_type"`
	}
	require.NoError(t, json.Unmarshal(pageData, &probe))
	assert.Equal(t, RecordTypePage, probe.RecordType)
	require.NoError(t, json.Unmarshal(metaData, &probe))
	assert.Equal(t, RecordTypeCrawlMeta, probe.RecordType)
}

package mcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/pkg/models"
)

func TestExtractSnippet(t *testing.T) {
	tests := []struct {
		name    string
		content string
		query   string
		maxLen  int
		wantHas string // substring that must appear
		wantPfx string // expected prefix (if any)
		wantSfx string // expected suffix (if any)
	}{
		{
			name:    "match in middle with ellipsis",
			content: "The quick brown fox jumps over the lazy dog and then keeps running forever",
			query:   "jumps",
			maxLen:  20,
			wantHas: "jumps",
			wantPfx: "...",
			wantSfx: "...",
		},
		{
			name:    "match at start",
			content: "Hello world this is a test",
			query:   "Hello",
			maxLen:  20,
			wantHas: "Hello",
		},
		{
			name:    "match at end",
			content: "This is a very long string that ends with target",
			query:   "target",
			maxLen:  20,
			wantHas: "target",
		},
		{
			name:    "no match truncated beginning",
			content: "abcdefghijklmnopqrstuvwxyz",
			query:   "zzz",
			maxLen:  10,
			wantHas: "abcdefghij",
			wantSfx: "...",
		},
		{
			name:    "short content returned as-is",
			content: "hi",
			query:   "missing",
			maxLen:  100,
			wantHas: "hi",
		},
		{
			name:    "empty content",
			content: "",
			query:   "test",
			maxLen:  50,
			wantHas: "",
		},
		{
			name:    "case insensitive",
			content: "The Quick Brown Fox",
			query:   "quick",
			maxLen:  100,
			wantHas: "Quick",
		},
		{
			name:    "unicode safety",
			content: "こんにちは世界、テストです。Unicode文字列のテスト。",
			query:   "テスト",
			maxLen:  15,
			wantHas: "テスト",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractSnippet(tt.content, tt.query, tt.maxLen)
			if tt.wantHas != "" {
				assert.Contains(t, got, tt.wantHas)
			}
			if tt.wantPfx != "" {
				assert.Contains(t, got, tt.wantPfx, "expected prefix ellipsis")
			}
			if tt.wantSfx != "" {
				assert.True(t, len(got) > 0 && got[len(got)-3:] == "...", "expected suffix ellipsis")
			}
		})
	}
}

func TestParseJSONLine(t *testing.T) {
	t.Run("valid JSON", func(t *testing.T) {
		line := `{"url":"https://example.com","title":"Test","content":"Hello","headings":["H1"]}`
		var page models.PageJSONL
		err := parseJSONLine(line, &page)
		require.NoError(t, err)
		assert.Equal(t, "https://example.com", page.URL)
		assert.Equal(t, "Test", page.Title)
		assert.Equal(t, "Hello", page.Content)
		assert.Equal(t, []string{"H1"}, page.Headings)
	})

	t.Run("empty string", func(t *testing.T) {
		var page models.PageJSONL
		err := parseJSONLine("", &page)
		assert.Error(t, err)
	})

	t.Run("invalid JSON", func(t *testing.T) {
		var page models.PageJSONL
		err := parseJSONLine("{not valid json}", &page)
		assert.Error(t, err)
	})

	t.Run("partial fields", func(t *testing.T) {
		line := `{"url":"https://example.com"}`
		var page models.PageJSONL
		err := parseJSONLine(line, &page)
		require.NoError(t, err)
		assert.Equal(t, "https://example.com", page.URL)
		assert.Empty(t, page.Title)
		assert.Empty(t, page.Content)
	})
}

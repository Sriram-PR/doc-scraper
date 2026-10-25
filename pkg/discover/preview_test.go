package discover

import (
	"io"
	"log/slog"
	"net/url"
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
)

func TestExtractPreview(t *testing.T) {
	html := `<html><body><nav><a href="/a">A</a></nav><article><h1>Install</h1><p>` +
		strings.Repeat("Real documentation prose about installing the tool. ", 5) +
		`</p><pre><code>pip install demo</code></pre></article><pre>outside</pre></body></html>`
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	require.NoError(t, err)
	u, _ := url.Parse("https://docs.example.com/install/")

	p := ExtractPreview(doc, u, &config.SiteConfig{ContentSelector: "article"}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	assert.Empty(t, p.Err)
	assert.Equal(t, "https://docs.example.com/install/", p.URL)
	assert.Contains(t, p.Markdown, "pip install demo")
	assert.Equal(t, len(p.Markdown), p.Chars)
	assert.Equal(t, 1, p.CodeBlocksKept)
	assert.Equal(t, 2, p.CodeBlocksSeen)
	assert.Equal(t, 1, p.Headings)
	assert.False(t, p.NavLeak)
	assert.Greater(t, p.PageTextRatio, 0.5)
	assert.Less(t, p.PageTextRatio, 1.0)
}

func TestExtractPreview_SelectorMatchesNothing(t *testing.T) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(`<html><body><main><p>text</p></main></body></html>`))
	require.NoError(t, err)
	u, _ := url.Parse("https://docs.example.com/")
	p := ExtractPreview(doc, u, &config.SiteConfig{ContentSelector: "#nope"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	assert.Contains(t, p.Err, "yielded no content")
	assert.Empty(t, p.Markdown)
}

func TestFirstLines(t *testing.T) {
	assert.Equal(t, "a\nb", FirstLines("a\nb", 2))
	assert.Equal(t, "a\nb\n... (2 more lines)\n", FirstLines("a\nb\nc\nd", 2))
}

func TestHasNavResidue(t *testing.T) {
	prose := strings.Repeat("Real explanatory prose about the feature. ", 4)
	cases := []struct {
		name string
		html string
		want bool
	}{
		{"content callout with a link", `<aside class="note">Note: ` + prose + `<a href="/x">see this</a></aside>`, false},
		{"starlight tip", `<aside class="starlight-aside starlight-aside--tip"><p>Tip</p>` + prose + `</aside>`, false},
		{"nav element", `<nav><a href="/a">A</a><a href="/b">B</a></nav>`, true},
		{"role navigation", `<div role="navigation"><a href="/a">A</a></div>`, true},
		{"link-list sidebar", `<div class="sidebar"><a href="/a">Install</a> <a href="/b">Configure</a> <a href="/c">Deploy</a></div>`, true},
		{"aside wrapping a nav", `<aside class="md-source-file">2025-11-07 <nav><a href="/u">+9</a></nav></aside>`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := goquery.NewDocumentFromReader(strings.NewReader("<html><body><main><p>" + prose + "</p>" + tc.html + "</main></body></html>"))
			require.NoError(t, err)
			assert.Equal(t, tc.want, hasNavResidue(doc.Find("main")))
		})
	}
}

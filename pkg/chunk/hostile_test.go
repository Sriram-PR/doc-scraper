package chunk_test

import (
	"strings"
	"testing"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/chunk"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/process"
)

// goldmark is quadratic on some hand-written Markdown (unclosed links and
// emphasis, link reference definitions, deep nesting). Crawled pages only
// reach the chunker through process.ToMarkdown, which escapes those
// characters, and the HTML parser caps nesting at 512, so these shapes must
// stay linear. A converter change that lets raw Markdown through fails here.
func TestHostileHTMLStaysFastThroughTheChunker(t *testing.T) {
	const n = 100000
	for name, body := range map[string]string{
		"unclosed links":     "<p>" + strings.Repeat("[a](", n) + "</p>",
		"unclosed links h2":  "<h2>" + strings.Repeat("[a](", n) + "</h2>",
		"emphasis runs":      "<p>" + strings.Repeat("*a_", n) + "</p>",
		"link references":    "<p>" + strings.Repeat("[a]: /u<br>", n) + "</p>",
		"angle brackets":     "<p>" + strings.Repeat("&lt;a ", n) + "</p>",
		"links":              "<p>" + strings.Repeat(`<a href="/x">a</a>`, n) + "</p>",
		"nested blockquotes": strings.Repeat(strings.Repeat("<blockquote>", 500)+"<h2>x</h2>"+strings.Repeat("</blockquote>", 500), 20),
	} {
		doc, err := goquery.NewDocumentFromReader(strings.NewReader("<html><body><main>" + body + "</main></body></html>"))
		require.NoError(t, err, name)
		md, err := process.ToMarkdown(doc.Find("main"))
		require.NoError(t, err, name)

		start := time.Now()
		chunk.Split(md)
		chunk.Anchors(md)
		require.Less(t, time.Since(start), 5*time.Second, "%s: %d bytes of Markdown", name, len(md))
	}
}

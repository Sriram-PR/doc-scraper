package detect

import (
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
)

func FuzzDetectPage(f *testing.F) {
	f.Add(`<html><head><meta name="generator" content="Docusaurus v3.1.0"></head><body><article class="theme-doc-markdown">text</article></body></html>`)
	f.Add(`<html><body><div id="app"></div><script>window.$docsify={}</script></body></html>`)
	f.Add(`<meta name="generator" content="` + strings.Repeat("A", 100000) + `">`)
	f.Add(`<script src="_static/doctools.js"></script><div class="body"><section id="x">t</section></div>`)
	f.Add(`<html><body>` + strings.Repeat(`<div class="fern-prose">`, 500) + `</body></html>`)
	f.Add("\x00\xff<html»<meta name=generator content=mkdocs-1.6")
	f.Add(`<d360-article-content></d360-article-content>`)
	f.Fuzz(func(t *testing.T, html string) {
		doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
		if err != nil {
			t.Skip()
		}
		r := DetectPage(doc)
		if r.Framework == "" {
			t.Fatal("empty framework")
		}
		if r.Fallback && r.Selector != "" {
			t.Fatalf("fallback result carries selector %q", r.Selector)
		}
		if !r.Fallback && r.Selector == "" {
			t.Fatal("confirmed result missing selector")
		}
	})
}

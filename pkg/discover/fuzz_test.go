package discover

import (
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"
)

func FuzzParseSitemapStream(f *testing.F) {
	f.Add(`<?xml version="1.0"?><urlset><url><loc>https://a.example/docs/</loc></url></urlset>`)
	f.Add(`<?xml version="1.0"?><sitemapindex><sitemap><loc>https://a.example/s1.xml</loc></sitemap></sitemapindex>`)
	f.Add(`<urlset><url><loc>https://a.example/x</loc></url><url><loc>`)
	f.Add(`<urlset><url><loc></loc></url><notloc/></urlset>`)
	f.Add("\x1f\x8b garbage not really gzip")
	f.Add(`<a><b><c><loc>x</loc></c></b></a>`)
	f.Fuzz(func(t *testing.T, input string) {
		doc := parseSitemapStream(strings.NewReader(input), 100)
		if len(doc.locs) > 100 {
			t.Fatalf("locs %d exceeds cap", len(doc.locs))
		}
		for _, l := range doc.locs {
			if strings.TrimSpace(l) == "" {
				t.Fatal("empty loc emitted")
			}
		}
	})
}

func FuzzParseRobotsLines(f *testing.F) {
	f.Add("User-agent: *\nDisallow: /private/\nSitemap: https://a.example/sitemap.xml\n")
	f.Add("USER-AGENT: GPTBot\nDisallow: /\n")
	f.Add("Content-Signal: ai-train=yes, search=yes, ai-input=no\n")
	f.Add("sitemap:\nsitemap: x\ndisallow\n#comment\nDisallow: /a #trail\n")
	f.Add(strings.Repeat("User-agent: *\n", 1000))
	f.Fuzz(func(t *testing.T, input string) {
		var info RobotsInfo
		parseRobotsLines(input, &info)
		for _, s := range info.Sitemaps {
			if s == "" {
				t.Fatal("empty sitemap entry")
			}
		}
		for _, d := range info.Disallows {
			if !strings.HasPrefix(d, "/") || d == "/" {
				t.Fatalf("disallow %q not a usable path rule", d)
			}
		}
	})
}

func FuzzClusterScope(f *testing.F) {
	f.Add("https://a.example/en/stable/guide/", "https://a.example/en/stable/x/\nhttps://a.example/en/1.0/y/")
	f.Add("https://a.example/", "")
	f.Add("https://a.example/docs/intro.html", "https://a.example/docs/a/\nnot a url\nhttps://other.example/b/")
	f.Add("https://a.example/%2e%2e/x", "https://a.example/\x00/")
	f.Fuzz(func(t *testing.T, seedRaw, pathsBlob string) {
		seed, err := url.Parse(seedRaw)
		if err != nil || seed.Host == "" {
			t.Skip()
		}
		scope := clusterScope(seed, strings.Split(pathsBlob, "\n"))
		if !strings.HasPrefix(scope.Prefix, "/") || !strings.HasSuffix(scope.Prefix, "/") {
			t.Fatalf("prefix %q not /-delimited", scope.Prefix)
		}
		if scope.PrefixCount > scope.TotalCount {
			t.Fatalf("prefix count %d exceeds total %d", scope.PrefixCount, scope.TotalCount)
		}
		if scope.MaxDepth < 3 || scope.MaxDepth > depthCap {
			if scope.MaxDepth != defaultMaxDepth {
				t.Fatalf("max depth %d outside bounds", scope.MaxDepth)
			}
		}
	})
}

func FuzzParseLlmsLinks(f *testing.F) {
	f.Add("# T\n\n- [a](https://a.example/x): note\n- [b](https://b.example/y)\n")
	f.Add("](https://" + strings.Repeat("a", 5000) + ")")
	f.Add(strings.Repeat("x", 2<<20))
	f.Fuzz(func(t *testing.T, input string) {
		for _, l := range parseLlmsLinks(input) {
			if !strings.HasPrefix(l, "http") {
				t.Fatalf("non-http link %q", l)
			}
			if !utf8.ValidString(l) && utf8.ValidString(input) {
				t.Fatalf("invalid utf8 introduced in %q", l)
			}
		}
	})
}

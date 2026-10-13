package discover

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// SitemapInfo carries the URL inventory pulled from the site's sitemap.
type SitemapInfo struct {
	Found      bool
	SitemapURL string
	URLs       []string
	Truncated  bool
	ShardCount int
}

const (
	maxSitemapURLs   = 50000
	maxSitemapShards = 2
	maxSitemapBytes  = 64 << 20
	// Covers /project/ (GitHub Pages) and /group/project/ (GitLab Pages).
	maxSitemapDirDepth = 2
)

// Astro's default integration serves sitemap-index.xml.
var sitemapNames = []string{"sitemap.xml", "sitemap-index.xml", "sitemap_index.xml"}

// fetchSitemap locates the sitemap and flattens it, recursing one level into
// an index. Order: robots.txt directives, the host root, then the seed's
// ancestor directories shallowest first, since project-pages sites publish
// their sitemap under the project path.
func (d *Discoverer) fetchSitemap(ctx context.Context, finalURL *url.URL, robotsSitemaps []string) SitemapInfo {
	base := finalURL.Scheme + "://" + finalURL.Host
	candidates := append([]string{}, robotsSitemaps...)
	for _, dir := range sitemapDirs(finalURL.Path) {
		for _, name := range sitemapNames {
			candidates = append(candidates, base+dir+name)
		}
	}

	seen := map[string]struct{}{}
	for _, c := range candidates {
		if _, dup := seen[c]; dup {
			continue
		}
		seen[c] = struct{}{}
		if info, ok := d.loadSitemap(ctx, c); ok {
			return info
		}
	}
	return SitemapInfo{}
}

// sitemapDirs returns "/" followed by up to maxSitemapDirDepth ancestor
// directories of p. A final segment without a trailing slash is a file.
func sitemapDirs(p string) []string {
	dirs := []string{"/"}
	p = p[:strings.LastIndex(p, "/")+1]
	cur := "/"
	for i, s := range strings.Split(strings.Trim(p, "/"), "/") {
		if s == "" || i >= maxSitemapDirDepth {
			break
		}
		cur += s + "/"
		dirs = append(dirs, cur)
	}
	return dirs
}

func (d *Discoverer) loadSitemap(ctx context.Context, sitemapURL string) (SitemapInfo, bool) {
	doc, ok := d.streamSitemap(ctx, sitemapURL, maxSitemapURLs)
	if !ok || len(doc.locs) == 0 {
		return SitemapInfo{}, false
	}
	info := SitemapInfo{Found: true, SitemapURL: sitemapURL, Truncated: doc.truncated}

	if doc.isIndex {
		info.ShardCount = len(doc.locs)
		if len(doc.locs) > maxSitemapShards {
			info.Truncated = true
		}
		for i, shard := range doc.locs {
			if i >= maxSitemapShards {
				break
			}
			if shardDoc, sok := d.streamSitemap(ctx, shard, maxSitemapURLs-len(info.URLs)); sok && !shardDoc.isIndex {
				info.URLs = append(info.URLs, shardDoc.locs...)
				info.Truncated = info.Truncated || shardDoc.truncated
			}
		}
		return info, true
	}

	info.URLs = doc.locs
	return info, true
}

type sitemapDoc struct {
	isIndex   bool
	locs      []string
	truncated bool
}

func (d *Discoverer) streamSitemap(ctx context.Context, sitemapURL string, maxURLs int) (sitemapDoc, bool) {
	if maxURLs <= 0 {
		return sitemapDoc{truncated: true}, true
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sitemapURL, nil)
	if err != nil {
		return sitemapDoc{}, false
	}
	if d.UserAgent != "" {
		req.Header.Set("User-Agent", d.UserAgent)
	}
	resp, err := d.Client.Do(req)
	if err != nil {
		return sitemapDoc{}, false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return sitemapDoc{}, false
	}

	var reader io.Reader = bufio.NewReader(io.LimitReader(resp.Body, maxSitemapBytes))
	if magic, perr := reader.(*bufio.Reader).Peek(2); perr == nil && len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		zr, zerr := gzip.NewReader(reader)
		if zerr != nil {
			return sitemapDoc{}, false
		}
		defer func() { _ = zr.Close() }()
		reader = zr
	}
	doc := parseSitemapStream(reader, maxURLs)
	return doc, len(doc.locs) > 0 || doc.isIndex
}

// parseSitemapStream reads <loc> values from a urlset or sitemapindex as a
// token stream, so an oversized or truncated document still yields every URL
// that arrived intact instead of failing the whole parse.
func parseSitemapStream(r io.Reader, maxURLs int) sitemapDoc {
	dec := xml.NewDecoder(r)
	var doc sitemapDoc
	var sawRoot, inLoc bool
	var current strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			doc.truncated = !errors.Is(err, io.EOF)
			return doc
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if !sawRoot {
				sawRoot = true
				doc.isIndex = t.Name.Local == "sitemapindex"
			}
			if t.Name.Local == "loc" {
				inLoc = true
				current.Reset()
			}
		case xml.CharData:
			if inLoc {
				current.Write(t)
			}
		case xml.EndElement:
			if t.Name.Local == "loc" && inLoc {
				inLoc = false
				if loc := strings.TrimSpace(current.String()); loc != "" {
					doc.locs = append(doc.locs, loc)
					if len(doc.locs) >= maxURLs {
						doc.truncated = true
						return doc
					}
				}
			}
		}
	}
}

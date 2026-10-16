package parse

import (
	"encoding/xml"
	"net/url"
	"strings"
)

// XMLURL represents a <url> element in a sitemap
type XMLURL struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod,omitempty"`
}

// XMLURLSet represents a <urlset> element in a sitemap
type XMLURLSet struct {
	XMLName xml.Name `xml:"urlset"`
	URLs    []XMLURL `xml:"url"`
}

// XMLSitemap represents a <sitemap> element in a sitemap index file
type XMLSitemap struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod,omitempty"`
}

// XMLSitemapIndex represents a <sitemapindex> element
type XMLSitemapIndex struct {
	XMLName  xml.Name     `xml:"sitemapindex"`
	Sitemaps []XMLSitemap `xml:"sitemap"`
}

// Covers /project/ (GitHub Pages) and /group/project/ (GitLab Pages).
const maxSitemapDirDepth = 2

// Astro's default integration serves sitemap-index.xml.
var sitemapNames = []string{"sitemap.xml", "sitemap-index.xml", "sitemap_index.xml"}

// SitemapCandidates returns the conventional sitemap locations for a page,
// in probe order: the host root, then the page's ancestor directories
// shallowest first, since project-pages sites publish their sitemap under
// the project path rather than announcing it in robots.txt.
func SitemapCandidates(page *url.URL) []string {
	base := page.Scheme + "://" + page.Host
	dirs := sitemapDirs(page.Path)
	out := make([]string, 0, len(dirs)*len(sitemapNames))
	for _, dir := range dirs {
		for _, name := range sitemapNames {
			out = append(out, base+dir+name)
		}
	}
	return out
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

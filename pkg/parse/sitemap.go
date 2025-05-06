package parse

import "encoding/xml"

// --- XML Structs for Sitemap Parsing ---

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

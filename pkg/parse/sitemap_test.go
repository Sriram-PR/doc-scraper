package parse

import (
	"encoding/xml"
	"testing"
)

// --- XMLURL Tests ---

func TestXMLURL_Unmarshal(t *testing.T) {
	tests := []struct {
		name            string
		xmlData         string
		expectedLoc     string
		expectedLastMod string
	}{
		{
			name:            "LocOnly",
			xmlData:         `<url><loc>https://example.com/page</loc></url>`,
			expectedLoc:     "https://example.com/page",
			expectedLastMod: "",
		},
		{
			name:            "LocAndLastMod",
			xmlData:         `<url><loc>https://example.com/page</loc><lastmod>2024-01-15</lastmod></url>`,
			expectedLoc:     "https://example.com/page",
			expectedLastMod: "2024-01-15",
		},
		{
			name:            "EmptyLoc",
			xmlData:         `<url><loc></loc></url>`,
			expectedLoc:     "",
			expectedLastMod: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var u XMLURL
			err := xml.Unmarshal([]byte(tt.xmlData), &u)
			if err != nil {
				t.Fatalf("xml.Unmarshal() error = %v", err)
			}
			if u.Loc != tt.expectedLoc {
				t.Errorf("XMLURL.Loc = %q, want %q", u.Loc, tt.expectedLoc)
			}
			if u.LastMod != tt.expectedLastMod {
				t.Errorf("XMLURL.LastMod = %q, want %q", u.LastMod, tt.expectedLastMod)
			}
		})
	}
}

// --- XMLURLSet Tests ---

func TestXMLURLSet_Unmarshal(t *testing.T) {
	tests := []struct {
		name         string
		xmlData      string
		expectedURLs int
	}{
		{
			name: "MultipleURLs",
			xmlData: `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>https://example.com/page1</loc></url>
  <url><loc>https://example.com/page2</loc></url>
  <url><loc>https://example.com/page3</loc></url>
</urlset>`,
			expectedURLs: 3,
		},
		{
			name: "SingleURL",
			xmlData: `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>https://example.com/only</loc></url>
</urlset>`,
			expectedURLs: 1,
		},
		{
			name: "EmptyURLSet",
			xmlData: `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
</urlset>`,
			expectedURLs: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var urlSet XMLURLSet
			err := xml.Unmarshal([]byte(tt.xmlData), &urlSet)
			if err != nil {
				t.Fatalf("xml.Unmarshal() error = %v", err)
			}
			if len(urlSet.URLs) != tt.expectedURLs {
				t.Errorf("len(XMLURLSet.URLs) = %d, want %d", len(urlSet.URLs), tt.expectedURLs)
			}
		})
	}
}

func TestXMLURLSet_UnmarshalWithLastMod(t *testing.T) {
	xmlData := `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url>
    <loc>https://example.com/page1</loc>
    <lastmod>2024-01-01</lastmod>
  </url>
  <url>
    <loc>https://example.com/page2</loc>
  </url>
</urlset>`

	var urlSet XMLURLSet
	err := xml.Unmarshal([]byte(xmlData), &urlSet)
	if err != nil {
		t.Fatalf("xml.Unmarshal() error = %v", err)
	}

	if len(urlSet.URLs) != 2 {
		t.Fatalf("len(XMLURLSet.URLs) = %d, want 2", len(urlSet.URLs))
	}

	// First URL has lastmod
	if urlSet.URLs[0].Loc != "https://example.com/page1" {
		t.Errorf("URLs[0].Loc = %q, want %q", urlSet.URLs[0].Loc, "https://example.com/page1")
	}
	if urlSet.URLs[0].LastMod != "2024-01-01" {
		t.Errorf("URLs[0].LastMod = %q, want %q", urlSet.URLs[0].LastMod, "2024-01-01")
	}

	// Second URL has no lastmod
	if urlSet.URLs[1].Loc != "https://example.com/page2" {
		t.Errorf("URLs[1].Loc = %q, want %q", urlSet.URLs[1].Loc, "https://example.com/page2")
	}
	if urlSet.URLs[1].LastMod != "" {
		t.Errorf("URLs[1].LastMod = %q, want empty", urlSet.URLs[1].LastMod)
	}
}

// --- XMLSitemap Tests ---

func TestXMLSitemap_Unmarshal(t *testing.T) {
	tests := []struct {
		name            string
		xmlData         string
		expectedLoc     string
		expectedLastMod string
	}{
		{
			name:            "LocOnly",
			xmlData:         `<sitemap><loc>https://example.com/sitemap1.xml</loc></sitemap>`,
			expectedLoc:     "https://example.com/sitemap1.xml",
			expectedLastMod: "",
		},
		{
			name:            "LocAndLastMod",
			xmlData:         `<sitemap><loc>https://example.com/sitemap1.xml</loc><lastmod>2024-06-01</lastmod></sitemap>`,
			expectedLoc:     "https://example.com/sitemap1.xml",
			expectedLastMod: "2024-06-01",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var s XMLSitemap
			err := xml.Unmarshal([]byte(tt.xmlData), &s)
			if err != nil {
				t.Fatalf("xml.Unmarshal() error = %v", err)
			}
			if s.Loc != tt.expectedLoc {
				t.Errorf("XMLSitemap.Loc = %q, want %q", s.Loc, tt.expectedLoc)
			}
			if s.LastMod != tt.expectedLastMod {
				t.Errorf("XMLSitemap.LastMod = %q, want %q", s.LastMod, tt.expectedLastMod)
			}
		})
	}
}

// --- XMLSitemapIndex Tests ---

func TestXMLSitemapIndex_Unmarshal(t *testing.T) {
	tests := []struct {
		name             string
		xmlData          string
		expectedSitemaps int
	}{
		{
			name: "MultipleSitemaps",
			xmlData: `<?xml version="1.0" encoding="UTF-8"?>
<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <sitemap><loc>https://example.com/sitemap1.xml</loc></sitemap>
  <sitemap><loc>https://example.com/sitemap2.xml</loc></sitemap>
</sitemapindex>`,
			expectedSitemaps: 2,
		},
		{
			name: "SingleSitemap",
			xmlData: `<?xml version="1.0" encoding="UTF-8"?>
<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <sitemap><loc>https://example.com/sitemap.xml</loc></sitemap>
</sitemapindex>`,
			expectedSitemaps: 1,
		},
		{
			name: "EmptySitemapIndex",
			xmlData: `<?xml version="1.0" encoding="UTF-8"?>
<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
</sitemapindex>`,
			expectedSitemaps: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var idx XMLSitemapIndex
			err := xml.Unmarshal([]byte(tt.xmlData), &idx)
			if err != nil {
				t.Fatalf("xml.Unmarshal() error = %v", err)
			}
			if len(idx.Sitemaps) != tt.expectedSitemaps {
				t.Errorf("len(XMLSitemapIndex.Sitemaps) = %d, want %d", len(idx.Sitemaps), tt.expectedSitemaps)
			}
		})
	}
}

func TestXMLSitemapIndex_UnmarshalWithLastMod(t *testing.T) {
	xmlData := `<?xml version="1.0" encoding="UTF-8"?>
<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <sitemap>
    <loc>https://example.com/sitemap1.xml</loc>
    <lastmod>2024-01-15T10:30:00Z</lastmod>
  </sitemap>
  <sitemap>
    <loc>https://example.com/sitemap2.xml</loc>
  </sitemap>
</sitemapindex>`

	var idx XMLSitemapIndex
	err := xml.Unmarshal([]byte(xmlData), &idx)
	if err != nil {
		t.Fatalf("xml.Unmarshal() error = %v", err)
	}

	if len(idx.Sitemaps) != 2 {
		t.Fatalf("len(XMLSitemapIndex.Sitemaps) = %d, want 2", len(idx.Sitemaps))
	}

	// First sitemap has lastmod
	if idx.Sitemaps[0].Loc != "https://example.com/sitemap1.xml" {
		t.Errorf("Sitemaps[0].Loc = %q, want %q", idx.Sitemaps[0].Loc, "https://example.com/sitemap1.xml")
	}
	if idx.Sitemaps[0].LastMod != "2024-01-15T10:30:00Z" {
		t.Errorf("Sitemaps[0].LastMod = %q, want %q", idx.Sitemaps[0].LastMod, "2024-01-15T10:30:00Z")
	}

	// Second sitemap has no lastmod
	if idx.Sitemaps[1].Loc != "https://example.com/sitemap2.xml" {
		t.Errorf("Sitemaps[1].Loc = %q, want %q", idx.Sitemaps[1].Loc, "https://example.com/sitemap2.xml")
	}
	if idx.Sitemaps[1].LastMod != "" {
		t.Errorf("Sitemaps[1].LastMod = %q, want empty", idx.Sitemaps[1].LastMod)
	}
}

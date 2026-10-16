package parse

import (
	"encoding/xml"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
			require.NoError(t, xml.Unmarshal([]byte(tt.xmlData), &u))
			assert.Equal(t, tt.expectedLoc, u.Loc)
			assert.Equal(t, tt.expectedLastMod, u.LastMod)
		})
	}
}

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
			require.NoError(t, xml.Unmarshal([]byte(tt.xmlData), &urlSet))
			assert.Len(t, urlSet.URLs, tt.expectedURLs)
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
	require.NoError(t, xml.Unmarshal([]byte(xmlData), &urlSet))

	require.Len(t, urlSet.URLs, 2)

	// First URL has lastmod
	assert.Equal(t, "https://example.com/page1", urlSet.URLs[0].Loc)
	assert.Equal(t, "2024-01-01", urlSet.URLs[0].LastMod)

	// Second URL has no lastmod
	assert.Equal(t, "https://example.com/page2", urlSet.URLs[1].Loc)
	assert.Empty(t, urlSet.URLs[1].LastMod)
}

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
			require.NoError(t, xml.Unmarshal([]byte(tt.xmlData), &s))
			assert.Equal(t, tt.expectedLoc, s.Loc)
			assert.Equal(t, tt.expectedLastMod, s.LastMod)
		})
	}
}

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
			require.NoError(t, xml.Unmarshal([]byte(tt.xmlData), &idx))
			assert.Len(t, idx.Sitemaps, tt.expectedSitemaps)
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
	require.NoError(t, xml.Unmarshal([]byte(xmlData), &idx))

	require.Len(t, idx.Sitemaps, 2)

	// First sitemap has lastmod
	assert.Equal(t, "https://example.com/sitemap1.xml", idx.Sitemaps[0].Loc)
	assert.Equal(t, "2024-01-15T10:30:00Z", idx.Sitemaps[0].LastMod)

	// Second sitemap has no lastmod
	assert.Equal(t, "https://example.com/sitemap2.xml", idx.Sitemaps[1].Loc)
	assert.Empty(t, idx.Sitemaps[1].LastMod)
}

func TestSitemapCandidates(t *testing.T) {
	names := func(dirs ...string) []string {
		files := []string{"sitemap.xml", "sitemap-index.xml", "sitemap_index.xml"}
		out := make([]string, 0, len(dirs)*len(files))
		for _, d := range dirs {
			for _, n := range files {
				out = append(out, "https://example.com"+d+n)
			}
		}
		return out
	}
	tests := []struct {
		name string
		path string
		want []string
	}{
		{"Root", "/", names("/")},
		{"EmptyPath", "", names("/")},
		{"RootFile", "/index.html", names("/")},
		{"ProjectDir", "/project/", names("/", "/project/")},
		{"ProjectFile", "/project/guide.html", names("/", "/project/")},
		{"CappedAtTwoLevels", "/group/project/docs/page/", names("/", "/group/", "/group/project/")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, err := url.Parse("https://example.com" + tt.path)
			require.NoError(t, err)
			assert.Equal(t, tt.want, SitemapCandidates(u))
		})
	}
}

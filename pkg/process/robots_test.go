package process

import (
	"net/http"
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRobotsDirectives(t *testing.T) {
	tests := []struct {
		name         string
		head         string
		xRobots      []string
		wantNoindex  bool
		wantNofollow bool
	}{
		{name: "no directives"},
		{name: "meta noindex", head: `<meta name="robots" content="noindex">`, wantNoindex: true},
		{name: "meta nofollow", head: `<meta name="robots" content="nofollow">`, wantNofollow: true},
		{name: "meta both, mixed case and spacing", head: `<meta name="ROBOTS" content=" NoIndex ,NOFOLLOW ">`, wantNoindex: true, wantNofollow: true},
		{name: "meta none", head: `<meta name="robots" content="none">`, wantNoindex: true, wantNofollow: true},
		{name: "meta index follow", head: `<meta name="robots" content="index, follow">`},
		{name: "crawler-specific meta ignored", head: `<meta name="googlebot" content="noindex">`},
		{name: "unrelated meta ignored", head: `<meta name="description" content="noindex">`},
		{name: "header noindex", xRobots: []string{"noindex"}, wantNoindex: true},
		{name: "header none", xRobots: []string{"none"}, wantNoindex: true, wantNofollow: true},
		{name: "header across values", xRobots: []string{"noarchive", "nofollow"}, wantNofollow: true},
		{name: "header scoped to another crawler ignored", xRobots: []string{"googlebot: noindex, nofollow"}},
		{name: "header unscoped directive before a scoped one", xRobots: []string{"noindex, googlebot: nofollow"}, wantNoindex: true},
		{name: "header colon directive is not a scope", xRobots: []string{"unavailable_after: 2030-01-01, noindex"}, wantNoindex: true},
		{name: "meta and header combine", head: `<meta name="robots" content="nofollow">`, xRobots: []string{"noindex"}, wantNoindex: true, wantNofollow: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, err := goquery.NewDocumentFromReader(strings.NewReader(
				`<html><head>` + tt.head + `</head><body><p>x</p></body></html>`))
			require.NoError(t, err)
			header := http.Header{}
			for _, v := range tt.xRobots {
				header.Add("X-Robots-Tag", v)
			}

			noindex, nofollow := RobotsDirectives(doc, header)
			assert.Equal(t, tt.wantNoindex, noindex, "noindex")
			assert.Equal(t, tt.wantNofollow, nofollow, "nofollow")
		})
	}
}

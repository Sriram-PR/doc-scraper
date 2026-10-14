package process

import (
	"net/http"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// colonDirectives are X-Robots-Tag directives that carry a value, so their
// colon does not mark a crawler-scoped header value.
var colonDirectives = map[string]struct{}{
	"unavailable_after": {}, "max-snippet": {}, "max-image-preview": {}, "max-video-preview": {},
}

// RobotsDirectives reports page-level noindex and nofollow from
// <meta name="robots"> and the X-Robots-Tag header. Directives aimed at a
// named crawler (<meta name="googlebot">, "googlebot: noindex") are ignored.
func RobotsDirectives(doc *goquery.Document, header http.Header) (noindex, nofollow bool) {
	apply := func(list string) {
		for _, d := range strings.Split(list, ",") {
			switch strings.ToLower(strings.TrimSpace(d)) {
			case "noindex":
				noindex = true
			case "nofollow":
				nofollow = true
			case "none":
				noindex, nofollow = true, true
			}
		}
	}

	doc.Find("meta[name]").Each(func(_ int, s *goquery.Selection) {
		if name, _ := s.Attr("name"); strings.EqualFold(strings.TrimSpace(name), "robots") {
			content, _ := s.Attr("content")
			apply(content)
		}
	})

	for _, v := range header.Values("X-Robots-Tag") {
		if scope, _, ok := strings.Cut(v, ":"); ok && !strings.Contains(scope, ",") {
			if _, known := colonDirectives[strings.ToLower(strings.TrimSpace(scope))]; !known {
				continue
			}
		}
		apply(v)
	}
	return noindex, nofollow
}

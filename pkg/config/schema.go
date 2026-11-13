package config

import (
	"fmt"
	"reflect"
)

// RequiredSiteFields are the entry fields SiteConfig.Validate refuses to do without.
var RequiredSiteFields = []string{"start_urls", "allowed_domain", "content_selector"}

var siteFieldDescriptions = map[string]string{
	"start_urls":                "Seed URLs the crawl starts from",
	"allowed_domain":            "Host the crawl stays on, e.g. docs.example.com",
	"allowed_path_prefix":       `Only follow paths under this prefix (default "/")`,
	"content_selector":          "CSS selector for the page's main content, e.g. main or article",
	"link_extraction_selectors": "CSS selectors whose links are followed (default: whole page)",
	"disallowed_path_patterns":  "Regexes; matching paths are skipped",
	"respect_nofollow":          "Skip links marked rel=nofollow",
	"respect_noindex":           "Skip pages marked noindex (default true)",
	"user_agent":                "Overrides the server's user agent for this site",
	"delay_per_host":            `Minimum gap between requests, a duration such as "1s"`,
	"max_depth":                 "Exclusive depth bound: start pages are depth 0, so 1 fetches only the start pages and 2 adds the pages they link to. 0 means unlimited",
	"skip_images":               "Don't download images (default: the server-wide skip_images, which is true unless set)",
	"max_image_size_bytes":      "Largest image to download; 0 means unlimited",
	"allowed_image_domains":     "Only download images from these hosts",
	"disallowed_image_domains":  "Never download images from these hosts",
}

var siteFieldExtras = map[string]map[string]any{
	"start_urls":           {"minItems": 1},
	"max_depth":            {"minimum": 0},
	"max_image_size_bytes": {"minimum": 0},
}

// SiteFieldSchema returns the JSON Schema properties of a site entry as MCP
// clients may set it, derived from SiteConfig so the two cannot drift.
func SiteFieldSchema() map[string]any {
	props := map[string]any{}
	for name, t := range yamlFields(reflect.TypeOf(SiteConfig{})) {
		if refusedSiteFields[name] {
			continue
		}
		p := jsonType(t)
		p["description"] = siteFieldDescriptions[name]
		for k, v := range siteFieldExtras[name] {
			p[k] = v
		}
		props[name] = p
	}
	return props
}

func jsonType(t reflect.Type) map[string]any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch {
	case t == durationType, t.Kind() == reflect.String:
		return map[string]any{"type": "string"}
	case t.Kind() == reflect.Bool:
		return map[string]any{"type": "boolean"}
	case t.Kind() >= reflect.Int && t.Kind() <= reflect.Int64:
		return map[string]any{"type": "integer"}
	case t.Kind() == reflect.Slice:
		return map[string]any{"type": "array", "items": jsonType(t.Elem())}
	}
	panic(fmt.Sprintf("config: no JSON type for site field kind %s", t))
}

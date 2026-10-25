package config

import (
	"fmt"
	"maps"
	"math"
	"reflect"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// refusedSiteFields can't be set over MCP: turning JSONL output off breaks
// read_page and search_docs for the site.
var refusedSiteFields = map[string]bool{"enable_jsonl_output": true, "jsonl_output_filename": true}

var durationType = reflect.TypeOf(time.Duration(0))

// MergeSiteFields sets fields, keyed by YAML name with JSON-decoded values, on
// a copy of base. Unknown and refused names are errors, as are values of the
// wrong type for their field.
func MergeSiteFields(base SiteConfig, fields map[string]any) (SiteConfig, error) {
	known := yamlFields(reflect.TypeOf(SiteConfig{}))
	names := slices.Sorted(maps.Keys(fields))
	for _, name := range names {
		if refusedSiteFields[name] {
			return SiteConfig{}, fmt.Errorf("field %q can't be set here: turning JSONL output off breaks read_page and search_docs", name)
		}
		t, ok := known[name]
		if !ok {
			return SiteConfig{}, fmt.Errorf("unknown field %q; allowed fields: %s", clipName(name), strings.Join(allowedSiteFields(known), ", "))
		}
		if err := checkFieldValue(name, t, fields[name]); err != nil {
			return SiteConfig{}, err
		}
	}

	out := cloneSite(base)
	for _, name := range names {
		v := fields[name]
		if f, ok := v.(float64); ok {
			v = int64(f)
		}
		raw, err := yaml.Marshal(map[string]any{name: v})
		if err != nil {
			return SiteConfig{}, fmt.Errorf("field %q: %w", name, err)
		}
		if err := yaml.Unmarshal(raw, &out); err != nil {
			return SiteConfig{}, fmt.Errorf("field %q: %w", name, err)
		}
	}
	return out, nil
}

// checkFieldValue catches what yaml.v3 would accept silently: it truncates a
// fraction into an integer field and reads a bare number as nanoseconds.
func checkFieldValue(name string, t reflect.Type, v any) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	f, isNum := v.(float64)
	switch {
	case t == durationType && v != nil:
		if _, ok := v.(string); !ok {
			return fmt.Errorf("field %q must be a duration string such as \"1s\"", name)
		}
	case isNum && t.Kind() >= reflect.Int && t.Kind() <= reflect.Int64:
		if f != math.Trunc(f) || math.Abs(f) > 1<<53 {
			return fmt.Errorf("field %q must be an integer", name)
		}
	}
	return nil
}

// clipName bounds an agent-supplied field name echoed in an error.
func clipName(name string) string {
	const maxLen = 64
	if len(name) <= maxLen {
		return name
	}
	return strings.ToValidUTF8(name[:maxLen], "") + "..."
}

func allowedSiteFields(known map[string]reflect.Type) []string {
	var out []string
	for name := range known {
		if !refusedSiteFields[name] {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// cloneSite copies s so yaml decoding into the copy can't write through
// shared slices or pointers.
func cloneSite(s SiteConfig) SiteConfig {
	c := s
	c.StartURLs = slices.Clone(s.StartURLs)
	c.LinkExtractionSelectors = slices.Clone(s.LinkExtractionSelectors)
	c.DisallowedPathPatterns = slices.Clone(s.DisallowedPathPatterns)
	c.AllowedImageDomains = slices.Clone(s.AllowedImageDomains)
	c.DisallowedImageDomains = slices.Clone(s.DisallowedImageDomains)
	c.RespectNoindex = clonePtr(s.RespectNoindex)
	c.SkipImages = clonePtr(s.SkipImages)
	c.MaxImageSizeBytes = clonePtr(s.MaxImageSizeBytes)
	c.EnableJSONLOutput = clonePtr(s.EnableJSONLOutput)
	return c
}

func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// SiteFieldMap renders site keyed by YAML field name, the shape
// MergeSiteFields takes, omitting the fields RenderSiteEntry omits and the
// refused ones, so the map can be passed back unchanged.
func SiteFieldMap(site *SiteConfig) (map[string]any, error) {
	raw, err := yaml.Marshal(site)
	if err != nil {
		return nil, fmt.Errorf("render site entry: %w", err)
	}
	m := map[string]any{}
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("render site entry: %w", err)
	}
	for name := range refusedSiteFields {
		delete(m, name)
	}
	return m, nil
}

// ChangedSiteFields lists the YAML names of the fields that differ between a and b.
func ChangedSiteFields(a, b SiteConfig) []string {
	va, vb := reflect.ValueOf(a), reflect.ValueOf(b)
	t := va.Type()
	var out []string
	for i := range t.NumField() {
		if !reflect.DeepEqual(va.Field(i).Interface(), vb.Field(i).Interface()) {
			name, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ",")
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

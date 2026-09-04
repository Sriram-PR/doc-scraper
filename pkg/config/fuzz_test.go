package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// FuzzSpliceSiteEntry checks the config writer's core safety property: for any
// input file, splicing either fails cleanly or yields YAML that still parses
// and contains the new site, with every previously parseable site retained.
func FuzzSpliceSiteEntry(f *testing.F) {
	entry, err := RenderSiteEntry("fuzz_site", &SiteConfig{
		StartURLs:       []string{"https://a.example/docs/"},
		AllowedDomain:   "a.example",
		ContentSelector: "main",
	})
	if err != nil {
		f.Fatal(err)
	}

	f.Add("sites:\n  old:\n    allowed_domain: b.example\n")
	f.Add("# comment\nnum_workers: 2\n")
	f.Add("")
	f.Add("sites: {}\n")
	f.Add("{}")
	f.Add("{a: 1}\n")
	f.Add("sites: 3\n")
	f.Add("sites:\n")
	f.Add("- a\n- b\n")
	f.Add("sites:\n  old: |\n    multiline\n    scalar\nafter: true\n")
	f.Add(strings.Repeat("k:\n ", 100))
	f.Fuzz(func(t *testing.T, input string) {
		var before AppConfig
		beforeErr := yaml.Unmarshal([]byte(input), &before)

		out, err := spliceSiteEntry([]byte(input), "fuzz_site", entry)
		if err != nil {
			return
		}
		var after AppConfig
		if err := yaml.Unmarshal(out, &after); err != nil {
			if beforeErr == nil {
				t.Fatalf("splice broke previously valid YAML: %v\ninput: %q\nout: %q", err, input, out)
			}
			return
		}
		if _, ok := after.Sites["fuzz_site"]; !ok && beforeErr == nil {
			t.Fatalf("spliced config lost the new site\ninput: %q\nout: %q", input, out)
		}
		if beforeErr == nil {
			for key := range before.Sites {
				if _, ok := after.Sites[key]; !ok {
					t.Fatalf("splice dropped existing site %q\ninput: %q\nout: %q", key, input, out)
				}
			}
		}
	})
}

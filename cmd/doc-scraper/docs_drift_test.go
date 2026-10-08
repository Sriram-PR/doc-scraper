package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
)

var (
	docsRoot     = filepath.Join("..", "..", "docs", "src", "content", "docs")
	tableKeyRow  = regexp.MustCompile("(?m)^\\| `([^`]+)` \\|")
	helpFlagLine = regexp.MustCompile(`(?m)^  (-[a-z][a-z-]*)\b`)
)

func readDocsPage(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(docsRoot, filepath.FromSlash(rel)))
	require.NoError(t, err, "docs page %s is missing", rel)
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

// docsSection returns the body under a level-2 heading, up to the next level-2 heading.
func docsSection(t *testing.T, page, heading string) string {
	t.Helper()
	_, after, ok := strings.Cut(page, "\n"+heading+"\n")
	require.True(t, ok, "missing heading %q", heading)
	section, _, _ := strings.Cut(after, "\n## ")
	return section
}

func tableKeys(section string) []string {
	matches := tableKeyRow.FindAllStringSubmatch(section, -1)
	keys := make([]string, 0, len(matches))
	for _, m := range matches {
		keys = append(keys, m[1])
	}
	sort.Strings(keys)
	return keys
}

func yamlKeys(typ reflect.Type) []string {
	keys := []string{}
	for i := range typ.NumField() {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("yaml"), ",")
		if name != "" && name != "-" {
			keys = append(keys, name)
		}
	}
	sort.Strings(keys)
	return keys
}

func TestDocs_ConfigurationReferenceMatchesConfigStructs(t *testing.T) {
	page := readDocsPage(t, "reference/configuration.md")
	for _, tc := range []struct {
		heading string
		typ     reflect.Type
	}{
		{"## Global options", reflect.TypeFor[config.AppConfig]()},
		{"## HTTP client settings", reflect.TypeFor[config.HTTPClientConfig]()},
		{"## Site options", reflect.TypeFor[config.SiteConfig]()},
	} {
		t.Run(tc.typ.Name(), func(t *testing.T) {
			assert.Equal(t, yamlKeys(tc.typ), tableKeys(docsSection(t, page, tc.heading)),
				"docs reference/configuration.md %q table is out of sync with config.%s yaml keys", tc.heading, tc.typ.Name())
		})
	}
}

func helpFlags(t *testing.T, bin string, args []string) []string {
	t.Helper()
	out, err := exec.Command(bin, append(args, "-h")...).CombinedOutput()
	require.NoError(t, err, "%v -h:\n%s", args, out)
	seen := map[string]bool{}
	flags := []string{}
	for _, m := range helpFlagLine.FindAllStringSubmatch(string(out), -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			flags = append(flags, m[1])
		}
	}
	sort.Strings(flags)
	return flags
}

func TestDocs_CLIReferenceMatchesHelp(t *testing.T) {
	bin := smokeBinary(t)
	page := readDocsPage(t, "reference/cli.md")
	for _, args := range [][]string{
		{"crawl"}, {"watch"}, {"add"}, {"search"}, {"mcp-server"}, {"config", "validate"}, {"config", "list"},
	} {
		name := strings.Join(args, " ")
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, helpFlags(t, bin, args), tableKeys(docsSection(t, page, "## `"+name+"`")),
				"docs reference/cli.md section for %q is out of sync with `doc-scraper %s -h`", name, name)
		})
	}
}

func docsToolNames(t *testing.T) []string {
	t.Helper()
	return tableKeys(docsSection(t, readDocsPage(t, "mcp/tools.md"), "## Tools"))
}

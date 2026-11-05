package process

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
)

func FuzzToMarkdown(f *testing.F) {
	fixtures, _ := filepath.Glob(filepath.Join("testdata", "tables", "*.html"))
	for _, path := range fixtures {
		page, err := os.ReadFile(path)
		if err == nil {
			f.Add(string(page))
		}
	}
	f.Add(`<table><tr><th>A</th><th>B</th></tr><tr><td>a<br>b</td><td><code>x | y</code></td></tr></table>`)
	f.Add(`<table><tr><th colspan="2">H</th></tr><tr><td rowspan="3">a</td><td><ul><li>x</li></ul></td></tr></table>`)
	f.Add(`<table><tr><td><table><tr><td><pre>1
2</pre></td><td><pre>a
b</pre></td></tr></table></td></tr></table>`)
	f.Fuzz(func(t *testing.T, page string) {
		doc, err := goquery.NewDocumentFromReader(strings.NewReader(page))
		if err != nil {
			return
		}
		got, err := ToMarkdown(doc.Find("body"))
		if err != nil {
			return
		}
		if row := malformedTableRow(got); row != "" {
			t.Fatalf("table row %q has a different cell count from its header in:\n%s", row, got)
		}
	})
}

var delimiterRow = regexp.MustCompile(`^\|(\s*:?-+:?\s*\|)+$`)

// malformedTableRow returns the first GFM table row, outside fenced code, whose
// count of unescaped "|" differs from its header's.
func malformedTableRow(markdown string) string {
	lines := strings.Split(markdown, "\n")
	inFence := false
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence || !strings.HasPrefix(line, "|") || i+1 >= len(lines) || !delimiterRow.MatchString(strings.TrimSpace(lines[i+1])) {
			continue
		}
		want := unescapedPipes(line)
		for i += 2; i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "|"); i++ {
			if unescapedPipes(strings.TrimSpace(lines[i])) != want {
				return lines[i]
			}
		}
		i--
	}
	return ""
}

func unescapedPipes(row string) int {
	n := 0
	for i := 0; i < len(row); i++ {
		switch row[i] {
		case '\\':
			i++
		case '|':
			n++
		}
	}
	return n
}

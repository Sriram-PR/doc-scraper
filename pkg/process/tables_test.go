package process

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func tableMarkdown(t *testing.T, html string) string {
	t.Helper()
	got, err := ToMarkdown(selectionOf(t, "<main>"+html+"</main>", "main"))
	require.NoError(t, err)
	return got
}

func fixtureMarkdown(t *testing.T, name string) string {
	t.Helper()
	page, err := os.ReadFile(filepath.Join("testdata", "tables", name))
	require.NoError(t, err)
	got, err := ToMarkdown(selectionOf(t, string(page), "main"))
	require.NoError(t, err)
	return got
}

func TestToMarkdown_CodeTables(t *testing.T) {
	const fooPy = "def foo(bar):\n    return bar | 1\n\n\nprint(foo(2))"
	tests := []struct {
		fixture string
		want    string
	}{
		{"pymdownx-filename.html", "x.py\n\n```\n" + fooPy + "\n```\n\nplain\n\n```\n" + fooPy + "\n```"},
		{"pygments-anchors-linenostep.html", "a.py\n\n```\n" + fooPy + "\n```"},
		{"chroma-inline-styles.html", "```\nfunc main() {\n\tx := a | b\n\n\tprintln(x)\n}\n```"},
		{"chroma-classes.html", "```\nfunc main() {\n\tx := a | b\n\n\tprintln(x)\n}\n```"},
		{"rouge-jekyll-handwritten.html", "```ruby\ndef hi\n  puts \"a | b\"\nend\n```"},
		{"sphinx-highlight-python.html", "```python\nx = 1\nprint(x)\n```"},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			assert.Equal(t, tt.want, fixtureMarkdown(t, tt.fixture))
		})
	}
}

func TestToMarkdown_TwoPreCellsThatAreNotLineNumbersStayData(t *testing.T) {
	got := tableMarkdown(t, `<table><tr><td><pre>x = 1</pre></td><td><pre>y = 2</pre></td></tr></table>`)
	assert.Contains(t, got, "x = 1")
	assert.Contains(t, got, "y = 2")
}

func TestMarkdownFormatVersion(t *testing.T) {
	assert.Equal(t, 2, MarkdownFormatVersion, "table output changed in v2.25.0")
}

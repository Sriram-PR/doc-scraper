package process

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func selectionOf(t *testing.T, page, selector string) *goquery.Selection {
	t.Helper()
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(page))
	require.NoError(t, err)
	sel := doc.Find(selector)
	require.Positive(t, sel.Length(), "selector %q matched nothing", selector)
	return sel
}

func TestToMarkdown_GitHubFlavored(t *testing.T) {
	tests := []struct {
		name string
		html string
		want []string
		not  []string
	}{
		{
			name: "PipeTable",
			html: `<table><thead><tr><th>Name</th><th>Type</th></tr></thead><tbody><tr><td>alpha</td><td>string</td></tr></tbody></table>`,
			want: []string{"| Name", "| alpha", "|---"},
		},
		{
			name: "ColspanKeepsColumnCount",
			html: `<table><thead><tr><th>A</th><th>B</th></tr></thead><tbody><tr><td colspan="2">span</td></tr></tbody></table>`,
			want: []string{"| span |   |"},
		},
		{
			name: "Strikethrough",
			html: `<p><del>gone</del> and <s>struck</s></p>`,
			want: []string{"~~gone~~", "~~struck~~"},
		},
		{
			name: "TaskList",
			html: `<ul><li><input type="checkbox" checked> done</li><li><input type="checkbox"> todo</li></ul>`,
			want: []string{"- [x]  done", "- [ ]  todo"},
		},
		{
			name: "NonCheckboxInputDropped",
			html: `<ul><li><input type="text" value="q"> text</li></ul>`,
			want: []string{"- text"},
			not:  []string{"[ ]", "[x]", "q"},
		},
		{
			name: "FencedCodeKeepsLanguage",
			html: "<pre><code class=\"language-rust\">fn main() {}\n</code></pre>",
			want: []string{"```rust\nfn main() {}\n```"},
		},
		{
			name: "NoListEndComment",
			html: `<ul><li>one</li></ul><ul><li>two</li></ul>`,
			want: []string{"- one", "- two"},
			not:  []string{"<!--"},
		},
		{
			name: "RelativeLinksStayRelative",
			html: `<p><a href="../a/b.md#frag">link</a> <img src="img/x.png" alt="pic"></p>`,
			want: []string{"[link](../a/b.md#frag)", "![pic](img/x.png)"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ToMarkdown(selectionOf(t, "<main>"+tt.html+"</main>", "main"))
			require.NoError(t, err)
			for _, w := range tt.want {
				assert.Contains(t, got, w)
			}
			for _, n := range tt.not {
				assert.NotContains(t, got, n)
			}
		})
	}
}

// SelectByPriority and readability can hand back several top-level nodes.
func TestToMarkdown_ConvertsEverySelectedNode(t *testing.T) {
	got, err := ToMarkdown(selectionOf(t, `<main><p>first main</p></main><main><p>second main</p></main>`, "main"))
	require.NoError(t, err)
	assert.Contains(t, got, "first main")
	assert.Contains(t, got, "second main")
}

func TestToMarkdown_LeavesSelectionUntouched(t *testing.T) {
	sel := selectionOf(t, `<main><p>keep <script>x()</script></p><ul><li><input type="checkbox" checked> a</li></ul></main>`, "main")
	before, err := goquery.OuterHtml(sel)
	require.NoError(t, err)
	_, err = ToMarkdown(sel)
	require.NoError(t, err)
	after, err := goquery.OuterHtml(sel)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestToMarkdown_EmptySelection(t *testing.T) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(`<p>x</p>`))
	require.NoError(t, err)
	got, err := ToMarkdown(doc.Find("main"))
	require.NoError(t, err)
	assert.Empty(t, got)
}

// v1 was quadratic in list and table size (64k items took 69 s); this input
// takes v2 about 0.15 s without -race.
func TestToMarkdown_LargeListAndTableAreFast(t *testing.T) {
	const items = 32000
	var b strings.Builder
	b.WriteString("<main><ul>")
	for i := range items {
		fmt.Fprintf(&b, "<li>item %d</li>", i)
	}
	b.WriteString("</ul><table>")
	for i := range items / 2 {
		fmt.Fprintf(&b, "<tr><td>a%d</td><td>b</td></tr>", i)
	}
	b.WriteString("</table></main>")
	sel := selectionOf(t, b.String(), "main")

	start := time.Now()
	got, err := ToMarkdown(sel)
	elapsed := time.Since(start)
	require.NoError(t, err)
	assert.Contains(t, got, fmt.Sprintf("- item %d", items-1))
	assert.Less(t, elapsed, 5*time.Second, "conversion must stay linear in input size")
}

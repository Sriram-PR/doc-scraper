package process

import (
	"os"
	"path/filepath"
	"strings"
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

func TestToMarkdown_DataTables(t *testing.T) {
	tests := []struct {
		name string
		html string
		want string
	}{
		{
			name: "BrCellStaysATable",
			html: `<table><thead><tr><th>Param</th><th>Description</th></tr></thead><tbody><tr><td>a</td><td>First line.<br>Second line.</td></tr></tbody></table>`,
			want: "| Param | Description                 |\n|-------|-----------------------------|\n| a     | First line.<br>Second line. |",
		},
		{
			name: "ParagraphsJoinedByBr",
			html: `<table><tr><th>Name</th><th>Notes</th></tr><tr><td>a</td><td><p>one</p><p>two</p></td></tr></table>`,
			want: "| Name | Notes      |\n|------|------------|\n| a    | one<br>two |",
		},
		{
			name: "PipeInCodeIsEscaped",
			html: `<table><tr><th>Op</th><th>Result</th></tr><tr><td><code>x | y</code></td><td>union</td></tr></table>`,
			want: "| Op       | Result |\n|----------|--------|\n| `x \\| y` | union  |",
		},
		{
			name: "PipeInAltAndTitleIsEscaped",
			html: `<table><tr><th>A</th><th>B</th></tr><tr><td><img src="a.png" alt="a|b" title="c|d"> <a href="u" title="t|x">link</a></td><td>v</td></tr></table>`,
			want: "| A                                      | B |\n|----------------------------------------|---|\n| ![a\\|b](a.png \"c\\|d\") [link](u \"t\\|x\") | v |",
		},
		{
			name: "OneLinePreBecomesInlineCode",
			html: `<table><tr><th>Syntax:</th><td><pre>aio on | off | threads[=pool];</pre></td></tr><tr><th>Default:</th><td><pre>aio off;</pre></td></tr></table>`,
			want: "| Syntax:  | `aio on \\| off \\| threads[=pool];` |\n|----------|------------------------------------|\n| Default: | `aio off;`                         |",
		},
		{
			name: "FourLinesStayATable",
			html: `<table><tr><th>A</th><th>B</th></tr><tr><td>x</td><td>1<br>2<br>3<br>4</td></tr></table>`,
			want: "| A | B                |\n|---|------------------|\n| x | 1<br>2<br>3<br>4 |",
		},
		{
			name: "EmptyDivBeforeParagraphAddsNoBr",
			html: `<table><tr><th>Key</th><th>Description</th></tr><tr><td>name</td><td><div class="ansibleOptionAnchor"></div><p>The name.</p></td></tr></table>`,
			want: "| Key  | Description |\n|------|-------------|\n| name | The name.   |",
		},
		{
			name: "BrInsideALinkBecomesASpace",
			html: `<table><tr><th>Spec</th></tr><tr><td><a href="#s">HTML<br>Living Standard</a></td></tr></table>`,
			want: "| Spec                       |\n|----------------------------|\n| [HTML Living Standard](#s) |",
		},
		{
			name: "InlineOnlyTableUnchanged",
			html: `<table><thead><tr><th>Name</th><th>Type</th></tr></thead><tbody><tr><td>alpha</td><td>string</td></tr></tbody></table>`,
			want: "| Name  | Type   |\n|-------|--------|\n| alpha | string |",
		},
		{
			name: "ListCellFallsBackWithCellsApart",
			html: `<table><tr><th>Element</th><th>Permitted content</th></tr><tr><td>ul</td><td><ul><li>li</li><li>script</li></ul></td></tr></table>`,
			want: "**Element:** ul\n\n**Permitted content:**\n\n- li\n- script",
		},
		{
			name: "MultiLinePreFallsBack",
			html: "<table><tr><th>Function</th><th>Example</th></tr><tr><td>concat</td><td><pre>select concat('a',\n  'b');</pre></td></tr></table>",
			want: "**Function:** concat\n\n**Example:**\n\n```\nselect concat('a',\n  'b');\n```",
		},
		{
			name: "FiveLinesFallBack",
			html: `<table><tr><th>A</th><th>B</th></tr><tr><td>x</td><td>1<br>2<br>3<br>4<br>5</td></tr></table>`,
			want: "**A:** x\n\n**B:** 1  \n2  \n3  \n4  \n5",
		},
		{
			name: "PresentationTableFallsBack",
			html: `<table role="presentation"><tr><td>left</td><td>right</td></tr></table>`,
			want: "left\n\nright",
		},
		{
			name: "TableInsideALinkStaysInline",
			html: `<a href="#t"><table><tr><td>left</td><td>right</td></tr></table></a>`,
			want: "[left right](#t)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tableMarkdown(t, tt.html))
		})
	}
}

func TestToMarkdown_FallbackLabels(t *testing.T) {
	tests := []struct {
		name string
		html string
		want string
	}{
		{
			name: "EachBodyCellNamesItsColumn",
			html: `<table><tr><th>Function</th><th>Description</th><th>Example</th></tr>` +
				`<tr><td><code>concat(val1, ...)</code></td><td><p>Concatenates the arguments.</p><ul><li>NULL arguments are ignored.</li></ul></td><td><code>concat('abc', 2)</code></td></tr>` +
				`<tr><td><code>left(s, n)</code></td><td>First n characters.</td><td><code>left('abc', 2)</code></td></tr></table>`,
			want: "**Function:** `concat(val1, ...)`\n\n**Description:** Concatenates the arguments.\n\n- NULL arguments are ignored.\n\n**Example:** `concat('abc', 2)`\n\n" +
				"**Function:** `left(s, n)`\n\n**Description:** First n characters.\n\n**Example:** `left('abc', 2)`",
		},
		{
			name: "ColspanAndRowspanMapToTheirHeaders",
			html: `<table><tr><th colspan="2">Name</th><th>Notes</th></tr><tr><td rowspan="2">a</td><td>a1</td><td><ul><li>x</li></ul></td></tr><tr><td>a2</td><td>y</td></tr></table>`,
			want: "**Name:** a\n\n**Name:** a1\n\n**Notes:**\n\n- x\n\n**Name:** a2\n\n**Notes:** y",
		},
		{
			name: "NestedTableStaysATableUnderItsLabel",
			html: `<table><tr><th>Member</th><th>Details</th></tr><tr><td>size</td><td><table><tr><th>Ret</th><th>Notes</th></tr><tr><td>int</td><td>count</td></tr></table></td></tr></table>`,
			want: "**Member:** size\n\n**Details:**\n\n| Ret | Notes |\n|-----|-------|\n| int | count |",
		},
		{
			name: "NestedFallbackGetsItsOwnLabels",
			html: `<table><tr><th>Outer</th><th>Info</th></tr><tr><td>o</td><td><table><tr><th>In</th></tr><tr><td><ul><li>deep</li></ul></td></tr></table></td></tr></table>`,
			want: "**Outer:** o\n\n**Info:**\n\n**In:**\n\n- deep",
		},
		{
			name: "EmptyCellGetsNoLabel",
			html: `<table><tr><th>A</th><th>B</th></tr><tr><td></td><td><ul><li>x</li></ul></td></tr></table>`,
			want: "**B:**\n\n- x",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tableMarkdown(t, tt.html))
		})
	}
}

func TestToMarkdown_MalformedTables(t *testing.T) {
	tests := []struct {
		name string
		html string
		want string
	}{
		{
			name: "BadSpansCountAsOne",
			html: `<table><tr><th>A</th><th>B</th></tr><tr><td colspan="abc">x</td><td rowspan="-3">y</td><td colspan="0">z</td></tr></table>`,
			want: "| A | B |   |\n|---|---|---|\n| x | y | z |",
		},
		{
			name: "RaggedRows",
			html: `<table><tr><th>A</th><th>B</th><th>C</th></tr><tr><td>1</td></tr><tr><td>1</td><td>2</td><td>3</td><td>4</td></tr></table>`,
			want: "| A | B | C |   |\n|---|---|---|---|\n| 1 |   |   |   |\n| 1 | 2 | 3 | 4 |",
		},
		{
			name: "EmptyTable",
			html: `<table></table>`,
			want: "",
		},
		{
			name: "EmptyRow",
			html: `<table><tr></tr></table>`,
			want: "",
		},
		{
			name: "HeaderCellInABodyRow",
			html: `<table><tr><th>Key</th><th>Value</th></tr><tr><th>a</th><td><ul><li>x</li></ul></td></tr></table>`,
			want: "**Key:** a\n\n**Value:**\n\n- x",
		},
		{
			name: "RowOutsideTbody",
			html: `<table><thead><tr><th>H</th></tr></thead><tr><td>outside tbody</td></tr></table>`,
			want: "| H             |\n|---------------|\n| outside tbody |",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tableMarkdown(t, tt.html))
		})
	}
}

// 1000 rows each claiming rowspan=1000 colspan=1000 would need a billion grid
// slots; past the budget the table is written without labels.
func TestToMarkdown_HugeSpansSkipLabels(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<table><tr><th>A</th><th>B</th></tr>`)
	for range 1000 {
		b.WriteString(`<tr><td rowspan="1000" colspan="1000">x</td><td><ul><li>y</li></ul></td></tr>`)
	}
	b.WriteString(`</table>`)

	got := tableMarkdown(t, b.String())

	assert.NotContains(t, got, "**A:**")
	assert.Contains(t, got, "x\n\n- y")
}

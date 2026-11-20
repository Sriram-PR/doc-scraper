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

// v1 was quadratic in list and table size and took 69 s on this input; v2
// takes about 0.3 s, or a few seconds under -race.
func TestToMarkdown_LargeListAndTableAreFast(t *testing.T) {
	const items = 64000
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
	assert.Less(t, elapsed, 20*time.Second, "conversion must stay linear in input size")
}

func benchmarkPage(b *testing.B) *goquery.Selection {
	b.Helper()
	page := strings.Repeat(`<h2>Title</h2><p>Some <a href="/x">linked</a> text with <code>code</code>.</p><ul><li>one</li><li>two</li></ul>`, 50)
	doc, err := goquery.NewDocumentFromReader(strings.NewReader("<body>" + page + "</body>"))
	require.NoError(b, err)
	return doc.Find("body")
}

func BenchmarkToMarkdownParallel(b *testing.B) {
	sel := benchmarkPage(b)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := ToMarkdown(sel); err != nil {
				b.Error(err)
				return
			}
		}
	})
}

// The converter uses BEL as its escape marker; one left in the page would leave
// the "|" after it bare and break the table row.
func TestToMarkdown_BellInPageIsDropped(t *testing.T) {
	const page = "<table><tr><th>H</th><th>I</th></tr><tr><td>a\a|b</td><td>c</td></tr></table>"
	got, err := ToMarkdown(selectionOf(t, "<main>"+page+"</main>", "main"))
	require.NoError(t, err)
	want, err := ToMarkdown(selectionOf(t, "<main>"+strings.ReplaceAll(page, "\a", "")+"</main>", "main"))
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestToMarkdown_CodeBlockLines(t *testing.T) {
	tests := []struct {
		name string
		html string
		want string
	}{
		{
			name: "DocusaurusLinesWithBlankLine",
			html: `<pre class="prism-code language-bash"><code class="codeBlockLines_FJaf">` +
				`<div class="token-line"><span class="token plain">cd my-website</span><br/></div>` +
				`<div class="token-line"><span class="token plain" style="display:inline-block"></span><br/></div>` +
				`<div class="token-line"><span class="token function">npm</span><span class="token plain"> run start</span><br/></div>` +
				`</code></pre>`,
			want: "```bash\ncd my-website\n\nnpm run start\n```",
		},
		{
			name: "ExpressiveCodeLines",
			html: `<pre data-language="js"><code>` +
				`<div class="ec-line"><div class="code"><span>import a from 'a';</span></div></div>` +
				`<div class="ec-line"><div class="code">
</div></div>` +
				`<div class="ec-line"><div class="code"><span>export default a;</span></div></div>` +
				`</code></pre>`,
			want: "```\nimport a from 'a';\n\nexport default a;\n```",
		},
		{
			name: "NextraCopyButtonInsidePre",
			html: `<pre tabindex="0"><div class="x:absolute"><button title="Copy code"><svg></svg></button></div>` +
				`<code class="language-js" data-language="js"><span class="line"><span>const a = 1</span></span>
<span class="line"><span>const b = 2</span></span></code></pre>`,
			want: "```js\nconst a = 1\nconst b = 2\n```",
		},
		{
			name: "BreaksWithoutCode",
			html: `<pre>a<br>b</pre>`,
			want: "```\na\nb\n```",
		},
		{
			name: "BackticksInFlattenedCode",
			html: `<pre class="language-md"><code><div class="token-line">` + "```js" + `<br/></div><div class="token-line">x<br/></div><div class="token-line">` + "```" + `<br/></div></code></pre>`,
			want: "````md\n```js\nx\n```\n````",
		},
		{
			name: "CodeWithoutClassHasNoInfoString",
			html: `<pre><code><div>one</div><div>two</div></code></pre>`,
			want: "```\none\ntwo\n```",
		},
		{
			name: "PlainCodeUnchanged",
			html: "<pre><code class=\"language-go\">package main\n\nfunc main() {}\n</code></pre>",
			want: "```go\npackage main\n\nfunc main() {}\n```",
		},
		{
			name: "ChromaLinesUnchanged",
			html: `<pre tabindex="0" class="chroma"><code class="language-sh" data-lang="sh"><span class="line"><span class="cl">hugo new site quickstart
</span></span><span class="line"><span class="cl">cd quickstart
</span></span></code></pre>`,
			want: "```sh\nhugo new site quickstart\ncd quickstart\n```",
		},
		{
			name: "ShikiLinesUnchanged",
			html: `<pre class="shiki" tabindex="0"><code><span class="line"><span>npm add -D vitepress</span></span>
<span class="line"></span>
<span class="line"><span>npx vitepress init</span></span></code></pre>`,
			want: "```\nnpm add -D vitepress\n\nnpx vitepress init\n```",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ToMarkdown(selectionOf(t, "<main>"+tt.html+"</main>", "main"))
			require.NoError(t, err)
			assert.Equal(t, tt.want, strings.TrimSpace(got))
		})
	}
}

func TestToMarkdown_CodeFrameCaptions(t *testing.T) {
	const copyButton = `<div class="copy"><div aria-live="polite"></div><button title="Copy to clipboard" data-copied="Copied!" data-code="npm run dev"><div></div></button></div>`
	tests := []struct {
		name string
		html string
		want string
	}{
		{
			name: "TerminalLabelDropped",
			html: `<div class="expressive-code"><figure class="frame is-terminal not-content"><figcaption class="header"><span class="title"></span><span class="sr-only">Terminal window</span></figcaption>` +
				`<pre data-language="sh"><code><div class="ec-line"><div class="code"><span>npm run dev</span></div></div></code></pre>` + copyButton + `</figure></div>`,
			want: "```\nnpm run dev\n```",
		},
		{
			name: "TitleKept",
			html: `<div class="expressive-code"><figure class="frame has-title not-content"><figcaption class="header"><span class="title">astro.config.mjs</span></figcaption>` +
				`<pre data-language="js"><code><div class="ec-line"><div class="code"><span>export default {}</span></div></div></code></pre>` + copyButton + `</figure></div>`,
			want: "astro.config.mjs\n\n```\nexport default {}\n```",
		},
		{
			name: "ScreenReaderTextOutsideCodeFramesKept",
			html: `<figure><img src="a.png" alt="A"><figcaption>Diagram <span class="sr-only">(opens larger)</span></figcaption></figure><p>See <a href="/x">docs<span class="sr-only"> (new tab)</span></a></p>`,
			want: "![A](a.png)\n\nDiagram (opens larger)\n\nSee [docs (new tab)](/x)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ToMarkdown(selectionOf(t, "<main>"+tt.html+"</main>", "main"))
			require.NoError(t, err)
			assert.Equal(t, tt.want, strings.TrimSpace(got))
		})
	}
}

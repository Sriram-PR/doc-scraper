package chunk

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSplitEmptyAndWhitespace(t *testing.T) {
	assert.Empty(t, Split(""))
	assert.Empty(t, Split("   \n\n\t\n"))
}

func TestSplitHeadingPaths(t *testing.T) {
	md := strings.Join([]string{
		"intro paragraph before any heading " + strings.Repeat("x", 300),
		"",
		"# Guide " + strings.Repeat("a", 300),
		"",
		"## Install " + strings.Repeat("b", 300),
		"",
		"### From source " + strings.Repeat("c", 300),
		"",
		"## Configure " + strings.Repeat("d", 300),
	}, "\n")

	chunks := Split(md)
	require.Len(t, chunks, 5)

	assert.Empty(t, chunks[0].HeadingPath)
	assert.True(t, strings.HasPrefix(chunks[1].HeadingPath, "Guide"))
	assert.True(t, strings.HasPrefix(chunks[2].HeadingPath, "Guide") && strings.Contains(chunks[2].HeadingPath, " > Install"))
	assert.Contains(t, chunks[3].HeadingPath, " > Install")
	assert.Contains(t, chunks[3].HeadingPath, " > From source")
	assert.Contains(t, chunks[4].HeadingPath, " > Configure")
	assert.NotContains(t, chunks[4].HeadingPath, "Install")

	for i, c := range chunks {
		assert.Equal(t, i, c.Seq)
	}
}

func TestSplitSiblingHeadingReplacesLevel(t *testing.T) {
	md := "# Top " + strings.Repeat("t", 300) + "\n\n## A " + strings.Repeat("a", 300) + "\n\n## B " + strings.Repeat("b", 300)
	chunks := Split(md)
	require.Len(t, chunks, 3)
	assert.NotContains(t, chunks[2].HeadingPath, "A "+strings.Repeat("a", 3))
}

func TestSplitIgnoresHeadingsInsideFences(t *testing.T) {
	md := strings.Join([]string{
		"## Real heading " + strings.Repeat("r", 300),
		"",
		"```bash",
		"# not a heading, a shell comment",
		"echo hi",
		"```",
		"",
		"trailing text " + strings.Repeat("y", 300),
	}, "\n")

	chunks := Split(md)
	require.Len(t, chunks, 1)
	assert.Contains(t, chunks[0].Text, "# not a heading")
	assert.Contains(t, chunks[0].Text, "trailing text")
}

func TestSplitTildeFence(t *testing.T) {
	md := "~~~\n# hidden\n~~~\n\nafter " + strings.Repeat("z", 300)
	chunks := Split(md)
	require.Len(t, chunks, 1)
	assert.Empty(t, chunks[0].HeadingPath)
}

func TestSplitOversizedSectionAtParagraphs(t *testing.T) {
	para := strings.Repeat("word ", 200) // ~1000 bytes
	md := "# Big " + strings.Repeat("h", 250) + "\n\n" + para + "\n\n" + para + "\n\n" + para

	chunks := Split(md)
	require.Greater(t, len(chunks), 1)
	for _, c := range chunks {
		assert.LessOrEqual(t, len(c.Text), targetMaxBytes+10)
		assert.True(t, strings.HasPrefix(c.HeadingPath, "Big"))
	}
}

func TestSplitGiantParagraphAtLines(t *testing.T) {
	var b strings.Builder
	for range 400 {
		b.WriteString(strings.Repeat("c", 40))
		b.WriteByte('\n')
	}
	chunks := Split("# Code " + strings.Repeat("h", 250) + "\n\n" + strings.TrimSuffix(b.String(), "\n"))
	require.Greater(t, len(chunks), 1)
	for _, c := range chunks {
		assert.LessOrEqual(t, len(c.Text), hardMaxBytes+1)
	}
}

func TestSplitTinyTrailingSectionStandsAloneWithOwnPath(t *testing.T) {
	md := strings.Join([]string{
		"# Main " + strings.Repeat("m", 300),
		"",
		"## See also",
		"",
		"[link](https://example.com)",
	}, "\n")

	chunks := Split(md)
	require.Len(t, chunks, 2)
	assert.Contains(t, chunks[1].Text, "See also")
	assert.Contains(t, chunks[1].HeadingPath, " > See also")
}

func TestSplitFoldsUmbrellaHeadingIntoChild(t *testing.T) {
	md := strings.Join([]string{
		"# Top " + strings.Repeat("t", 300),
		"",
		"## Container",
		"",
		"### Child " + strings.Repeat("c", 300),
		"",
		"## Sibling " + strings.Repeat("s", 300),
	}, "\n")

	chunks := Split(md)
	require.Len(t, chunks, 3)
	childIdx := -1
	for i, c := range chunks {
		if strings.Contains(c.HeadingPath, "Container") {
			if strings.Contains(c.HeadingPath, "Container > Child") {
				childIdx = i
			}
			continue
		}
		assert.NotContains(t, c.Text, "## Container",
			"umbrella heading must not leak into a non-descendant chunk (chunk %d path %q)", i, c.HeadingPath)
	}
	require.GreaterOrEqual(t, childIdx, 0)
	assert.Contains(t, chunks[childIdx].Text, "## Container", "umbrella heading text lives in its child")
}

func TestSplitNestedUmbrellasCascade(t *testing.T) {
	md := "# A\n## B\n### C\n\nbody " + strings.Repeat("x", 300)
	chunks := Split(md)
	require.Len(t, chunks, 1)
	assert.Equal(t, "A > B > C", chunks[0].HeadingPath)
	assert.Contains(t, chunks[0].Text, "# A")
	assert.Contains(t, chunks[0].Text, "## B")
}

func TestSplitIndentedHeadingIsCode(t *testing.T) {
	md := "intro " + strings.Repeat("i", 300) + "\n\n    # not a heading\n\nmore " + strings.Repeat("z", 300)
	chunks := Split(md)
	require.Len(t, chunks, 1)
	assert.Empty(t, chunks[0].HeadingPath)
}

func TestSplitIndentedFenceIsCodeNotFence(t *testing.T) {
	md := strings.Join([]string{
		"para " + strings.Repeat("a", 300),
		"",
		"    ```",
		"",
		"# Real heading " + strings.Repeat("r", 300),
	}, "\n")
	chunks := Split(md)
	require.Len(t, chunks, 2)
	assert.True(t, strings.HasPrefix(chunks[1].HeadingPath, "Real heading"))
}

func TestSplitClosingHashesWithoutSpaceKept(t *testing.T) {
	chunks := Split("## foo##\n\nbody " + strings.Repeat("b", 300))
	require.Len(t, chunks, 1)
	assert.Equal(t, "foo##", chunks[0].HeadingPath)
}

func TestSplitCRLFNormalized(t *testing.T) {
	md := "# H " + strings.Repeat("h", 300) + "\r\n\r\nbody line\r\nsecond line\r\n"
	chunks := Split(md)
	require.NotEmpty(t, chunks)
	for _, c := range chunks {
		assert.NotContains(t, c.Text, "\r")
	}
}

func TestSplitGiantSingleLineBounded(t *testing.T) {
	md := "## Code " + strings.Repeat("h", 260) + "\n\n" + strings.Repeat("中文", 5000)
	chunks := Split(md)
	require.Greater(t, len(chunks), 1)
	for _, c := range chunks {
		assert.LessOrEqual(t, len(c.Text), hardMaxBytes+utf8.UTFMax)
		assert.True(t, utf8.ValidString(c.Text), "rune-safe splitting must preserve UTF-8")
	}
}

func TestSplitFencedBlockNotSplitAtInternalBlankLine(t *testing.T) {
	var b strings.Builder
	b.WriteString("# Console " + strings.Repeat("h", 250) + "\n\n```console\n")
	for range 40 {
		b.WriteString(strings.Repeat("$ command output line ", 3) + "\n\n")
	}
	b.WriteString("```\n")

	chunks := Split(b.String())
	for _, c := range chunks {
		fences := strings.Count("\n"+c.Text, "\n```")
		assert.Equal(t, 0, fences%2, "every chunk must contain balanced fences:\n%s", c.Text)
	}
}

func TestSplitOversizedFencedBlockReopensFence(t *testing.T) {
	var b strings.Builder
	b.WriteString("# Big " + strings.Repeat("h", 250) + "\n\n```go\n")
	for range 300 {
		b.WriteString(strings.Repeat("x", 38) + "\n")
	}
	b.WriteString("```\n")

	chunks := Split(b.String())
	require.Greater(t, len(chunks), 1)
	sawFencePart := false
	for _, c := range chunks {
		if !strings.Contains(c.Text, "```") {
			continue
		}
		sawFencePart = true
		for part := range strings.SplitSeq(c.Text, "\n\n```go\n") {
			_ = part
		}
		assert.LessOrEqual(t, len(c.Text), hardMaxBytes+16)
		assert.Equal(t, 0, strings.Count("\n"+c.Text, "\n```")%2,
			"fence parts must be balanced:\n%.120s", c.Text)
		if idx := strings.Index(c.Text, "```go"); idx >= 0 {
			assert.True(t, strings.HasSuffix(c.Text, "```"), "part with an opener must close")
		}
	}
	assert.True(t, sawFencePart)
}

func TestSplitAnchors(t *testing.T) {
	md := strings.Join([]string{
		"# Getting Started " + strings.Repeat("g", 300),
		"",
		"## From Source " + strings.Repeat("s", 300),
		"",
		"## From Source " + strings.Repeat("t", 300),
	}, "\n")

	chunks := Split(md)
	require.Len(t, chunks, 3)
	assert.True(t, strings.HasPrefix(chunks[0].Anchor, "getting-started"))
	assert.True(t, strings.HasPrefix(chunks[1].Anchor, "from-source"))
	assert.True(t, strings.HasPrefix(chunks[2].Anchor, "from-source"))
	assert.NotEqual(t, chunks[1].Anchor, chunks[2].Anchor, "duplicate headings get distinct anchors")
	assert.Empty(t, Split("no headings here " + strings.Repeat("x", 300))[0].Anchor)
}

func TestIdentifierTokens(t *testing.T) {
	assert.Equal(t, "max Retries", IdentifierTokens("set maxRetries to bound attempts"))
	assert.Contains(t, IdentifierTokens("call getUserById then setTimeout"), "get User By Id")
	assert.Contains(t, IdentifierTokens("call getUserById then setTimeout"), "set Timeout")
	assert.Empty(t, IdentifierTokens("plain prose with snake_case only"))
	assert.Empty(t, IdentifierTokens(""))
}

func TestSplitTinyFirstSectionStandsAlone(t *testing.T) {
	chunks := Split("# Short")
	require.Len(t, chunks, 1)
	assert.Equal(t, "# Short", chunks[0].Text)
}

func TestSection(t *testing.T) {
	md := "intro\n# Guide\ntext\n## Install\nsteps\n```\n# not a heading\n```\n### Linux\napt\n## Install\nagain\n# Next\nend"
	for _, tc := range []struct {
		anchor string
		want   string
	}{
		{"guide", "# Guide\ntext\n## Install\nsteps\n```\n# not a heading\n```\n### Linux\napt\n## Install\nagain\n"},
		{"install", "## Install\nsteps\n```\n# not a heading\n```\n### Linux\napt\n"},
		{"linux", "### Linux\napt\n"},
		{"install-1", "## Install\nagain\n"},
		{"next", "# Next\nend"},
	} {
		start, end, ok := Section(md, tc.anchor)
		require.True(t, ok, tc.anchor)
		assert.Equal(t, tc.want, md[start:end], tc.anchor)
	}
	for _, anchor := range []string{"missing", "not-a-heading", ""} {
		_, _, ok := Section(md, anchor)
		assert.False(t, ok, anchor)
	}
}

// Search results carry Split's anchors; each must resolve to a section.
func TestSectionFindsEverySplitAnchor(t *testing.T) {
	pages := map[string]string{
		"inline": "# A\n\nBody.\n\n## B\n\n" + strings.Repeat("words ", 600) + "\n\n## B\n\nAgain.\n\n### C d\n\n```\n## fenced\n```\n",
	}
	fixtures, err := filepath.Glob(filepath.Join("testdata", "golden", "*.md"))
	require.NoError(t, err)
	for _, f := range fixtures {
		b, err := os.ReadFile(f)
		require.NoError(t, err)
		pages[f] = string(b)
	}
	for name, md := range pages {
		anchors := Anchors(md)
		for _, c := range Split(md) {
			if c.Anchor == "" {
				continue
			}
			assert.Contains(t, anchors, c.Anchor, name)
			start, end, ok := Section(md, c.Anchor)
			require.True(t, ok, "%s: %s", name, c.Anchor)
			assert.Less(t, start, end, "%s: %s", name, c.Anchor)
		}
	}
}

// Cases cite CommonMark 0.31.2 example numbers where one applies.
func TestAnchorsFollowCommonMark(t *testing.T) {
	for _, tc := range []struct {
		name string
		md   string
		want []string
	}{
		{"setext both levels", "Foo\n===\n\nBar\n---\n", []string{"foo", "bar"}},
		{"setext one-char underline [83]", "Foo\n=\n", []string{"foo"}},
		{"setext indented [84]", "   Foo\n---\n\n  Foo\n-----\n\n  Foo\n  ===\n", []string{"foo", "foo-1", "foo-2"}},
		{"setext underline indent and trailing spaces [86]", "Foo\n   ----      \n", []string{"foo"}},
		{"four-space title is code [85]", "    Foo\n    ---\n\n    Foo\n---\n", nil},
		{"four-space underline is text [87]", "Foo\n    ---\n", nil},
		{"multi-line setext title [95]", "Foo\nBar\n---\n", []string{"foo-bar"}},
		{"only the last paragraph", "Foo\n\nbar\n---\n", []string{"bar"}},
		{"underline with inner space [88]", "Foo\n= =\n\nFoo\n--- -\n", nil},
		{"blank line before dashes [104]", "Foo\n\n---\n", nil},
		{"equals after a fence", "```\nx\n```\n===\n", nil},
		{"equals after an ATX heading", "# H\n===\n", []string{"h"}},
		{"lone equals [97]", "====\n", nil},
		{"quote then break [92]", "> Foo\n---\n", nil},
		{"lazy quote line is no underline [93]", "> foo\nbar\n===\n", nil},
		{"list then break [94]", "- Foo\n---\n", nil},
		{"setext inside a quote", "> Foo\n> ---\n", []string{"foo"}},
		{"nested quote", "> > ## Deep\n", []string{"deep"}},
		{"list item headings [300]", "- # Foo\n- Bar\n  ---\n  baz\n", []string{"foo", "bar"}},
		{"converter list heading", "- ## Packages\n\n1. ## Item\n", []string{"packages", "item"}},
		{"converter quote heading", "> ### Each protection type\n", []string{"each-protection-type"}},
		{"quoted fence keeps its comment", "> ```bash\n> # comment\n> ```\n", nil},
		{"unquoted line ends a quoted fence", "> ```\n> l1\nl2\n> ```\n\n# real\n", []string{"real"}},
		{"dashes after a table", "| a |\n| --- |\n| x |\n---\n", nil},
		{"link reference definition is no title [216]", "[foo]: /url\n===\n", nil},
		{"paragraph after a link reference [215]", "[foo]: /url\nbar\n===\n", []string{"bar"}},
		{"html block hides a hash line", "<div>\n# not a heading\n</div>\n", nil},
		{"hash without space", "#not-a-heading\n", nil},
		{"seven hashes", "####### seven\n", nil},
		{"closing hashes", "## Closing hashes ##\n", []string{"closing-hashes"}},
		{"tab after hashes", "#\tTab\n", []string{"tab"}},
		{"crlf", "Foo\r\n===\r\n\r\n# Bar\r\n", []string{"foo", "bar"}},
	} {
		assert.Equal(t, tc.want, Anchors(tc.md), tc.name)
	}
}

func TestSectionStartsAtTheHeadingLine(t *testing.T) {
	md := "Intro\n\nFoo\nBar\n---\ntext\n\n- ## Item\n  body\n\n> # Quoted\n> more\n"
	for _, tc := range []struct{ anchor, want string }{
		{"foo-bar", "Foo\nBar\n---\ntext\n\n"},
		{"item", "- ## Item\n  body\n\n"},
		{"quoted", "> # Quoted\n> more\n"},
	} {
		start, end, ok := Section(md, tc.anchor)
		require.True(t, ok, tc.anchor)
		assert.Equal(t, tc.want, md[start:end], tc.anchor)
	}
}

func TestSplitNestedHeadingJoinsTheHierarchy(t *testing.T) {
	md := "### Step\n\n" + strings.Repeat("step body ", 40) + "\n\n> ## Note\n>\n> " + strings.Repeat("quoted ", 60) + "\n"
	chunks := Split(md)
	require.Len(t, chunks, 2)
	assert.Equal(t, "Step", chunks[0].HeadingPath)
	assert.Equal(t, "Note", chunks[1].HeadingPath)
	assert.Equal(t, "note", chunks[1].Anchor)

	start, end, ok := Section(md, "step")
	require.True(t, ok)
	assert.Equal(t, md[:strings.Index(md, "> ## Note")], md[start:end])
}

// A tab split by a list's content indent leaves virtual spaces that
// goldmark's Pos() counts as bytes; the heading must stay on its own line.
func TestHeadingStartIgnoresTabPadding(t *testing.T) {
	hs := headings("* 0\n\t#\nnext\n")
	require.Len(t, hs, 1)
	assert.Equal(t, 4, hs[0].start)
}

func TestSplitHeadingPathIsPlainText(t *testing.T) {
	md := "# Guide [home](http://example.com/)\n\n## Use `run()` and *care*\n\n" + strings.Repeat("body ", 80) + "\n"
	chunks := Split(md)
	require.Len(t, chunks, 1)
	assert.Equal(t, "Guide home > Use run() and care", chunks[0].HeadingPath)
	assert.Equal(t, "use-run-and-care", chunks[0].Anchor)
	assert.Equal(t, []string{"guide-home", "use-run-and-care"}, Anchors(md))
}

// Expected values are github-slugger 2.0.0's output for the same headings.
func TestAnchorsMatchGitHubSlugger(t *testing.T) {
	page := func(hs ...string) string {
		var b strings.Builder
		for _, h := range hs {
			b.WriteString("## " + h + "\n\n")
		}
		return b.String()
	}
	for _, tc := range []struct {
		name string
		md   string
		want []string
	}{
		{"literal suffix after a generated one", page("A", "A", "A-1", "A", "A-1"), []string{"a", "a-1", "a-1-1", "a-2", "a-1-2"}},
		{"echo fixture", page("echo", "echo", "echo 1", "echo-1", "echo"), []string{"echo", "echo-1", "echo-1-1", "echo-1-2", "echo-2"}},
		{"foxtrot fixture", page("foxtrot-1", "foxtrot", "foxtrot"), []string{"foxtrot-1", "foxtrot", "foxtrot-2"}},
		{"punctuation and symbols", page("A/AAAA records", "C++ & C#", "$HOME", "1.2.3"), []string{"aaaaa-records", "c--c", "home", "123"}},
		{"combining marks", page("हिन्दी", "école"), []string{"हिन्दी", "école"}},
		{"letter numbers and connectors", page("Ⅷ roman", "‿x"), []string{"ⅷ-roman", "‿x"}},
		{"empty slugs are not anchors", page("😀", "!!!", "😀"), nil},
	} {
		assert.Equal(t, tc.want, Anchors(tc.md), tc.name)
	}
}

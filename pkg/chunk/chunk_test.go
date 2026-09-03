package chunk

import (
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

func TestParseHeadingEdgeCases(t *testing.T) {
	_, _, ok := parseHeading("#not-a-heading")
	assert.False(t, ok)

	_, _, ok = parseHeading("####### seven")
	assert.False(t, ok)

	level, text, ok := parseHeading("## Closing hashes ##")
	assert.True(t, ok)
	assert.Equal(t, 2, level)
	assert.Equal(t, "Closing hashes", text)
}

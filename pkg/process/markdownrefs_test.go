package process

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExtractHeadings_BasicMarkdown(t *testing.T) {
	markdown := []byte(`# Main Title

Some intro text.

## Section One

Content here.

### Subsection A

More content.

## Section Two

Final content.
`)

	headings := ExtractHeadings(markdown)

	assert.Equal(t, []string{
		"Main Title",
		"Section One",
		"Subsection A",
		"Section Two",
	}, headings)
}

func TestExtractHeadings_Empty(t *testing.T) {
	markdown := []byte(`Just plain text without any headings.`)

	headings := ExtractHeadings(markdown)

	assert.Empty(t, headings)
}

func TestExtractHeadings_OnlyH1(t *testing.T) {
	markdown := []byte(`# Single Title`)

	headings := ExtractHeadings(markdown)

	assert.Equal(t, []string{"Single Title"}, headings)
}

func TestExtractHeadings_AllLevels(t *testing.T) {
	markdown := []byte(`# H1
## H2
### H3
#### H4
##### H5
###### H6
`)

	headings := ExtractHeadings(markdown)

	assert.Equal(t, []string{"H1", "H2", "H3", "H4", "H5", "H6"}, headings)
}

func TestExtractHeadings_EmptyDocument(t *testing.T) {
	markdown := []byte(``)

	headings := ExtractHeadings(markdown)

	assert.Empty(t, headings)
}

func TestExtractLinksAndImages(t *testing.T) {
	tests := []struct {
		name           string
		markdown       string
		expectedLinks  []string
		expectedImages []string
	}{
		{
			name: "basic links and images",
			markdown: `# Title
Here is a [link](https://example.com) and an ![image](./images/photo.png).
Another [link2](./page.md) here.`,
			expectedLinks:  []string{"https://example.com", "./page.md"},
			expectedImages: []string{"./images/photo.png"},
		},
		{
			name:           "no links or images",
			markdown:       "Just plain text without any links or images.",
			expectedLinks:  nil,
			expectedImages: nil,
		},
		{
			name: "multiple images",
			markdown: `![alt1](img1.png)
![alt2](img2.jpg)
![](img3.gif)`,
			expectedLinks:  nil,
			expectedImages: []string{"img1.png", "img2.jpg", "img3.gif"},
		},
		{
			name: "mixed content",
			markdown: `# Documentation

See the [API reference](./api.md) for details.

![diagram](./images/arch.svg)

For more info, visit [our site](https://docs.example.com).
`,
			expectedLinks:  []string{"./api.md", "https://docs.example.com"},
			expectedImages: []string{"./images/arch.svg"},
		},
		{
			name:           "adjacent links",
			markdown:       "[a](x)[b](y)",
			expectedLinks:  []string{"x", "y"},
			expectedImages: nil,
		},
		{
			name:           "link immediately after image",
			markdown:       "![img](i)[b](y)",
			expectedLinks:  []string{"y"},
			expectedImages: []string{"i"},
		},
		{
			name:           "mixed adjacent link and image",
			markdown:       "[a](x)![img](i)[b](y)",
			expectedLinks:  []string{"x", "y"},
			expectedImages: []string{"i"},
		},
		{
			name:           "empty markdown",
			markdown:       "",
			expectedLinks:  nil,
			expectedImages: nil,
		},
		{
			name:           "link with spaces in URL",
			markdown:       "[doc](./my%20document.md)",
			expectedLinks:  []string{"./my%20document.md"},
			expectedImages: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			links, images := ExtractLinksAndImages(tt.markdown)
			assert.Equal(t, tt.expectedLinks, links)
			assert.Equal(t, tt.expectedImages, images)
		})
	}
}

// The regex extractors the scanners replaced, verbatim: the fuzz oracle.
var (
	refHeadingRe = regexp.MustCompile(`(?m)^(#{1,6})\s+(.+)$`)
	refLinkRe    = regexp.MustCompile(`\[([^\]]*)\]\(([^)]+)\)`)
	refImageRe   = regexp.MustCompile(`!\[([^\]]*)\]\(([^)]+)\)`)
)

func refHeadings(markdown []byte) []string {
	matches := refHeadingRe.FindAllSubmatch(markdown, -1)
	if len(matches) == 0 {
		return nil
	}
	headings := make([]string, 0, len(matches))
	for _, m := range matches {
		if len(m) >= 3 {
			text := strings.TrimSpace(string(m[2]))
			if text != "" {
				headings = append(headings, text)
			}
		}
	}
	return headings
}

func refLinksAndImages(markdown string) (links []string, images []string) {
	for _, match := range refLinkRe.FindAllStringSubmatchIndex(markdown, -1) {
		if match[0] > 0 && markdown[match[0]-1] == '!' {
			continue
		}
		if u := strings.TrimSpace(markdown[match[4]:match[5]]); u != "" {
			links = append(links, u)
		}
	}
	for _, match := range refImageRe.FindAllStringSubmatch(markdown, -1) {
		if u := strings.TrimSpace(match[2]); u != "" {
			images = append(images, u)
		}
	}
	return links, images
}

var markdownRefsSeeds = []string{
	"# a\n## b",
	"#\n# x",
	"#######  x\n# y",
	"#  ",
	"# \n\n",
	"#\n\n",
	"x\n#\t\r\nfoo",
	"#\f\n",
	"# \r",
	"#\vx\n# y",
	"# a\r\n## b\r\n",
	"[a](b)[c](d)",
	"![a](b)",
	"[![i](i.png)](l)",
	"[a]()(b)",
	"[a]( )",
	"[a](\nb)",
	"[a\nb](c)",
	"[[a](b)",
	"![a ![b](c)",
	"[a](b",
	"[]](x)",
	"![](x)[](y)",
	"\xff# x\n# \xfe\n[\xff](\xfe)",
}

// FuzzMarkdownRefsMatchRegex: the scanners return exactly what the regexes
// did, nil versus empty included, since JSONL writes them as null versus [].
func FuzzMarkdownRefsMatchRegex(f *testing.F) {
	for _, s := range markdownRefsSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if got, want := ExtractHeadings([]byte(s)), refHeadings([]byte(s)); !reflect.DeepEqual(got, want) {
			t.Fatalf("headings of %q: got %#v, want %#v", s, got, want)
		}
		gotL, gotI := ExtractLinksAndImages(s)
		wantL, wantI := refLinksAndImages(s)
		if !reflect.DeepEqual(gotL, wantL) || !reflect.DeepEqual(gotI, wantI) {
			t.Fatalf("links/images of %q: got %#v %#v, want %#v %#v", s, gotL, gotI, wantL, wantI)
		}
	})
}

func benchDocPage(sections int) string {
	var b strings.Builder
	for i := range sections {
		fmt.Fprintf(&b, "## Section %d\n\nSome prose with a [link %d](../page-%d.md) and `code`.\n\n"+
			"![fig %d](images/fig-%d.png)\n\n~~~go\n// comment\nx := []int{%d}\n~~~\n\n", i, i, i, i, i, i)
	}
	return b.String()
}

func BenchmarkMarkdownRefs(b *testing.B) {
	for _, p := range []struct{ name, md string }{
		{"small", benchDocPage(3)},
		{"median", benchDocPage(30)},
		{"large", benchDocPage(600)},
		{"brackets", strings.Repeat("[`kw`](kw.md#a) [x] ", 2000)},
	} {
		md := []byte(p.md)
		b.Run(p.name+"/scan", func(b *testing.B) {
			b.SetBytes(int64(len(md)))
			b.ReportAllocs()
			for b.Loop() {
				ExtractHeadings(md)
				ExtractLinksAndImages(p.md)
			}
		})
		b.Run(p.name+"/regex", func(b *testing.B) {
			b.SetBytes(int64(len(md)))
			b.ReportAllocs()
			for b.Loop() {
				refHeadings(md)
				refLinksAndImages(p.md)
			}
		})
	}
}

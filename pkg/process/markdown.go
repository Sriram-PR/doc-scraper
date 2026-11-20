package process

import (
	"fmt"
	"strings"
	"sync"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/strikethrough"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/table"
	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// MarkdownFormatVersion is part of the crawler's incremental content hash.
// Bump it whenever a change here alters the Markdown for the same HTML, so the
// next incremental crawl reconverts pages whose HTML did not change.
const MarkdownFormatVersion = 5

// A Converter is safe to share, but every call takes its mutex, and workers
// spending their time on that mutex stops a crawl scaling past about 8 of them.
var markdownConverters = sync.Pool{New: func() any {
	return converter.NewConverter(converter.WithPlugins(
		base.NewBasePlugin(),
		commonmark.NewCommonmarkPlugin(commonmark.WithListEndComment(false)),
		table.NewTablePlugin(),
		strikethrough.NewStrikethroughPlugin(),
		tablesPlugin{},
		elementRenderers{},
	))
}}

// ToMarkdown converts every node in sel to GitHub-flavored Markdown. It
// converts a copy, so sel is left as it was.
func ToMarkdown(sel *goquery.Selection) (string, error) {
	container := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
	for _, n := range sel.Clone().Nodes {
		dropBells(n)
		dropCodeFrameCaptions(n)
		flattenCodeBlocks(n)
		container.AppendChild(n)
	}
	conv := markdownConverters.Get().(*converter.Converter)
	defer markdownConverters.Put(conv)
	out, err := conv.ConvertNode(container)
	if err != nil {
		return "", fmt.Errorf("converting HTML to Markdown: %w", err)
	}
	return string(out), nil
}

// dropBells removes BEL from text: the converter uses it as its escape marker,
// so a BEL before a "|" in a table cell left the pipe bare and broke the row.
func dropBells(n *html.Node) {
	if n.Type == html.TextNode && strings.ContainsRune(n.Data, '\a') {
		n.Data = strings.ReplaceAll(n.Data, "\a", "")
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		dropBells(c)
	}
}

// dropCodeFrameCaptions removes screen-reader-only labels, such as Expressive
// Code's "Terminal window", from the captions of figures that hold code.
func dropCodeFrameCaptions(n *html.Node) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		dropCodeFrameCaptions(c)
	}
	if !isElement(n, "figure") || firstElement(n, "pre") == nil {
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if isElement(c, "figcaption") {
			removeScreenReaderOnly(c)
		}
	}
}

func removeScreenReaderOnly(n *html.Node) {
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		if hasClass(c, "sr-only") {
			n.RemoveChild(c)
		} else {
			removeScreenReaderOnly(c)
		}
		c = next
	}
}

// flattenCodeBlocks replaces the children of each <pre> that wraps lines in block
// elements with their text: the converter breaks the line at every <div> and <br>.
func flattenCodeBlocks(n *html.Node) {
	if isElement(n, "pre") {
		if hasLineMarkup(n) {
			flattenPre(n)
		}
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		flattenCodeBlocks(c)
	}
}

func hasLineMarkup(n *html.Node) bool {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && (c.Data == "br" || codeBlockBreaks[c.Data]) || hasLineMarkup(c) {
			return true
		}
	}
	return false
}

func flattenPre(pre *html.Node) {
	var b strings.Builder
	appendCodeText(&b, pre)
	text := &html.Node{Type: html.TextNode, Data: b.String()}
	var replacement *html.Node
	if code := firstElement(pre, "code"); code != nil {
		replacement = &html.Node{Type: html.ElementNode, Data: "code", DataAtom: atom.Code}
		if class := attr(code, "class"); class != "" {
			replacement.Attr = []html.Attribute{{Key: "class", Val: class}}
		}
		replacement.AppendChild(text)
	} else {
		replacement = text
	}
	for pre.FirstChild != nil {
		pre.RemoveChild(pre.FirstChild)
	}
	pre.AppendChild(replacement)
}

// codeBlockBreaks are the elements that start a new line inside a <pre>, as
// they would when rendered.
var codeBlockBreaks = map[string]bool{
	"address": true, "article": true, "aside": true, "blockquote": true, "details": true, "dd": true,
	"div": true, "dl": true, "dt": true, "figcaption": true, "figure": true, "footer": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true, "header": true, "hr": true,
	"li": true, "main": true, "nav": true, "ol": true, "p": true, "pre": true, "section": true,
	"summary": true, "table": true, "tr": true, "ul": true,
}

// appendCodeText writes n's text as rendered: a block element starts a line
// only when the current one has text, so a <br> ending a <div> line doesn't
// add an empty line.
func appendCodeText(b *strings.Builder, n *html.Node) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		switch {
		case c.Type == html.TextNode:
			b.WriteString(c.Data)
		case c.Type != html.ElementNode: // comments
		case isElement(c, "script", "style", "textarea", "template"):
		case c.Data == "br":
			b.WriteByte('\n')
		case codeBlockBreaks[c.Data]:
			endLine(b)
			appendCodeText(b, c)
			endLine(b)
		default:
			appendCodeText(b, c)
		}
	}
}

func endLine(b *strings.Builder) {
	if s := b.String(); s != "" && s[len(s)-1] != '\n' {
		b.WriteByte('\n')
	}
}

// elementRenderers is the one renderer for every element we render ourselves.
// The library copies and sorts its handler list for each node it renders, so
// each extra handler slowed every page by about 7%.
type elementRenderers struct{}

func (elementRenderers) Name() string { return "element-renderers" }

func (elementRenderers) Init(conv *converter.Converter) error {
	// v2 has no plugin for task-list checkboxes, and its base plugin removes
	// every <input> otherwise.
	conv.Register.TagType("input", converter.TagTypeInline, converter.PriorityEarly)
	conv.Register.Renderer(renderElement, converter.PriorityEarly)
	return nil
}

func renderElement(ctx converter.Context, w converter.Writer, n *html.Node) converter.RenderStatus {
	switch {
	case isElement(n, "input"):
		return renderCheckbox(ctx, w, n)
	case isElement(n, "td", "th"):
		return renderKeptCell(ctx, w, n)
	}
	return converter.RenderTryNext
}

func renderCheckbox(_ converter.Context, w converter.Writer, n *html.Node) converter.RenderStatus {
	var isCheckbox, checked bool
	for _, a := range n.Attr {
		switch a.Key {
		case "type":
			isCheckbox = strings.EqualFold(a.Val, "checkbox")
		case "checked":
			checked = true
		}
	}
	if !isCheckbox {
		return converter.RenderSuccess
	}
	if checked {
		_, _ = w.WriteString("[x] ")
	} else {
		_, _ = w.WriteString("[ ] ")
	}
	return converter.RenderSuccess
}

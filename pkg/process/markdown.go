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
const MarkdownFormatVersion = 3

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

package process

import (
	"fmt"
	"strings"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/strikethrough"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/table"
	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// The library guards a Converter with a mutex, so crawl workers share one.
var markdownConverter = converter.NewConverter(converter.WithPlugins(
	base.NewBasePlugin(),
	commonmark.NewCommonmarkPlugin(commonmark.WithListEndComment(false)),
	table.NewTablePlugin(),
	strikethrough.NewStrikethroughPlugin(),
	taskListPlugin{},
))

// ToMarkdown converts every node in sel to GitHub-flavored Markdown. It
// converts a copy, so sel is left as it was.
func ToMarkdown(sel *goquery.Selection) (string, error) {
	container := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
	for _, n := range sel.Clone().Nodes {
		container.AppendChild(n)
	}
	out, err := markdownConverter.ConvertNode(container)
	if err != nil {
		return "", fmt.Errorf("converting HTML to Markdown: %w", err)
	}
	return string(out), nil
}

// taskListPlugin renders task-list checkboxes. v2 has no plugin for them, and
// its base plugin removes every <input> otherwise.
type taskListPlugin struct{}

func (taskListPlugin) Name() string { return "task-list" }

func (taskListPlugin) Init(conv *converter.Converter) error {
	conv.Register.TagType("input", converter.TagTypeInline, converter.PriorityEarly)
	conv.Register.RendererFor("input", converter.TagTypeInline, renderCheckbox, converter.PriorityEarly)
	return nil
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
	switch {
	case !isCheckbox:
	case checked:
		_, _ = w.WriteString("[x] ")
	default:
		_, _ = w.WriteString("[ ] ")
	}
	return converter.RenderSuccess
}

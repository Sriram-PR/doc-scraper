package process

import (
	"strings"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// tablesPlugin decides what every table becomes before the stock table plugin
// sees it: line-numbered code becomes a code block.
type tablesPlugin struct{}

func (tablesPlugin) Name() string { return "tables" }

// Between base's element removal (PriorityEarly) and its whitespace collapse
// (PriorityLate), and off PriorityStandard: the library's handler sort is not
// stable, so a tie with another plugin's pre-renderer would run in any order.
const tablesPreRenderPriority = converter.PriorityStandard - 100

func (tablesPlugin) Init(conv *converter.Converter) error {
	conv.Register.PreRenderer(rewriteTables, tablesPreRenderPriority)
	return nil
}

func rewriteTables(_ converter.Context, doc *html.Node) {
	for _, t := range tablesInnermostFirst(doc) {
		if code, ok := codeTable(t); ok {
			replaceWithCode(t, code)
		}
	}
}

func tablesInnermostFirst(doc *html.Node) []*html.Node {
	var out []*html.Node
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
		if isElement(n, "table") {
			out = append(out, n)
		}
	}
	visit(doc)
	return out
}

type codeBlock struct {
	text     string
	filename string
	language string
}

// codeTable recognizes a highlighter's line-numbers-plus-code table: Pygments
// and pymdownx (td.linenos, td.code), Rouge (td.gutter or td.rouge-gutter),
// Chroma with classes (td.lntd), or by shape for Chroma's inline styles.
func codeTable(t *html.Node) (codeBlock, bool) {
	rows := ownRows(t)
	var filename string
	if len(rows) == 2 {
		if cs := ownCells(rows[0]); len(cs) == 1 && isElement(cs[0], "th") && attr(cs[0], "colspan") == "2" {
			filename = strings.TrimSpace(textOf(cs[0]))
			rows = rows[1:]
		}
	}
	if len(rows) != 1 {
		return codeBlock{}, false
	}
	cs := ownCells(rows[0])
	if len(cs) != 2 || !isGutterAndCode(cs[0], cs[1]) {
		return codeBlock{}, false
	}
	pre := firstElement(cs[1], "pre")
	if pre == nil {
		return codeBlock{}, false
	}
	text := strings.TrimPrefix(codeText(pre), "\n")
	text = strings.TrimSuffix(text, "\n")
	return codeBlock{text: text, filename: filename, language: codeLanguage(t, cs[1], pre)}, true
}

func isGutterAndCode(gutter, code *html.Node) bool {
	switch {
	case hasClass(gutter, "linenos") && hasClass(code, "code"),
		(hasClass(gutter, "gutter") || hasClass(gutter, "rouge-gutter")) && (hasClass(code, "code") || hasClass(code, "rouge-code")),
		hasClass(gutter, "lntd") && hasClass(code, "lntd"):
		return true
	}
	if firstElement(gutter, "table") != nil || firstElement(code, "table") != nil {
		return false
	}
	numbers := firstElement(gutter, "pre")
	return numbers != nil && firstElement(code, "pre") != nil && onlyLineNumbers(textOf(numbers))
}

// onlyLineNumbers: every line is blank or ASCII digits (linenostep leaves
// blank lines), and at least one is digits.
func onlyLineNumbers(s string) bool {
	digits := false
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		for _, r := range line {
			if r < '0' || r > '9' {
				return false
			}
		}
		digits = true
	}
	return digits
}

// codeLanguage looks for language-X, lang-X or highlight-X (Sphinx) on the
// code, then outward to the table's great-grandparent.
func codeLanguage(t, codeCell, pre *html.Node) string {
	candidates := []*html.Node{firstElement(pre, "code"), pre, codeCell, t}
	for p, i := t.Parent, 0; p != nil && i < 3; p, i = p.Parent, i+1 {
		candidates = append(candidates, p)
	}
	for _, n := range candidates {
		if n == nil {
			continue
		}
		for _, class := range strings.Fields(attr(n, "class")) {
			for _, prefix := range []string{"language-", "lang-", "highlight-"} {
				lang, ok := strings.CutPrefix(class, prefix)
				if !ok || lang == "" || (prefix == "highlight-" && (lang == "default" || lang == "none")) {
					continue
				}
				return lang
			}
		}
	}
	return ""
}

func replaceWithCode(t *html.Node, cb codeBlock) {
	if cb.filename != "" {
		p := &html.Node{Type: html.ElementNode, Data: "p", DataAtom: atom.P}
		p.AppendChild(&html.Node{Type: html.TextNode, Data: cb.filename})
		t.Parent.InsertBefore(p, t)
	}
	code := &html.Node{Type: html.ElementNode, Data: "code", DataAtom: atom.Code}
	if cb.language != "" {
		code.Attr = []html.Attribute{{Key: "class", Val: "language-" + cb.language}}
	}
	code.AppendChild(&html.Node{Type: html.TextNode, Data: cb.text})
	pre := &html.Node{Type: html.ElementNode, Data: "pre", DataAtom: atom.Pre}
	pre.AppendChild(code)
	t.Parent.InsertBefore(pre, t)
	t.Parent.RemoveChild(t)
}

// ownRows returns the table's rows, not those of tables nested in it.
func ownRows(t *html.Node) []*html.Node {
	var rows []*html.Node
	for c := t.FirstChild; c != nil; c = c.NextSibling {
		switch {
		case isElement(c, "tr"):
			rows = append(rows, c)
		case isElement(c, "thead", "tbody", "tfoot"):
			for r := c.FirstChild; r != nil; r = r.NextSibling {
				if isElement(r, "tr") {
					rows = append(rows, r)
				}
			}
		}
	}
	return rows
}

func ownCells(tr *html.Node) []*html.Node {
	var cells []*html.Node
	for c := tr.FirstChild; c != nil; c = c.NextSibling {
		if isElement(c, "td", "th") {
			cells = append(cells, c)
		}
	}
	return cells
}

func isElement(n *html.Node, names ...string) bool {
	if n == nil || n.Type != html.ElementNode {
		return false
	}
	for _, name := range names {
		if n.Data == name {
			return true
		}
	}
	return false
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func hasClass(n *html.Node, class string) bool {
	for _, c := range strings.Fields(attr(n, "class")) {
		if c == class {
			return true
		}
	}
	return false
}

func firstElement(n *html.Node, name string) *html.Node {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if isElement(c, name) {
			return c
		}
		if found := firstElement(c, name); found != nil {
			return found
		}
	}
	return nil
}

func textOf(n *html.Node) string {
	var sb strings.Builder
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n.Type == html.TextNode {
			sb.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(n)
	return sb.String()
}

// codeText is textOf without aria-hidden subtrees.
func codeText(n *html.Node) string {
	var sb strings.Builder
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n.Type == html.TextNode {
			sb.WriteString(n.Data)
		}
		if n.Type == html.ElementNode && attr(n, "aria-hidden") == "true" {
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(n)
	return sb.String()
}

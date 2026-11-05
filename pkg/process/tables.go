package process

import (
	"bytes"
	"regexp"
	"strings"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// tablesPlugin decides what every table becomes before the stock table plugin
// sees it: line-numbered code becomes a code block, a table whose cells fit on
// one line each stays a table, and anything else becomes plain blocks.
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

// keptAttr marks the tables left for the stock table plugin. The DOM is a
// copy made by ToMarkdown, so the marker never reaches the page.
const keptAttr = "data-doc-scraper-kept"

// maxCellLines keeps layout tables (navigation, one-column function lists)
// out of GFM: a cell with more lines than this becomes unreadable on one row.
const maxCellLines = 4

func rewriteTables(_ converter.Context, doc *html.Node) {
	tables := tablesInnermostFirst(doc)
	// Decided before any rewrite, so an outer table still sees its nested
	// tables as tables.
	codes := make(map[*html.Node]codeBlock)
	keep := make(map[*html.Node]bool)
	for _, t := range tables {
		if code, ok := codeTable(t); ok {
			codes[t] = code
		} else {
			keep[t] = keepAsTable(t)
		}
	}
	for _, t := range tables {
		switch code, isCode := codes[t]; {
		case isCode:
			replaceWithCode(t, code)
		case keep[t]:
			t.Attr = append(t.Attr, html.Attribute{Key: keptAttr})
			prepareKeptCells(t)
		default:
			rewriteFallback(t)
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

// keepAsTable reports whether t can be a GFM table: every cell fits on one
// row, and the stock table plugin would not refuse it (2_collect.go).
func keepAsTable(t *html.Node) bool {
	if attr(t, "role") == "presentation" || insideInlineFormatting(t) || hasBlockDescendant(t) {
		return false
	}
	rows := ownRows(t)
	if len(rows) == 0 {
		return false
	}
	for _, r := range rows {
		for _, c := range ownCells(r) {
			if cellLines(c) > maxCellLines {
				return false
			}
		}
	}
	return true
}

func insideInlineFormatting(n *html.Node) bool {
	for p := n.Parent; p != nil; p = p.Parent {
		if isElement(p, "a", "strong", "b", "em", "i", "del", "s", "strike") {
			return true
		}
	}
	return false
}

func hasBlockDescendant(t *html.Node) bool {
	var found bool
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		for c := n.FirstChild; c != nil && !found; c = c.NextSibling {
			if isElement(c, "table", "ul", "ol", "dl", "hr", "h1", "h2", "h3", "h4", "h5", "h6", "blockquote", "figure", "details") ||
				(isElement(c, "pre") && !isOneLinePre(c)) {
				found = true
				return
			}
			visit(c)
		}
	}
	visit(t)
	return found
}

func isOneLinePre(pre *html.Node) bool {
	return !strings.Contains(strings.TrimSuffix(textOf(pre), "\n"), "\n")
}

// cellLines counts a cell's non-empty lines the way renderKeptCell splits
// them: at <br>, and around <p> and <div>.
func cellLines(cell *html.Node) int {
	lines, current := 0, false
	end := func() {
		if current {
			lines++
		}
		current = false
	}
	var visit func(*html.Node)
	visit = func(parent *html.Node) {
		for c := parent.FirstChild; c != nil; c = c.NextSibling {
			switch {
			case isElement(c, "br"):
				end()
			case isElement(c, "p", "div"):
				end()
				visit(c)
				end()
			case strings.TrimSpace(textOf(c)) != "" || isElement(c, "img") || firstElement(c, "img") != nil:
				current = true
			}
		}
	}
	visit(cell)
	end()
	return lines
}

// prepareKeptCells turns a one-line <pre> into inline code and escapes "|" in
// code spans and in image alt and link or image titles: GFM needs "\|" there
// too, and the stock plugin only escapes text, which the converter writes
// through its escaping step; code, alt and titles are written as they are.
func prepareKeptCells(t *html.Node) {
	for _, r := range ownRows(t) {
		for _, cell := range ownCells(r) {
			var pres, codes []*html.Node
			var visit func(*html.Node)
			visit = func(n *html.Node) {
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					escapePipesInAttrs(c)
					switch {
					case isElement(c, "pre"):
						pres = append(pres, c)
					case isElement(c, "code", "kbd", "samp", "tt", "var"):
						codes = append(codes, c)
					default:
						visit(c)
					}
				}
			}
			visit(cell)
			for _, pre := range pres {
				code := &html.Node{Type: html.ElementNode, Data: "code", DataAtom: atom.Code}
				code.AppendChild(&html.Node{Type: html.TextNode, Data: strings.TrimSpace(codeText(pre))})
				pre.Parent.InsertBefore(code, pre)
				pre.Parent.RemoveChild(pre)
				codes = append(codes, code)
			}
			for _, code := range codes {
				escapePipes(code)
			}
		}
	}
}

func escapePipesInAttrs(n *html.Node) {
	if !isElement(n, "a", "img") {
		return
	}
	for i, a := range n.Attr {
		if a.Key == "title" || (a.Key == "alt" && n.Data == "img") {
			n.Attr[i].Val = strings.ReplaceAll(a.Val, "|", `\|`)
		}
	}
}

func escapePipes(n *html.Node) {
	if n.Type == html.TextNode {
		n.Data = strings.ReplaceAll(n.Data, "|", `\|`)
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		escapePipes(c)
	}
}

var newlineRun = regexp.MustCompile(`[ \t]*\n[ \t]*`)

// renderKeptCell writes a kept table's cell on one line, its lines joined by
// <br>, so the stock plugin never sees a newline and gives up on the table.
// Called from renderElement.
func renderKeptCell(ctx converter.Context, w converter.Writer, n *html.Node) converter.RenderStatus {
	if !inKeptTable(n) {
		return converter.RenderTryNext
	}
	var lines [][]byte
	var current bytes.Buffer
	end := func() {
		line := bytes.TrimSpace(newlineRun.ReplaceAll(current.Bytes(), []byte(" ")))
		if len(line) > 0 {
			lines = append(lines, bytes.Clone(line))
		}
		current.Reset()
	}
	var visit func(*html.Node)
	visit = func(parent *html.Node) {
		for c := parent.FirstChild; c != nil; c = c.NextSibling {
			switch {
			case isElement(c, "br"):
				end()
			case isElement(c, "p", "div"):
				end()
				visit(c)
				end()
			default:
				ctx.RenderNodes(ctx, &current, c)
			}
		}
	}
	visit(n)
	end()
	_, _ = w.Write(bytes.Join(lines, []byte("<br>")))
	return converter.RenderSuccess
}

func inKeptTable(cell *html.Node) bool {
	for p := cell.Parent; p != nil; p = p.Parent {
		if isElement(p, "table") {
			for _, a := range p.Attr {
				if a.Key == keptAttr {
					return true
				}
			}
			return false
		}
	}
	return false
}

// rewriteFallback replaces t with one block per row and one per cell, so cells
// no longer run together when the table cannot be a GFM table. Inside a link or
// emphasis a block would break the inline, so there the cells are spans
// separated by spaces.
func rewriteFallback(t *html.Node) {
	box := func() *html.Node { return newElement("div", atom.Div) }
	inline := insideInlineFormatting(t)
	if inline {
		box = func() *html.Node { return newElement("span", atom.Span) }
	}
	out := box()
	add := func(parent, child *html.Node) {
		if inline && parent.FirstChild != nil {
			parent.AppendChild(&html.Node{Type: html.TextNode, Data: " "})
		}
		parent.AppendChild(child)
	}
	if caption := firstChildElement(t, "caption"); caption != nil {
		add(out, moveChildren(caption, box()))
	}
	for _, r := range ownRows(t) {
		row := box()
		for _, cell := range ownCells(r) {
			add(row, moveChildren(cell, box()))
		}
		add(out, row)
	}
	t.Parent.InsertBefore(out, t)
	t.Parent.RemoveChild(t)
}

func newElement(name string, a atom.Atom) *html.Node {
	return &html.Node{Type: html.ElementNode, Data: name, DataAtom: a}
}

func moveChildren(from, to *html.Node) *html.Node {
	for c := from.FirstChild; c != nil; {
		next := c.NextSibling
		from.RemoveChild(c)
		to.AppendChild(c)
		c = next
	}
	return to
}

func firstChildElement(n *html.Node, name string) *html.Node {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if isElement(c, name) {
			return c
		}
	}
	return nil
}

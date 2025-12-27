package process

import (
	"bytes"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// ExtractHeadings parses markdown content and extracts all heading texts.
// Returns a slice of heading strings in document order.
func ExtractHeadings(markdown []byte) []string {
	reader := text.NewReader(markdown)
	parser := goldmark.DefaultParser()
	doc := parser.Parse(reader)

	var headings []string
	ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if heading, ok := n.(*ast.Heading); ok {
			var buf bytes.Buffer
			for child := heading.FirstChild(); child != nil; child = child.NextSibling() {
				if textNode, ok := child.(*ast.Text); ok {
					buf.Write(textNode.Segment.Value(markdown))
				}
			}
			if buf.Len() > 0 {
				headings = append(headings, buf.String())
			}
		}
		return ast.WalkContinue, nil
	})

	return headings
}

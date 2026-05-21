package process

import (
	"regexp"
	"strings"
)

// headingRegex matches markdown ATX headings at the start of lines. Group 1 is
// the hash prefix (#-######), group 2 is the heading text up to end of line.
var headingRegex = regexp.MustCompile(`(?m)^(#{1,6})\s+(.+)$`)

// ExtractHeadings returns all ATX heading texts in markdown in document order.
// Does not parse Setext-style headings or detect headings inside fenced code
// blocks — acceptable for the inputs we feed it (HTML-to-markdown conversion
// output, where Setext headings do not occur).
func ExtractHeadings(markdown []byte) []string {
	matches := headingRegex.FindAllSubmatch(markdown, -1)
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

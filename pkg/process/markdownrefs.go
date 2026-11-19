package process

import (
	"regexp"
	"strings"
)

// headingRegex matches markdown ATX headings at the start of lines. Group 1 is
// the hash prefix (#-######), group 2 is the heading text up to end of line.
var (
	headingRegex = regexp.MustCompile(`(?m)^(#{1,6})\s+(.+)$`)
	mdLinkRe     = regexp.MustCompile(`\[([^\]]*)\]\(([^)]+)\)`)
	mdImageRe    = regexp.MustCompile(`!\[([^\]]*)\]\(([^)]+)\)`)
)

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

// ExtractLinksAndImages extracts markdown links and image references from markdown content.
// Returns two slices: links (from [text](url)) and images (from ![alt](url)).
func ExtractLinksAndImages(markdown string) (links []string, images []string) {
	linkMatches := mdLinkRe.FindAllStringSubmatchIndex(markdown, -1)
	for _, match := range linkMatches {
		if match[0] > 0 && markdown[match[0]-1] == '!' {
			continue
		}
		linkURL := strings.TrimSpace(markdown[match[4]:match[5]])
		if linkURL != "" {
			links = append(links, linkURL)
		}
	}

	imageMatches := mdImageRe.FindAllStringSubmatch(markdown, -1)
	for _, match := range imageMatches {
		if len(match) >= 3 {
			imageURL := strings.TrimSpace(match[2])
			if imageURL != "" {
				images = append(images, imageURL)
			}
		}
	}

	return links, images
}

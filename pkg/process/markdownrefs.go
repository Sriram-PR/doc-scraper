package process

import (
	"bytes"
	"regexp"
	"strings"
)

var (
	mdLinkRe  = regexp.MustCompile(`\[([^\]]*)\]\(([^)]+)\)`)
	mdImageRe = regexp.MustCompile(`!\[([^\]]*)\]\(([^)]+)\)`)
)

// isRegexSpace is RE2's \s, [\t\n\f\r ]: no \v.
func isRegexSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\f' || c == '\r'
}

// ExtractHeadings returns the ATX heading texts in markdown in document order.
// It reproduces `(?m)^(#{1,6})\s+(.+)$` exactly, including \s+ running across
// blank lines, and returns an empty non-nil slice when only blank headings
// matched, so JSONL keeps writing [] for those pages. Setext headings and code
// fences are not recognized.
func ExtractHeadings(markdown []byte) []string {
	md := markdown
	n := len(md)
	var out []string
	matched := false
	for i := 0; i < n; {
		if md[i] != '#' {
			j := bytes.IndexByte(md[i:], '\n')
			if j < 0 {
				break
			}
			i += j + 1
			continue
		}
		k := 0
		for i+k < n && md[i+k] == '#' {
			k++
		}
		p := i + k
		if k > 6 || p >= n || !isRegexSpace(md[p]) {
			j := bytes.IndexByte(md[p:], '\n')
			if j < 0 {
				break
			}
			i = p + j + 1
			continue
		}
		q := p
		for q < n && isRegexSpace(md[q]) {
			q++
		}
		if q == n {
			// \s+ gives bytes back until .+ can start on a non-newline.
			q = -1
			for x := n - 1; x > p; x-- {
				if md[x] != '\n' {
					q = x
					break
				}
			}
			if q < 0 {
				break
			}
		}
		e := n
		if j := bytes.IndexByte(md[q:], '\n'); j >= 0 {
			e = q + j
		}
		matched = true
		if t := strings.TrimSpace(string(md[q:e])); t != "" {
			out = append(out, t)
		}
		i = e + 1
	}
	if matched && out == nil {
		return []string{}
	}
	return out
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

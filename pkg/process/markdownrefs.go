package process

import (
	"strings"
)

// ExtractLinksAndImages matches `\[([^\]]*)\]\(([^)]+)\)` (skipping ones right
// after "!") and `!\[([^\]]*)\]\(([^)]+)\)` exactly, returning trimmed targets.
func ExtractLinksAndImages(markdown string) (links, images []string) {
	for pos := 0; ; {
		s, target, next, ok := nextBracketRef(markdown, pos, "[")
		if !ok {
			break
		}
		if s == 0 || markdown[s-1] != '!' {
			if t := strings.TrimSpace(target); t != "" {
				links = append(links, t)
			}
		}
		pos = next
	}
	for pos := 0; ; {
		_, target, next, ok := nextBracketRef(markdown, pos, "![")
		if !ok {
			break
		}
		if t := strings.TrimSpace(target); t != "" {
			images = append(images, t)
		}
		pos = next
	}
	return links, images
}

// nextBracketRef finds the leftmost match of open + `[^\]]*\]\([^)]+\)` at or
// after pos, returning where it starts, its target, and where the next search
// starts.
func nextBracketRef(md string, pos int, open string) (start int, target string, next int, ok bool) {
	for {
		k := strings.Index(md[pos:], open)
		if k < 0 {
			return 0, "", 0, false
		}
		s := pos + k
		c := strings.IndexByte(md[s+len(open):], ']')
		if c < 0 {
			return 0, "", 0, false
		}
		e := s + len(open) + c
		if e+1 < len(md) && md[e+1] == '(' {
			r := strings.IndexByte(md[e+2:], ')')
			if r < 0 {
				return 0, "", 0, false
			}
			if r > 0 {
				return s, md[e+2 : e+2+r], e + 3 + r, true
			}
		}
		// Every later open before e reaches this same ']' and fails the same way.
		pos = e + 1
	}
}

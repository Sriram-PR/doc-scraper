package process

import (
	"bytes"
	"strings"
)

// isRegexSpace is RE2's \s, [\t\n\f\r ]: no \v.
func isRegexSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\f' || c == '\r'
}

// ExtractHeadings matches `(?m)^(#{1,6})\s+(.+)$` exactly, \s+ crossing lines
// included; only blank matches give an empty non-nil slice, which JSONL writes as [].
func ExtractHeadings(md []byte) []string {
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

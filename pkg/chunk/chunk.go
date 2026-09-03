// Package chunk splits a page's markdown into heading-anchored sections for
// full-text indexing. Chunks are the unit of search: small enough that a BM25
// hit lands near the relevant text, large enough to carry usable context.
package chunk

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Chunk is one indexable section of a page. HeadingPath is the " > "-joined
// chain of ancestor headings (empty for content before the first heading);
// Anchor is the GitHub-style URL fragment of the section's own heading,
// deduplicated per page; Seq preserves document order within the page.
type Chunk struct {
	HeadingPath string
	Anchor      string
	Seq         int
	Text        string
}

const (
	// targetMaxBytes caps normal chunk growth. Sections larger than this are
	// split at paragraph boundaries; ~2KB keeps a chunk within a few hundred
	// tokens so ranked results stay precise.
	targetMaxBytes = 2048

	// hardMaxBytes bounds a single unbreakable paragraph (typically a huge
	// code block). Beyond this the content is split at line boundaries, and a
	// single monster line is split at rune boundaries.
	hardMaxBytes = 6144

	// minChunkBytes is the fold threshold: a section smaller than this whose
	// heading introduces the section that follows (an "umbrella" heading with
	// no body of its own) is folded into that child, so the heading text is
	// attributed to a path that contains it. Tiny parts split from the same
	// section merge into their predecessor.
	minChunkBytes = 256

	// maxIndent is CommonMark's limit for headings and fences: 4+ spaces of
	// indentation means an indented code block, not structure.
	maxIndent = 3
)

// Split breaks markdown into ordered chunks. Headings inside fenced code
// blocks are not treated as section boundaries, and a fenced block is never
// split at a blank line; when one exceeds the hard size cap it is split at
// line boundaries with the fence closed and reopened across the cut so every
// chunk remains valid markdown on its own.
func Split(markdown string) []Chunk {
	sections := splitSections(markdown)
	sections = foldUmbrellaSections(sections)

	var chunks []Chunk
	for _, sec := range sections {
		path := strings.Join(sec.path, " > ")
		for _, part := range mergeTinyParts(packSection(sec.lines)) {
			chunks = append(chunks, Chunk{HeadingPath: path, Anchor: sec.anchor, Text: part})
		}
	}
	for i := range chunks {
		chunks[i].Seq = i
	}
	return chunks
}

// mergeTinyParts absorbs sub-minChunkBytes parts of one section into their
// neighbors: the first part (typically a bare heading stranded by an oversized
// unit after it) merges forward, later ones merge backward. A merge is skipped
// when it would push the receiver past hardMaxBytes.
func mergeTinyParts(parts []string) []string {
	cleaned := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			cleaned = append(cleaned, p)
		}
	}
	out := cleaned[:0]
	for i := 0; i < len(cleaned); i++ {
		p := cleaned[i]
		if len(p) >= minChunkBytes {
			out = append(out, p)
			continue
		}
		switch {
		case len(out) > 0 && len(out[len(out)-1])+2+len(p) <= hardMaxBytes:
			out[len(out)-1] += "\n\n" + p
		case i+1 < len(cleaned) && len(p)+2+len(cleaned[i+1]) <= hardMaxBytes:
			cleaned[i+1] = p + "\n\n" + cleaned[i+1]
		default:
			out = append(out, p)
		}
	}
	return out
}

type section struct {
	path   []string
	anchor string
	lines  []string
}

func (s *section) size() int {
	n := 0
	for _, l := range s.lines {
		n += len(strings.TrimSpace(l))
	}
	return n
}

func splitSections(markdown string) []section {
	var (
		sections = []section{{}}
		stack    []headingFrame
		inFence  bool
		fenceTok string
		slugs    = map[string]int{}
	)

	for line := range strings.SplitSeq(markdown, "\n") {
		line = strings.TrimSuffix(line, "\r")
		trimmed, structural := structuralText(line)

		if structural {
			if tok, rest := fenceToken(trimmed); tok != "" {
				if !inFence {
					inFence, fenceTok = true, tok
				} else if tok[0] == fenceTok[0] && len(tok) >= len(fenceTok) && strings.TrimSpace(rest) == "" {
					inFence = false
				}
				cur := &sections[len(sections)-1]
				cur.lines = append(cur.lines, line)
				continue
			}

			if level, text, ok := parseHeading(trimmed); ok && !inFence {
				for len(stack) > 0 && stack[len(stack)-1].level >= level {
					stack = stack[:len(stack)-1]
				}
				stack = append(stack, headingFrame{level: level, text: text})
				var parts []string
				for _, f := range stack {
					if f.text != "" {
						parts = append(parts, f.text)
					}
				}
				sections = append(sections, section{
					path:   parts,
					anchor: dedupedSlug(text, slugs),
					lines:  []string{line},
				})
				continue
			}
		}

		cur := &sections[len(sections)-1]
		cur.lines = append(cur.lines, line)
	}
	return sections
}

// foldUmbrellaSections merges a heading-only section into the child section
// that follows it, so container headings ("## Concepts" straight into "###
// First concept") live in a chunk whose path actually contains them. Folding
// re-examines the merged result, so nested umbrellas cascade.
func foldUmbrellaSections(sections []section) []section {
	out := sections[:0]
	for i := range sections {
		sec := sections[i]
		if i+1 < len(sections) && len(sec.path) > 0 && sec.size() < minChunkBytes && pathExtends(sections[i+1].path, sec.path) {
			sections[i+1].lines = append(append([]string{}, sec.lines...), sections[i+1].lines...)
			continue
		}
		out = append(out, sec)
	}
	return out
}

func pathExtends(child, parent []string) bool {
	if len(child) <= len(parent) {
		return false
	}
	for i := range parent {
		if child[i] != parent[i] {
			return false
		}
	}
	return true
}

type headingFrame struct {
	level int
	text  string
}

// structuralText returns the line with up to maxIndent leading spaces removed
// and whether the line may carry block structure at all: CommonMark treats 4+
// spaces (or a leading tab) as an indented code block, where '#' and fences
// are literal text.
func structuralText(line string) (string, bool) {
	n := 0
	for n < len(line) && line[n] == ' ' {
		n++
	}
	if n > maxIndent || (n < len(line) && line[n] == '\t') {
		return line, false
	}
	return line[n:], true
}

// fenceToken returns the leading run of backticks or tildes when the line
// opens or closes a fenced code block (3+ per CommonMark) plus the rest of
// the line, else ("", "").
func fenceToken(trimmed string) (string, string) {
	for _, ch := range []byte{'`', '~'} {
		n := 0
		for n < len(trimmed) && trimmed[n] == ch {
			n++
		}
		if n >= 3 {
			return trimmed[:n], trimmed[n:]
		}
	}
	return "", ""
}

func parseHeading(trimmed string) (level int, text string, ok bool) {
	n := 0
	for n < len(trimmed) && trimmed[n] == '#' {
		n++
	}
	if n == 0 || n > 6 {
		return 0, "", false
	}
	rest := trimmed[n:]
	if rest != "" && rest[0] != ' ' && rest[0] != '\t' {
		return 0, "", false
	}
	text = strings.TrimSpace(rest)
	// A closing run of #s only counts when preceded by a space ("## foo ##");
	// "## foo##" keeps its hashes per CommonMark.
	if i := strings.LastIndexFunc(text, func(r rune) bool { return r != '#' }); i >= 0 {
		if i+1 < len(text) && text[i] == ' ' {
			text = strings.TrimSpace(text[:i])
		}
	} else {
		text = ""
	}
	return n, text, true
}

// dedupedSlug builds a GitHub-style anchor for a heading, appending -1, -2,
// ... for repeated headings in document order, matching how renderers keep
// duplicate anchors distinct.
func dedupedSlug(heading string, seen map[string]int) string {
	slug := slugify(heading)
	if slug == "" {
		return ""
	}
	n := seen[slug]
	seen[slug] = n + 1
	if n > 0 {
		return slug + "-" + itoa(n)
	}
	return slug
}

func slugify(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
	}
	return b.String()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// unit is an atomic run of lines for size-splitting: either one prose
// paragraph or one whole fenced code block (blank lines inside included).
type unit struct {
	lines   []string
	fenced  bool
	opener  string
	fenceCh byte
}

func packSection(lines []string) []string {
	units := atomicUnits(lines)

	var (
		out []string
		cur []string
		n   int
	)
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.Join(cur, "\n\n"))
			cur, n = nil, 0
		}
	}
	for _, u := range units {
		text := strings.Join(u.lines, "\n")
		if n > 0 && n+2+len(text) > targetMaxBytes {
			flush()
		}
		if len(text) > hardMaxBytes {
			flush()
			if u.fenced {
				out = append(out, splitFencedBlock(u)...)
			} else {
				out = append(out, splitAtLines(u.lines)...)
			}
			continue
		}
		cur = append(cur, text)
		n += 2 + len(text)
	}
	flush()
	return out
}

func atomicUnits(lines []string) []unit {
	var (
		units []unit
		cur   unit
	)
	flush := func() {
		if len(cur.lines) > 0 {
			units = append(units, cur)
		}
		cur = unit{}
	}
	for _, line := range lines {
		trimmed, structural := structuralText(line)
		if cur.fenced {
			cur.lines = append(cur.lines, line)
			if structural {
				if tok, rest := fenceToken(trimmed); tok != "" && tok[0] == cur.fenceCh && strings.TrimSpace(rest) == "" {
					flush()
				}
			}
			continue
		}
		if structural {
			if tok, _ := fenceToken(trimmed); tok != "" {
				flush()
				cur = unit{lines: []string{line}, fenced: true, opener: line, fenceCh: tok[0]}
				continue
			}
		}
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		cur.lines = append(cur.lines, line)
	}
	flush()
	return units
}

// splitFencedBlock breaks an oversized fenced block at line boundaries,
// closing the fence at each cut and reopening it (with its info string) in
// the continuation so every part renders as a valid, self-contained block.
func splitFencedBlock(u unit) []string {
	closer := strings.Repeat(string(u.fenceCh), 3)
	budget := hardMaxBytes - len(u.opener) - len(closer) - 2
	if budget < 1 {
		budget = 1
	}

	body := u.lines[1:]
	if len(body) > 0 {
		if trimmed, ok := structuralText(body[len(body)-1]); ok {
			if tok, rest := fenceToken(trimmed); tok != "" && tok[0] == u.fenceCh && strings.TrimSpace(rest) == "" {
				body = body[:len(body)-1]
			}
		}
	}

	groups := packLines(body, budget)
	if len(groups) == 0 {
		groups = [][]string{{}}
	}
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		part := u.opener + "\n" + strings.Join(g, "\n") + "\n" + closer
		out = append(out, part)
	}
	return out
}

func splitAtLines(lines []string) []string {
	groups := packLines(lines, hardMaxBytes)
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		out = append(out, strings.Join(g, "\n"))
	}
	return out
}

// packLines groups lines under max bytes each, splitting a single line
// longer than max at rune boundaries so the bound holds for any input.
func packLines(lines []string, max int) [][]string {
	var (
		out [][]string
		n   int
	)
	cur := make([]string, 0, len(lines))
	flush := func() {
		if len(cur) > 0 {
			out = append(out, cur)
			cur, n = nil, 0
		}
	}
	for _, line := range lines {
		for len(line) > max {
			flush()
			cut := cutPoint(line, max)
			out = append(out, []string{line[:cut]})
			line = line[cut:]
		}
		if n > 0 && n+1+len(line) > max {
			flush()
		}
		cur = append(cur, line)
		n += 1 + len(line)
	}
	flush()
	return out
}

// cutPoint picks where to split an overlong line: after the last whitespace
// rune inside the window, so words survive the cut whenever the line has any
// whitespace; a whitespace-free run (minified content) falls back to a
// rune-boundary cut, where splitting some token is unavoidable.
func cutPoint(line string, max int) int {
	window := line[:max]
	for len(window) > 0 {
		r, size := utf8.DecodeLastRuneInString(window)
		if unicode.IsSpace(r) {
			if len(window) > size {
				return len(window)
			}
			break
		}
		window = window[:len(window)-size]
	}
	return runeSafeCut(line, max)
}

// runeSafeCut returns the largest cut point <= max that does not land inside
// a multibyte rune. Invalid UTF-8 falls back to cutting at max.
func runeSafeCut(s string, max int) int {
	cut := max
	for back := 0; back < utf8.UTFMax && cut-back > 0; back++ {
		if utf8.RuneStart(s[cut-back-1]) {
			r, size := utf8.DecodeRuneInString(s[cut-back-1:])
			if r != utf8.RuneError || size > 1 {
				if cut-back-1+size <= max {
					return cut - back - 1 + size
				}
				return cut - back - 1
			}
			break
		}
	}
	return cut
}

// IdentifierTokens extracts camelCase identifiers from text and returns their
// split-word forms ("maxRetries" -> "max Retries"), space-joined and
// deduplicated. FTS5's unicode61 tokenizer splits snake_case on its own but
// keeps camelCase fused; indexing these shadow tokens lets a "max retries"
// query match prose that only says maxRetries.
func IdentifierTokens(text string) string {
	var (
		out  []string
		seen = map[string]struct{}{}
		word []rune
	)
	flushWord := func() {
		if len(word) == 0 {
			return
		}
		if split, ok := splitCamel(word); ok {
			if _, dup := seen[split]; !dup {
				seen[split] = struct{}{}
				out = append(out, split)
			}
		}
		word = word[:0]
	}
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			word = append(word, r)
		} else {
			flushWord()
		}
	}
	flushWord()
	return strings.Join(out, " ")
}

func splitCamel(word []rune) (string, bool) {
	var b strings.Builder
	hasSplit := false
	for i, r := range word {
		if i > 0 && unicode.IsUpper(r) && unicode.IsLower(word[i-1]) {
			b.WriteByte(' ')
			hasSplit = true
		}
		b.WriteRune(r)
	}
	if !hasSplit {
		return "", false
	}
	return b.String(), true
}

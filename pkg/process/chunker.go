package process

import (
	"strings"
)

// Chunk represents a single chunk of content with its metadata.
type Chunk struct {
	Content          string   // The chunk content (includes heading context when HeadingHierarchy is enabled)
	HeadingHierarchy []string // Extracted heading hierarchy from the chunk
	TokenCount       int      // Token count for this chunk
}

// ChunkerConfig holds configuration for the chunker.
type ChunkerConfig struct {
	MaxChunkSize int // Maximum chunk size in tokens (triggers recursive split if exceeded)
	ChunkOverlap int // Overlap between chunks in tokens (for recursive fallback)
}

// DefaultChunkerConfig returns sensible defaults for RAG chunking.
func DefaultChunkerConfig() ChunkerConfig {
	return ChunkerConfig{
		MaxChunkSize: 512,
		ChunkOverlap: 50,
	}
}

// ChunkMarkdown splits markdown content into chunks using a hybrid strategy:
//  1. Primary: split at ATX markdown headers so each chunk carries an
//     identifiable heading context.
//  2. Fallback for any section that exceeds MaxChunkSize tokens: recursive
//     separator-based packing (paragraph, line, sentence, word, then rune)
//     with ChunkOverlap tokens of overlap between consecutive sub-chunks.
//
// The tokenizer set up by InitTokenizer is the authority on token counts; if
// it is not initialized, an approximate "1 token per 4 characters" heuristic
// is used so chunking still produces sensible boundaries.
func ChunkMarkdown(markdown string, cfg ChunkerConfig) ([]Chunk, error) {
	if markdown == "" {
		return nil, nil
	}
	if cfg.MaxChunkSize <= 0 {
		cfg.MaxChunkSize = 512
	}
	if cfg.ChunkOverlap < 0 {
		cfg.ChunkOverlap = 0
	}
	if cfg.ChunkOverlap >= cfg.MaxChunkSize {
		// Overlap larger than chunk size would loop; clamp to half.
		cfg.ChunkOverlap = cfg.MaxChunkSize / 2
	}

	var parts []string
	for _, section := range splitByHeadings(markdown) {
		if effectiveTokenCount(section) <= cfg.MaxChunkSize {
			parts = append(parts, section)
			continue
		}
		parts = append(parts, splitWithOverlap(section, cfg.MaxChunkSize, cfg.ChunkOverlap)...)
	}

	chunks := make([]Chunk, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			continue
		}
		chunks = append(chunks, Chunk{
			Content:          part,
			HeadingHierarchy: ExtractHeadings([]byte(part)),
			TokenCount:       CountTokens(part),
		})
	}
	return chunks, nil
}

// effectiveTokenCount returns CountTokens, or an approximate 1-token-per-4-char
// estimate when the tokenizer is not initialized (CountTokens returns -1).
// The estimate keeps chunking sensible in tokenizer-disabled deployments.
func effectiveTokenCount(text string) int {
	if n := CountTokens(text); n >= 0 {
		return n
	}
	return len(text) / 4
}

// splitByHeadings breaks markdown at ATX heading lines (# .. ######). Each
// returned section starts at a heading line (or at document start for the
// preamble) and extends up to the next heading line.
func splitByHeadings(markdown string) []string {
	indices := headingRegex.FindAllStringIndex(markdown, -1)
	if len(indices) == 0 {
		return []string{markdown}
	}

	var sections []string
	if indices[0][0] > 0 {
		preamble := strings.TrimRight(markdown[:indices[0][0]], "\n")
		if strings.TrimSpace(preamble) != "" {
			sections = append(sections, preamble)
		}
	}
	for i, idx := range indices {
		start := idx[0]
		end := len(markdown)
		if i+1 < len(indices) {
			end = indices[i+1][0]
		}
		section := strings.TrimRight(markdown[start:end], "\n")
		if strings.TrimSpace(section) != "" {
			sections = append(sections, section)
		}
	}
	return sections
}

// splitWithOverlap performs recursive separator-based splitting and packs
// segments until the token budget is reached.
func splitWithOverlap(text string, maxTokens, overlap int) []string {
	if effectiveTokenCount(text) <= maxTokens {
		return []string{text}
	}
	return splitBySeparators(text, []string{"\n\n", "\n", ". ", " "}, maxTokens, overlap)
}

// splitBySeparators tries the first separator in seps; if a piece between
// separators is itself too large, falls through to the next separator level.
// When all separators are exhausted, hardSplit takes over.
func splitBySeparators(text string, seps []string, maxTokens, overlap int) []string {
	if effectiveTokenCount(text) <= maxTokens {
		return []string{text}
	}
	if len(seps) == 0 {
		return hardSplit(text, maxTokens, overlap)
	}
	sep := seps[0]
	rest := seps[1:]
	if !strings.Contains(text, sep) {
		return splitBySeparators(text, rest, maxTokens, overlap)
	}

	pieces := strings.Split(text, sep)
	var chunks []string
	var current strings.Builder

	flush := func() {
		if current.Len() > 0 {
			chunks = append(chunks, current.String())
			current.Reset()
		}
	}

	for _, piece := range pieces {
		// Oversized piece: flush, then recursively split this piece itself.
		if effectiveTokenCount(piece) > maxTokens {
			flush()
			chunks = append(chunks, splitBySeparators(piece, rest, maxTokens, overlap)...)
			continue
		}

		// Probe whether adding piece keeps us under budget.
		var candidate string
		if current.Len() == 0 {
			candidate = piece
		} else {
			candidate = current.String() + sep + piece
		}

		if effectiveTokenCount(candidate) <= maxTokens {
			if current.Len() > 0 {
				current.WriteString(sep)
			}
			current.WriteString(piece)
			continue
		}

		// Budget exceeded: flush current, seed new buffer with overlap tail
		// of the just-flushed chunk for context continuity.
		flush()
		if overlap > 0 && len(chunks) > 0 {
			if tail := tailByTokens(chunks[len(chunks)-1], overlap); tail != "" {
				current.WriteString(tail)
				current.WriteString(sep)
			}
		}
		current.WriteString(piece)
	}
	flush()
	return chunks
}

// hardSplit is the last-resort fallback: rune-boundary slicing when no
// structural separator helps. Uses a coarse 4-chars-per-token estimate to size
// the initial window and shrinks until the window fits the token budget.
func hardSplit(text string, maxTokens, overlap int) []string {
	if maxTokens <= 0 {
		return []string{text}
	}
	runes := []rune(text)
	if len(runes) == 0 {
		return nil
	}

	var chunks []string
	start := 0
	for start < len(runes) {
		end := start + maxTokens*4
		if end > len(runes) {
			end = len(runes)
		}
		for end > start+1 && effectiveTokenCount(string(runes[start:end])) > maxTokens {
			end = start + (end-start)*3/4
			if end <= start {
				end = start + 1
				break
			}
		}
		chunks = append(chunks, string(runes[start:end]))
		if end >= len(runes) {
			break
		}
		overlapChars := overlap * 4
		if overlapChars >= end-start {
			overlapChars = (end - start) / 2
		}
		start = end - overlapChars
	}
	return chunks
}

// tailByTokens returns the trailing substring of text whose token count is
// approximately tokens, used to seed overlap between consecutive chunks.
func tailByTokens(text string, tokens int) string {
	if tokens <= 0 {
		return ""
	}
	if effectiveTokenCount(text) <= tokens {
		return text
	}
	runes := []rune(text)
	start := len(runes) - tokens*4
	if start < 0 {
		start = 0
	}
	for start > 0 && effectiveTokenCount(string(runes[start:])) < tokens {
		step := tokens
		if step < 4 {
			step = 4
		}
		start -= step
		if start < 0 {
			start = 0
		}
	}
	return string(runes[start:])
}

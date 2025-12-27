package process

import (
	"regexp"
	"strings"

	"github.com/tmc/langchaingo/textsplitter"
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

// headingRegex matches markdown headings at the start of lines.
var headingRegex = regexp.MustCompile(`(?m)^(#{1,6})\s+(.+)$`)

// ChunkMarkdown splits markdown content into chunks using a hybrid strategy:
// 1. Primary: Split by markdown headers, preserving heading hierarchy
// 2. Fallback: If any chunk exceeds maxChunkSize, apply recursive character splitting
//
// Each chunk includes parent heading context prepended for RAG retrieval.
func ChunkMarkdown(markdown string, cfg ChunkerConfig) ([]Chunk, error) {
	if markdown == "" {
		return nil, nil
	}

	// Use token-aware length function if tokenizer is initialized
	lenFunc := func(s string) int {
		return CountTokens(s)
	}

	// Create recursive splitter for fallback on oversized chunks
	recursiveSplitter := textsplitter.NewRecursiveCharacter(
		textsplitter.WithChunkSize(cfg.MaxChunkSize),
		textsplitter.WithChunkOverlap(cfg.ChunkOverlap),
		textsplitter.WithLenFunc(lenFunc),
	)

	// Primary splitter: markdown headers with hierarchy tracking
	// Falls back to recursive splitter for chunks that are still too large
	splitter := textsplitter.NewMarkdownTextSplitter(
		textsplitter.WithHeadingHierarchy(true),
		textsplitter.WithChunkSize(cfg.MaxChunkSize),
		textsplitter.WithChunkOverlap(cfg.ChunkOverlap),
		textsplitter.WithSecondSplitter(recursiveSplitter),
		textsplitter.WithLenFunc(lenFunc),
	)

	// Split the markdown
	parts, err := splitter.SplitText(markdown)
	if err != nil {
		return nil, err
	}

	// Convert to Chunk structs with extracted metadata
	chunks := make([]Chunk, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			continue
		}

		chunk := Chunk{
			Content:          part,
			HeadingHierarchy: extractHeadingHierarchy(part),
			TokenCount:       CountTokens(part),
		}
		chunks = append(chunks, chunk)
	}

	return chunks, nil
}

// extractHeadingHierarchy extracts the heading hierarchy from chunk content.
// Returns headings in order from highest level (h1) to lowest (h6).
func extractHeadingHierarchy(content string) []string {
	matches := headingRegex.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return nil
	}

	// Track headings by level to build hierarchy
	hierarchy := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) >= 3 {
			heading := strings.TrimSpace(match[2])
			if heading != "" {
				hierarchy = append(hierarchy, heading)
			}
		}
	}

	return hierarchy
}

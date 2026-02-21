package process

import (
	"strings"
	"testing"
)

func TestChunkMarkdown_Empty(t *testing.T) {
	chunks, err := ChunkMarkdown("", DefaultChunkerConfig())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks for empty input, got %d", len(chunks))
	}
}

func TestChunkMarkdown_SingleSmallChunk(t *testing.T) {
	markdown := `# Hello

This is a small document.`

	cfg := ChunkerConfig{
		MaxChunkSize: 512,
		ChunkOverlap: 50,
	}

	chunks, err := ChunkMarkdown(markdown, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(chunks) != 1 {
		t.Errorf("expected 1 chunk for small document, got %d", len(chunks))
	}

	if len(chunks) > 0 {
		if !strings.Contains(chunks[0].Content, "Hello") {
			t.Errorf("expected chunk to contain 'Hello', got: %s", chunks[0].Content)
		}
	}
}

func TestChunkMarkdown_HeaderHierarchy(t *testing.T) {
	markdown := `# Main Title

Introduction paragraph.

## Section One

Content for section one.

### Subsection 1.1

More detailed content here.

## Section Two

Content for section two.
`

	cfg := ChunkerConfig{
		MaxChunkSize: 100,
		ChunkOverlap: 10,
	}

	chunks, err := ChunkMarkdown(markdown, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(chunks) < 2 {
		t.Errorf("expected multiple chunks, got %d", len(chunks))
	}

	// Check that heading hierarchy is extracted
	foundWithHierarchy := false
	for _, chunk := range chunks {
		if len(chunk.HeadingHierarchy) > 0 {
			foundWithHierarchy = true
			break
		}
	}
	if !foundWithHierarchy {
		t.Error("expected at least one chunk with heading hierarchy")
	}
}

func TestChunkMarkdown_TokenCount(t *testing.T) {
	// Initialize tokenizer so CountTokens returns real values
	err := InitTokenizer("cl100k_base")
	if err != nil {
		t.Fatalf("failed to initialize tokenizer: %v", err)
	}

	markdown := `# Test Document

This is a test document with some content that should be counted for tokens.
`

	cfg := DefaultChunkerConfig()
	chunks, err := ChunkMarkdown(markdown, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(chunks) == 0 {
		t.Fatal("expected at least 1 chunk")
	}

	// Token count should be positive when tokenizer is initialized
	if chunks[0].TokenCount <= 0 {
		t.Errorf("expected positive token count, got %d", chunks[0].TokenCount)
	}
}

func TestChunkMarkdown_LargeDocument(t *testing.T) {
	// Create a document that will definitely need multiple chunks
	var sb strings.Builder
	sb.WriteString("# Large Document\n\n")
	for i := range 50 {
		sb.WriteString("## Section ")
		sb.WriteString(string(rune('A' + i%26)))
		sb.WriteString("\n\n")
		sb.WriteString("This is paragraph content that adds up to create a larger document. ")
		sb.WriteString("We need enough text to trigger the chunking logic and split into multiple chunks. ")
		sb.WriteString("The quick brown fox jumps over the lazy dog repeatedly.\n\n")
	}

	cfg := ChunkerConfig{
		MaxChunkSize: 100, // Small chunk size to force splitting
		ChunkOverlap: 10,
	}

	chunks, err := ChunkMarkdown(sb.String(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(chunks) < 5 {
		t.Errorf("expected many chunks for large document, got %d", len(chunks))
	}
}

func TestExtractHeadingHierarchy(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected []string
	}{
		{
			name:     "no headings",
			content:  "Just some text without headings.",
			expected: nil,
		},
		{
			name:     "single heading",
			content:  "# Main Title\nSome content",
			expected: []string{"Main Title"},
		},
		{
			name:     "multiple headings",
			content:  "# Title\n## Section\n### Subsection\nContent",
			expected: []string{"Title", "Section", "Subsection"},
		},
		{
			name:     "heading with special chars",
			content:  "# Hello, World!\n## API Reference: v2.0",
			expected: []string{"Hello, World!", "API Reference: v2.0"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractHeadingHierarchy(tt.content)
			if len(result) != len(tt.expected) {
				t.Errorf("expected %d headings, got %d: %v", len(tt.expected), len(result), result)
				return
			}
			for i, heading := range result {
				if heading != tt.expected[i] {
					t.Errorf("heading %d: expected %q, got %q", i, tt.expected[i], heading)
				}
			}
		})
	}
}

func TestDefaultChunkerConfig(t *testing.T) {
	cfg := DefaultChunkerConfig()

	if cfg.MaxChunkSize != 512 {
		t.Errorf("expected MaxChunkSize 512, got %d", cfg.MaxChunkSize)
	}
	if cfg.ChunkOverlap != 50 {
		t.Errorf("expected ChunkOverlap 50, got %d", cfg.ChunkOverlap)
	}
}

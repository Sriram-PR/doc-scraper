package process

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExtractHeadings_BasicMarkdown(t *testing.T) {
	markdown := []byte(`# Main Title

Some intro text.

## Section One

Content here.

### Subsection A

More content.

## Section Two

Final content.
`)

	headings := ExtractHeadings(markdown)

	assert.Equal(t, []string{
		"Main Title",
		"Section One",
		"Subsection A",
		"Section Two",
	}, headings)
}

func TestExtractHeadings_Empty(t *testing.T) {
	markdown := []byte(`Just plain text without any headings.`)

	headings := ExtractHeadings(markdown)

	assert.Empty(t, headings)
}

func TestExtractHeadings_OnlyH1(t *testing.T) {
	markdown := []byte(`# Single Title`)

	headings := ExtractHeadings(markdown)

	assert.Equal(t, []string{"Single Title"}, headings)
}

func TestExtractHeadings_AllLevels(t *testing.T) {
	markdown := []byte(`# H1
## H2
### H3
#### H4
##### H5
###### H6
`)

	headings := ExtractHeadings(markdown)

	assert.Equal(t, []string{"H1", "H2", "H3", "H4", "H5", "H6"}, headings)
}

func TestExtractHeadings_EmptyDocument(t *testing.T) {
	markdown := []byte(``)

	headings := ExtractHeadings(markdown)

	assert.Empty(t, headings)
}

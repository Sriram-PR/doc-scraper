package crawler

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExtractLinksAndImages(t *testing.T) {
	tests := []struct {
		name           string
		markdown       string
		expectedLinks  []string
		expectedImages []string
	}{
		{
			name: "basic links and images",
			markdown: `# Title
Here is a [link](https://example.com) and an ![image](./images/photo.png).
Another [link2](./page.md) here.`,
			expectedLinks:  []string{"https://example.com", "./page.md"},
			expectedImages: []string{"./images/photo.png"},
		},
		{
			name:           "no links or images",
			markdown:       "Just plain text without any links or images.",
			expectedLinks:  nil,
			expectedImages: nil,
		},
		{
			name: "multiple images",
			markdown: `![alt1](img1.png)
![alt2](img2.jpg)
![](img3.gif)`,
			expectedLinks:  nil,
			expectedImages: []string{"img1.png", "img2.jpg", "img3.gif"},
		},
		{
			name: "mixed content",
			markdown: `# Documentation

See the [API reference](./api.md) for details.

![diagram](./images/arch.svg)

For more info, visit [our site](https://docs.example.com).
`,
			expectedLinks:  []string{"./api.md", "https://docs.example.com"},
			expectedImages: []string{"./images/arch.svg"},
		},
		{
			name:           "empty markdown",
			markdown:       "",
			expectedLinks:  nil,
			expectedImages: nil,
		},
		{
			name:           "link with spaces in URL",
			markdown:       "[doc](./my%20document.md)",
			expectedLinks:  []string{"./my%20document.md"},
			expectedImages: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			links, images := extractLinksAndImages(tt.markdown)
			assert.Equal(t, tt.expectedLinks, links)
			assert.Equal(t, tt.expectedImages, images)
		})
	}
}

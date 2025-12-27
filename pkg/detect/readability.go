package detect

import (
	"bytes"
	"fmt"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/go-shiori/go-readability"
)

// ReadabilityExtractor extracts main content using Mozilla's Readability algorithm
type ReadabilityExtractor struct{}

// NewReadabilityExtractor creates a new readability-based content extractor
func NewReadabilityExtractor() *ReadabilityExtractor {
	return &ReadabilityExtractor{}
}

// Extract extracts the main content from an HTML document using readability
// Returns the extracted content as a goquery Selection that can be processed like regular content
func (r *ReadabilityExtractor) Extract(doc *goquery.Document, pageURL *url.URL) (*goquery.Selection, string, error) {
	// Get the HTML from the document
	html, err := doc.Html()
	if err != nil {
		return nil, "", fmt.Errorf("failed to get HTML from document: %w", err)
	}

	// Parse with readability
	article, err := readability.FromReader(strings.NewReader(html), pageURL)
	if err != nil {
		return nil, "", fmt.Errorf("readability extraction failed: %w", err)
	}

	// Check if we got meaningful content
	if article.Content == "" {
		return nil, "", fmt.Errorf("readability extracted empty content")
	}

	// Parse the extracted content back into a goquery document
	// so it can be processed by the existing pipeline (image handling, link rewriting)
	contentDoc, err := goquery.NewDocumentFromReader(bytes.NewReader([]byte(article.Content)))
	if err != nil {
		return nil, "", fmt.Errorf("failed to parse readability content: %w", err)
	}

	// Return the body content as a selection
	// The readability output is typically wrapped in a div or article
	content := contentDoc.Find("body").Children()
	if content.Length() == 0 {
		content = contentDoc.Find("body")
	}

	title := article.Title
	if title == "" {
		title = doc.Find("title").First().Text()
	}

	return content, title, nil
}

// ExtractText extracts just the text content (no HTML) using readability
// Useful for search indexing or simple text output
func (r *ReadabilityExtractor) ExtractText(doc *goquery.Document, pageURL *url.URL) (string, string, error) {
	html, err := doc.Html()
	if err != nil {
		return "", "", fmt.Errorf("failed to get HTML from document: %w", err)
	}

	article, err := readability.FromReader(strings.NewReader(html), pageURL)
	if err != nil {
		return "", "", fmt.Errorf("readability extraction failed: %w", err)
	}

	return article.TextContent, article.Title, nil
}

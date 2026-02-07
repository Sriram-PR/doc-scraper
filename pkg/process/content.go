package process

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	md "github.com/JohannesKaufmann/html-to-markdown"
	"github.com/PuerkitoBio/goquery"
	"github.com/sirupsen/logrus"

	"github.com/piratf/doc-scraper/pkg/config"
	"github.com/piratf/doc-scraper/pkg/detect"
	"github.com/piratf/doc-scraper/pkg/utils"
)

// ContentProcessor handles extracting, cleaning, processing (images, links), converting to Markdown, and saving of page content
type ContentProcessor struct {
	imgProcessor        *ImageProcessor
	log                 *logrus.Logger
	appCfg              config.AppConfig
	detector            *detect.ContentDetector
	readabilityExtractor *detect.ReadabilityExtractor
}

// NewContentProcessor creates a ContentProcessor
func NewContentProcessor(imgProcessor *ImageProcessor, appCfg config.AppConfig, log *logrus.Logger) *ContentProcessor {
	return &ContentProcessor{
		imgProcessor:        imgProcessor,
		appCfg:              appCfg,
		log:                 log,
		detector:            detect.NewContentDetector(log),
		readabilityExtractor: detect.NewReadabilityExtractor(),
	}
}

// ExtractProcessAndSaveContent extracts content using siteCfg.ContentSelector, processes images and internal links within that content, converts it to Markdown, and saves it to a path derived from finalURL and siteOutputDir
// Returns the extracted page title and any critical error encountered during processing or saving
func (cp *ContentProcessor) ExtractProcessAndSaveContent(
	doc *goquery.Document, // Parsed document of the fetched page
	finalURL *url.URL, // Final URL after redirects
	siteCfg config.SiteConfig, // Site-specific configuration
	siteOutputDir string, // Base output directory for this site
	taskLog *logrus.Entry, // Logger with task-specific context
	ctx context.Context, // Context for cancellation propagation
) (pageTitle string, savedFilePath string, err error) {
	taskLog.Debug("Extracting, processing, and saving content...")

	pageTitle = strings.TrimSpace(doc.Find("title").First().Text())
	if pageTitle == "" {
		pageTitle = "Untitled Page"
	}
	taskLog = taskLog.WithField("page_title", pageTitle)

	var mainContent *goquery.Selection
	actualSelector := siteCfg.ContentSelector

	// Handle auto content selector detection
	if detect.IsAutoSelector(siteCfg.ContentSelector) {
		result := cp.detector.Detect(doc, finalURL)

		if result.Fallback {
			// Use readability extraction
			taskLog.Debug("Using readability extraction for content")
			extractedContent, extractedTitle, extractErr := cp.readabilityExtractor.Extract(doc, finalURL)
			if extractErr != nil {
				err = fmt.Errorf("%w: readability extraction failed for '%s': %v", utils.ErrContentSelector, finalURL.String(), extractErr)
				taskLog.Warn(err.Error())
				return pageTitle, "", err
			}
			mainContent = extractedContent
			if extractedTitle != "" {
				pageTitle = extractedTitle
				taskLog = taskLog.WithField("page_title", pageTitle)
			}
			taskLog.Debugf("Extracted content using readability (framework: %s)", result.Framework)
		} else {
			// Use detected framework selector
			actualSelector = result.Selector
			taskLog.Debugf("Auto-detected selector for %s: %s", result.Framework, actualSelector)

			mainContentSelection := doc.Find(actualSelector)
			if mainContentSelection.Length() == 0 {
				// Fallback to readability if detected selector fails
				taskLog.Warnf("Detected selector '%s' not found, falling back to readability", actualSelector)
				extractedContent, extractedTitle, extractErr := cp.readabilityExtractor.Extract(doc, finalURL)
				if extractErr != nil {
					err = fmt.Errorf("%w: selector '%s' not found and readability failed for '%s': %v", utils.ErrContentSelector, actualSelector, finalURL.String(), extractErr)
					taskLog.Warn(err.Error())
					return pageTitle, "", err
				}
				mainContent = extractedContent
				if extractedTitle != "" {
					pageTitle = extractedTitle
					taskLog = taskLog.WithField("page_title", pageTitle)
				}
			} else {
				mainContent = mainContentSelection.First().Clone()
			}
		}
	} else {
		// Use explicitly configured selector
		mainContentSelection := doc.Find(siteCfg.ContentSelector)
		if mainContentSelection.Length() == 0 {
			err = fmt.Errorf("%w: selector '%s' not found on page '%s'", utils.ErrContentSelector, siteCfg.ContentSelector, finalURL.String())
			taskLog.Warn(err.Error())
			return pageTitle, "", err
		}
		// Clone selection to modify images/links without affecting original doc needed for link extraction
		mainContent = mainContentSelection.First().Clone()
		taskLog.Debugf("Found main content using selector '%s'", siteCfg.ContentSelector)
	}

	currentPageFullOutputPath, pageInScope := cp.getOutputPathForURL(finalURL, siteCfg, siteOutputDir)
	if !pageInScope {
		err = fmt.Errorf("%w: output path calculation failed unexpectedly for in-scope URL '%s'", utils.ErrScopeViolation, finalURL.String())
		taskLog.Error(err)
		return pageTitle, "", err
	}

	currentPageOutputDir := filepath.Dir(currentPageFullOutputPath)

	// ProcessImages finds images, attempts downloads/checks cache via workers, and sets 'data-crawl-status' attribute on mainContent tags
	imageMap, _ := cp.imgProcessor.ProcessImages(mainContent, finalURL, siteCfg, siteOutputDir, taskLog, ctx)
	// Ignore non-fatal image processing errors (already logged by ImageProcessor)

	// Rewrite or remove img tags based on the status set by ProcessImages
	imgRewriteCount, imgRemoveCount, imgSkippedCount := 0, 0, 0
	mainContent.Find("img").Each(func(i int, element *goquery.Selection) {
		status, _ := element.Attr("data-crawl-status")
		originalSrc, srcExists := element.Attr("src")
		element.RemoveAttr("data-crawl-status") // Cleanup attribute

		switch status {
		case "success", "pending-download": // Check map for actual result
			if !srcExists { // Should not happen if status was set
				element.Remove()
				imgRemoveCount++
				taskLog.Warnf("Image status '%s' but missing src. Removing.", status)
				return
			}
			absImgURL, resolveErr := finalURL.Parse(originalSrc)
			if resolveErr != nil { // Should not happen if parsed before
				element.Remove()
				imgRemoveCount++
				taskLog.Warnf("Could not re-parse original src '%s'. Removing tag. Error: %v", originalSrc, resolveErr)
				return
			}

			if imgData, ok := imageMap[absImgURL.String()]; ok && imgData.LocalPath != "" {

				// 1. Construct the absolute path to the saved image file
				absoluteImagePath := filepath.Join(siteOutputDir, imgData.LocalPath)
				// 2. Calculate the path relative from the current MD file's directory to the image file
				relativeImagePath, relErr := filepath.Rel(currentPageOutputDir, absoluteImagePath)
				if relErr != nil {
					taskLog.Warnf("Could not calculate relative image path from '%s' to '%s' for src '%s': %v. Removing image tag.", currentPageOutputDir, absoluteImagePath, originalSrc, relErr)
					element.Remove()
					imgRemoveCount++
					return
				}

				// 3. Use the calculated relative path (ensure forward slashes for web/markdown)
				finalImageSrc := filepath.ToSlash(relativeImagePath)
				// Rewrite src to local path and update alt from caption
				element.SetAttr("src", finalImageSrc)
				if imgData.Caption != "" {
					element.SetAttr("alt", imgData.Caption)
				} else {
					element.RemoveAttr("alt")
				}
				imgRewriteCount++
			} else {
				// Download/lookup failed
				element.Remove()
				imgRemoveCount++
				taskLog.Debugf("Removing image tag for failed download/lookup: src='%s' (Status: %s)", originalSrc, status)
			}
		case "error-parse", "error-normalize", "error-db", "error-filesystem":
			// Fatal error during initial checks
			element.Remove()
			imgRemoveCount++
			taskLog.Debugf("Removing image tag due to fatal error: src='%s' (Status: %s)", originalSrc, status)
		case "skipped-config", "skipped-empty-src", "skipped-data-uri", "skipped-scheme", "skipped-domain", "skipped-robots":
			// Non-fatal skip, leave original tag
			imgSkippedCount++
			taskLog.Debugf("Leaving skipped image tag: src='%s' (Status: %s)", originalSrc, status)
		default:
			// Unknown or unexpected status, leave tag
			imgSkippedCount++
			taskLog.Warnf("Image tag with unexpected status '%s': src='%s'. Leaving tag.", status, originalSrc)
		}
	})
	taskLog.Debugf("Image handling complete: Rewrote %d, Removed %d, Left Skipped %d.", imgRewriteCount, imgRemoveCount, imgSkippedCount)

	// Rewrite internal links to relative markdown paths
	_, linkRewriteErr := cp.rewriteInternalLinks(mainContent, finalURL, currentPageFullOutputPath, siteCfg, siteOutputDir, taskLog)
	if linkRewriteErr != nil {
		// Log non-fatal link rewriting errors
		taskLog.Warnf("Non-fatal error during internal link rewriting: %v", linkRewriteErr)
	}

	// Clean up HTML before conversion (remove Sphinx headerlinks, etc.)
	cp.cleanupHTML(mainContent)

	// Convert the processed content to Markdown
	modifiedHTML, outerHtmlErr := goquery.OuterHtml(mainContent)
	if outerHtmlErr != nil {
		err = fmt.Errorf("failed getting modified HTML: %w", outerHtmlErr)
		taskLog.Error(err)
		return pageTitle, "", err
	}

	converter := md.NewConverter("", true, nil)
	markdownContent, convertErr := converter.ConvertString(modifiedHTML)
	if convertErr != nil {
		err = fmt.Errorf("%w: %w", utils.ErrMarkdownConversion, convertErr)
		taskLog.Error(err)
		return pageTitle, "", err
	}

	// Save the Markdown file
	outputDirForFile := filepath.Dir(currentPageFullOutputPath)
	if mkdirErr := os.MkdirAll(outputDirForFile, 0755); mkdirErr != nil {
		err = fmt.Errorf("%w: creating output directory '%s': %w", utils.ErrFilesystem, outputDirForFile, mkdirErr)
		taskLog.Error(err)
		return pageTitle, "", err
	}

	writeErr := os.WriteFile(currentPageFullOutputPath, []byte(markdownContent), 0644)
	if writeErr != nil {
		err = fmt.Errorf("%w: saving markdown '%s': %w", utils.ErrFilesystem, currentPageFullOutputPath, writeErr)
		taskLog.Error(err)
		return pageTitle, "", err
	}

	taskLog.Infof("Saved Markdown (%d bytes): %s", len(markdownContent), currentPageFullOutputPath)
	taskLog.Debug("Content extraction, processing, and saving complete.")
	return pageTitle, currentPageFullOutputPath, nil // Success
}

// getOutputPathForURL calculates the local filesystem path for a crawled URL, performing scope checks and mapping URLs to sanitized file/directory structures
// Returns the absolute output path and true if the URL is in scope, otherwise empty path and false
func (cp *ContentProcessor) getOutputPathForURL(targetURL *url.URL, siteCfg config.SiteConfig, siteOutputDir string) (string, bool) {
	// Scope checks: scheme, domain, path prefix
	if (targetURL.Scheme != "http" && targetURL.Scheme != "https") ||
		targetURL.Hostname() != siteCfg.AllowedDomain {
		return "", false
	}
	targetPath := targetURL.Path
	if targetPath == "" {
		targetPath = "/" // Treat root URL as "/"
	}
	if !strings.HasPrefix(targetPath, siteCfg.AllowedPathPrefix) {
		return "", false
	}

	outputFilename := "index.md" // Default for directory-like URLs
	outputSubDir := siteOutputDir

	normalizedPath := strings.TrimSuffix(targetPath, "/")
	if normalizedPath == "" {
		normalizedPath = "/"
	}
	relativePath := strings.TrimPrefix(normalizedPath, siteCfg.AllowedPathPrefix)
	relativePath = strings.TrimPrefix(relativePath, "/")

	if relativePath != "" {
		baseName := path.Base(relativePath)
		dirPart := path.Dir(relativePath)
		ext := path.Ext(baseName)

		// Determine if path looks like a file (has extension) or directory
		if ext != "" && len(ext) > 1 { // File-like URL
			outputFilename = utils.SanitizeFilename(strings.TrimSuffix(baseName, ext)) + ".md"
			if dirPart != "" && dirPart != "." {
				var sanitizedDirParts []string
				for _, part := range strings.Split(dirPart, "/") {
					if part != "" {
						sanitizedDirParts = append(sanitizedDirParts, utils.SanitizeFilename(part))
					}
				}
				if len(sanitizedDirParts) > 0 {
					outputSubDir = filepath.Join(siteOutputDir, filepath.Join(sanitizedDirParts...))
				}
			}
		} else { // Directory-like URL
			// Use index.md; create subdirs based on full relative path
			var sanitizedDirParts []string
			for _, part := range strings.Split(relativePath, "/") {
				if part != "" {
					sanitizedDirParts = append(sanitizedDirParts, utils.SanitizeFilename(part))
				}
			}
			if len(sanitizedDirParts) > 0 {
				outputSubDir = filepath.Join(siteOutputDir, filepath.Join(sanitizedDirParts...))
			}
		}
	}

	fullPath := filepath.Join(outputSubDir, outputFilename)
	return fullPath, true
}

// cleanupHTML removes unwanted elements from HTML before markdown conversion
// This handles framework-specific noise like Sphinx headerlinks (¶ symbols)
func (cp *ContentProcessor) cleanupHTML(content *goquery.Selection) {
	// Remove Sphinx/Pallets headerlink anchors (¶ symbols after headings)
	content.Find("a.headerlink").Remove()

	// Remove ReadTheDocs "Edit on GitHub" links
	content.Find("a.edit-on-github").Remove()

	// Remove common "permalink" patterns
	content.Find("a.permalink").Remove()
	content.Find("a[title='Permalink to this heading']").Remove()
	content.Find("a[title='Link to this heading']").Remove()

	// Remove empty anchor tags that might remain
	content.Find("a").Each(func(i int, s *goquery.Selection) {
		text := strings.TrimSpace(s.Text())
		href, _ := s.Attr("href")
		// Remove anchors that only contain ¶ or are empty with fragment-only hrefs
		if text == "¶" || text == "#" || (text == "" && strings.HasPrefix(href, "#")) {
			s.Remove()
		}
	})
}

// rewriteInternalLinks modifies href attributes of anchor tags within mainContent
// It converts links pointing within the crawl scope to relative filesystem paths
// Returns the number of links rewritten and the first non-fatal error encountered
func (cp *ContentProcessor) rewriteInternalLinks(
	mainContent *goquery.Selection, // The content selection to modify
	finalURL *url.URL, // Base URL for resolving relative hrefs
	currentPageFullOutputPath string, // Filesystem path of the current MD file
	siteCfg config.SiteConfig, // For scope checking linked URLs
	siteOutputDir string, // For calculating target MD paths
	taskLog *logrus.Entry,
) (rewriteCount int, err error) {
	taskLog.Debug("Rewriting internal links...")
	rewriteCount = 0
	var firstError error = nil

	currentPageOutputDir := filepath.Dir(currentPageFullOutputPath)

	mainContent.Find("a[href]").Each(func(index int, element *goquery.Selection) {
		href, exists := element.Attr("href")
		if !exists || href == "" {
			return
		}

		// Skip fragments, external links (mailto:, tel:, http:), protocol-relative (//), javascript:
		if strings.HasPrefix(href, "#") || strings.Contains(href, ":") || strings.HasPrefix(href, "//") {
			return
		}

		linkURL, parseErr := finalURL.Parse(href)
		if parseErr != nil {
			taskLog.Warnf("Skipping rewrite for unparseable link href '%s': %v", href, parseErr)
			if firstError == nil {
				firstError = parseErr
			}
			return
		}

		// Check if the linked URL is within crawl scope and get its potential output path
		targetOutputPath, isInScope := cp.getOutputPathForURL(linkURL, siteCfg, siteOutputDir)
		if !isInScope {
			return // Leave external or out-of-scope links unmodified
		}

		// Calculate the relative path from the current file's directory to the target file
		relativePath, relErr := filepath.Rel(currentPageOutputDir, targetOutputPath)
		if relErr != nil {
			taskLog.Warnf("Could not calculate relative path from '%s' to '%s' for link '%s': %v. Keeping original.", currentPageOutputDir, targetOutputPath, href, relErr)
			if firstError == nil {
				firstError = relErr
			}
			return
		}

		// Use forward slashes and preserve fragment
		relativePath = filepath.ToSlash(relativePath)
		if linkURL.Fragment != "" {
			relativePath += "#" + linkURL.Fragment
		}

		element.SetAttr("href", relativePath)
		rewriteCount++
	})

	taskLog.Debugf("Rewrote %d internal links.", rewriteCount)
	return rewriteCount, firstError
}

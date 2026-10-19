package process

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/PuerkitoBio/goquery"
	"gopkg.in/yaml.v3"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/detect"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/utils"
)

// ContentProcessor handles content extraction, image/link processing, Markdown conversion, and saving.
type ContentProcessor struct {
	imgProcessor         *ImageProcessor
	log                  *slog.Logger
	appCfg               *config.AppConfig
	detector             *detect.ContentDetector
	readabilityExtractor *detect.ReadabilityExtractor
}

func NewContentProcessor(imgProcessor *ImageProcessor, appCfg *config.AppConfig, log *slog.Logger) *ContentProcessor {
	return &ContentProcessor{
		imgProcessor:         imgProcessor,
		appCfg:               appCfg,
		log:                  log,
		detector:             detect.NewContentDetector(log),
		readabilityExtractor: detect.NewReadabilityExtractor(),
	}
}

type pageFrontmatter struct {
	Title       string `yaml:"title"`
	URL         string `yaml:"url"`
	CrawledAt   string `yaml:"crawled_at"`
	ContentHash string `yaml:"content_hash"`
	Depth       int    `yaml:"depth"`
}

// buildPageFrontmatter returns a YAML frontmatter block (--- delimited) carrying page
// metadata for downstream RAG/LLM consumers. The content hash matches the JSONL
// content_hash since both hash the same markdown body. Returns "" if marshaling fails.
func buildPageFrontmatter(title, pageURL, body string, depth int) string {
	fm := pageFrontmatter{
		Title:     title,
		URL:       pageURL,
		CrawledAt: time.Now().UTC().Format(time.RFC3339),
		Depth:     depth,
	}
	if len(body) > 0 {
		fm.ContentHash = utils.CalculateStringSHA256(body)
	}

	out, err := yaml.Marshal(fm)
	if err != nil {
		return ""
	}
	return "---\n" + string(out) + "---\n\n"
}

// SelectMainContent resolves the page title and extracts the main content selection
// using the configured selector, framework auto-detection, or the readability
// fallback. It performs no image processing, conversion, or writing, so callers can
// hash the returned selection before deciding whether to process the page.
func (cp *ContentProcessor) SelectMainContent(
	doc *goquery.Document,
	finalURL *url.URL,
	siteCfg *config.SiteConfig,
	taskLog *slog.Logger,
) (mainContent *goquery.Selection, pageTitle string, err error) {
	pageTitle = strings.TrimSpace(doc.Find("title").First().Text())
	if pageTitle == "" {
		pageTitle = "Untitled Page"
	}
	taskLog = taskLog.With("page_title", pageTitle)

	var actualSelector string

	if detect.IsAutoSelector(siteCfg.ContentSelector) {
		result := cp.detector.Detect(doc, finalURL)

		if result.Fallback {
			taskLog.Debug("Using readability extraction for content")
			extractedContent, extractedTitle, extractErr := cp.readabilityExtractor.Extract(doc, finalURL)
			if extractErr != nil {
				err = fmt.Errorf("%w: readability failed for '%s': %v", utils.ErrContentSelector, finalURL.String(), extractErr) //nolint:errorlint // extractErr is supplemental
				taskLog.Warn(err.Error())
				return nil, pageTitle, err
			}
			mainContent = extractedContent
			if extractedTitle != "" {
				pageTitle = extractedTitle
				taskLog = taskLog.With("page_title", pageTitle)
			}
			taskLog.Debug(fmt.Sprintf("Extracted content using readability (framework: %s)", result.Framework))
		} else {
			actualSelector = result.Selector
			taskLog.Debug(fmt.Sprintf("Auto-detected selector for %s: %s", result.Framework, actualSelector))

			mainContentSelection := detect.SelectByPriority(doc, actualSelector)
			if !hasExtractableContent(mainContentSelection) {
				taskLog.Warn(fmt.Sprintf(
					"Detected selector '%s' yielded no content (%d element(s) matched), falling back to readability",
					actualSelector, mainContentSelection.Length()))
				extractedContent, extractedTitle, extractErr := cp.readabilityExtractor.Extract(doc, finalURL)
				if extractErr != nil {
					err = fmt.Errorf(
						"%w: selector '%s' yielded no content and readability failed for '%s': %v",
						utils.ErrContentSelector, actualSelector, finalURL.String(), extractErr, //nolint:errorlint // extractErr is supplemental
					)
					taskLog.Warn(err.Error())
					return nil, pageTitle, err
				}
				mainContent = extractedContent
				if extractedTitle != "" {
					pageTitle = extractedTitle
				}
			} else {
				mainContent = mainContentSelection.Clone()
				if result.Exclude != "" {
					mainContent.Find(result.Exclude).Remove()
				}
			}
		}
	} else {
		mainContentSelection := detect.SelectByPriority(doc, siteCfg.ContentSelector)
		if !hasExtractableContent(mainContentSelection) {
			err = fmt.Errorf("%w: selector '%s' yielded no content on page '%s' (%d element(s) matched)",
				utils.ErrContentSelector, siteCfg.ContentSelector, finalURL.String(), mainContentSelection.Length())
			taskLog.Warn(err.Error())
			return nil, pageTitle, err
		}
		mainContent = mainContentSelection.Clone()
		taskLog.Debug(fmt.Sprintf("Found main content using selector '%s'", siteCfg.ContentSelector))
	}

	return mainContent, pageTitle, nil
}

// ProcessAndSaveContent processes images and internal links on the already-selected
// main content, converts it to Markdown, prepends YAML frontmatter, and writes the
// file to a path derived from finalURL and siteOutputDir.
func (cp *ContentProcessor) ProcessAndSaveContent(
	mainContent *goquery.Selection,
	pageTitle string,
	finalURL *url.URL,
	siteCfg *config.SiteConfig,
	siteOutputDir string,
	currentDepth int,
	taskLog *slog.Logger,
	ctx context.Context,
) (savedFilePath string, markdownBytes []byte, imageCount int, err error) {
	taskLog = taskLog.With("page_title", pageTitle)

	currentPageFullOutputPath, pageInScope := cp.OutputPathForURL(finalURL, siteCfg, siteOutputDir)
	if !pageInScope {
		err = fmt.Errorf("%w: output path calculation failed unexpectedly for in-scope URL '%s'", utils.ErrScopeViolation, finalURL.String())
		taskLog.Error(err.Error())
		return "", nil, 0, err
	}

	currentPageOutputDir := filepath.Dir(currentPageFullOutputPath)

	imageMap, _ := cp.imgProcessor.ProcessImages(mainContent, finalURL, siteCfg, siteOutputDir, taskLog, ctx)

	imgRewriteCount, imgRemoveCount, imgSkippedCount := 0, 0, 0
	mainContent.Find("img").Each(func(i int, element *goquery.Selection) {
		status, _ := element.Attr("data-crawl-status")
		originalSrc, srcExists := element.Attr("src")
		element.RemoveAttr("data-crawl-status")

		switch status {
		case "success", "pending-download": // Check map for actual result
			if !srcExists { // Should not happen if status was set
				element.Remove()
				imgRemoveCount++
				taskLog.Warn(fmt.Sprintf("Image status '%s' but missing src. Removing.", status))
				return
			}
			absImgURL, resolveErr := finalURL.Parse(originalSrc)
			if resolveErr != nil { // Should not happen if parsed before
				element.Remove()
				imgRemoveCount++
				taskLog.Warn(fmt.Sprintf("Could not re-parse original src '%s'. Removing tag. Error: %v", originalSrc, resolveErr))
				return
			}

			if imgData, ok := imageMap[absImgURL.String()]; ok && imgData.LocalPath != "" {
				absoluteImagePath := filepath.Join(siteOutputDir, imgData.LocalPath)
				relativeImagePath, relErr := filepath.Rel(currentPageOutputDir, absoluteImagePath)
				if relErr != nil {
					taskLog.Warn(fmt.Sprintf("Could not calculate relative image path from '%s' to '%s' for src '%s': %v. Removing image tag.", currentPageOutputDir, absoluteImagePath, originalSrc, relErr))
					element.Remove()
					imgRemoveCount++
					return
				}

				finalImageSrc := filepath.ToSlash(relativeImagePath)
				element.SetAttr("src", finalImageSrc)
				if imgData.Caption != "" {
					element.SetAttr("alt", imgData.Caption)
				} else {
					element.RemoveAttr("alt")
				}
				imgRewriteCount++
			} else {
				element.Remove()
				imgRemoveCount++
				taskLog.Debug(fmt.Sprintf("Removing image tag for failed download/lookup: src='%s' (Status: %s)", originalSrc, status))
			}
		case "error-parse", "error-normalize", "error-db", "error-filesystem":
			element.Remove()
			imgRemoveCount++
			taskLog.Debug(fmt.Sprintf("Removing image tag due to fatal error: src='%s' (Status: %s)", originalSrc, status))
		case "skipped-config", "skipped-empty-src", "skipped-data-uri", "skipped-scheme", "skipped-domain", "skipped-robots":
			imgSkippedCount++
			taskLog.Debug(fmt.Sprintf("Leaving skipped image tag: src='%s' (Status: %s)", originalSrc, status))
		default:
			imgSkippedCount++
			taskLog.Warn(fmt.Sprintf("Image tag with unexpected status '%s': src='%s'. Leaving tag.", status, originalSrc))
		}
	})
	taskLog.Debug(fmt.Sprintf("Image handling complete: Rewrote %d, Removed %d, Left Skipped %d.", imgRewriteCount, imgRemoveCount, imgSkippedCount))

	_, linkRewriteErr := cp.rewriteInternalLinks(mainContent, finalURL, currentPageFullOutputPath, siteCfg, siteOutputDir, taskLog)
	if linkRewriteErr != nil {
		taskLog.Warn(fmt.Sprintf("Non-fatal error during internal link rewriting: %v", linkRewriteErr))
	}

	cp.cleanupHTML(mainContent)

	markdownContent, convErr := ToMarkdown(mainContent)
	if convErr != nil {
		err = fmt.Errorf("%w: %w", utils.ErrParsing, convErr)
		taskLog.Error(err.Error())
		return "", nil, 0, err
	}

	outputDirForFile := filepath.Dir(currentPageFullOutputPath)
	if mkdirErr := os.MkdirAll(outputDirForFile, 0755); mkdirErr != nil {
		err = fmt.Errorf("%w: creating output directory '%s': %w", utils.ErrFilesystem, outputDirForFile, mkdirErr)
		taskLog.Error(err.Error())
		return "", nil, 0, err
	}

	fileContent := buildPageFrontmatter(pageTitle, finalURL.String(), markdownContent, currentDepth) + markdownContent
	writeErr := os.WriteFile(currentPageFullOutputPath, []byte(fileContent), 0644)
	if writeErr != nil {
		err = fmt.Errorf("%w: saving markdown '%s': %w", utils.ErrFilesystem, currentPageFullOutputPath, writeErr)
		taskLog.Error(err.Error())
		return "", nil, 0, err
	}

	taskLog.Info(fmt.Sprintf("Saved Markdown (%d bytes): %s", len(fileContent), currentPageFullOutputPath))
	taskLog.Debug("Content extraction, processing, and saving complete.")
	return currentPageFullOutputPath, []byte(markdownContent), imgRewriteCount, nil
}

// OutputPathForURL maps a crawled URL to a sanitized local filesystem path, performing scope
// checks. Returns the absolute output path and true if in scope, otherwise ("", false).
func (cp *ContentProcessor) OutputPathForURL(targetURL *url.URL, siteCfg *config.SiteConfig, siteOutputDir string) (string, bool) {
	if (targetURL.Scheme != "http" && targetURL.Scheme != "https") ||
		targetURL.Hostname() != siteCfg.AllowedDomain {
		return "", false
	}
	targetPath := targetURL.Path
	if targetPath == "" {
		targetPath = "/"
	}
	if !strings.HasPrefix(targetPath, siteCfg.AllowedPathPrefix) {
		return "", false
	}

	outputFilename := "index.md"
	outputSubDir := siteOutputDir

	normalizedPath := strings.TrimSuffix(targetPath, "/")
	if normalizedPath == "" {
		normalizedPath = "/"
	}
	// normalizedPath has lost its trailing slash, so the prefix must too, or
	// the prefix root itself ("/docs/" -> "/docs") would keep its full path.
	relativePath := strings.TrimPrefix(normalizedPath, strings.TrimSuffix(siteCfg.AllowedPathPrefix, "/"))
	relativePath = strings.TrimPrefix(relativePath, "/")

	if relativePath != "" {
		baseName := path.Base(relativePath)
		dirPart := path.Dir(relativePath)
		ext := path.Ext(baseName)

		if ext != "" && len(ext) > 1 { // file-like URL
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
		} else {
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

	// Defense in depth: even if upstream sanitization is bypassed, ensure the
	// resolved path stays under siteOutputDir. This blocks any URL whose path
	// segments resolve (via filepath.Join) outside the site's output tree.
	if !utils.IsWithinDir(siteOutputDir, fullPath) {
		return "", false
	}
	return fullPath, true
}

// visibleText trims whitespace and zero-width characters, so anchors that
// render as nothing (Docusaurus hash-links use U+200B) compare equal to "".
func visibleText(s *goquery.Selection) string {
	return strings.TrimFunc(s.Text(), func(r rune) bool {
		switch r {
		case '\u200b', '\u200c', '\u200d', '\ufeff':
			return true
		}
		return unicode.IsSpace(r)
	})
}

// hasExtractableContent reports whether a selection is worth saving. Matching
// an element is not enough: a site can render some pages through a different
// template that leaves the content container present but empty (MkDocs
// Material's own landing page is literally "<article class=md-content__inner> </article>"),
// which would otherwise be written out as a blank page. Embedded media counts,
// so a page whose content region is only a diagram or table is not discarded.
func hasExtractableContent(s *goquery.Selection) bool {
	if visibleText(s) != "" {
		return true
	}
	return s.Find("img, picture, video, audio, svg, iframe, table, pre").Length() > 0
}

// cleanupHTML removes framework-specific noise before markdown conversion
// (Sphinx headerlinks, RTD edit links, generic permalink anchors).
func (cp *ContentProcessor) cleanupHTML(content *goquery.Selection) {
	content.Find("a.headerlink").Remove()
	content.Find("a.edit-on-github").Remove()
	content.Find("a.permalink").Remove()
	content.Find("a[title='Permalink to this heading']").Remove()
	content.Find("a[title='Link to this heading']").Remove()

	content.Find("a").Each(func(i int, s *goquery.Selection) {
		text := visibleText(s)
		href, _ := s.Attr("href")
		if text == "¶" || text == "#" || (text == "" && strings.HasPrefix(href, "#")) {
			s.Remove()
		}
	})

	// mdBook, Docker, and Sphinx toc-backrefs wrap the whole heading in a
	// same-page anchor, which would otherwise render as "# [Title](\#title)".
	// Matched on text rather than href because Sphinx points these at a
	// generated id ("#id2") that never matches the heading's own id. Anchors
	// pointing off-page are left alone, so headings that are genuine links
	// (changelog entries linking to a diff) survive.
	content.Find("h1, h2, h3, h4, h5, h6").Each(func(i int, heading *goquery.Selection) {
		headingText := visibleText(heading)
		if headingText == "" {
			return
		}
		heading.Find("a").Each(func(j int, a *goquery.Selection) {
			if href, ok := a.Attr("href"); !ok || !strings.HasPrefix(href, "#") {
				return
			}
			if visibleText(a) == headingText {
				a.Contents().Unwrap()
			}
		})
	})
}

// rewriteInternalLinks converts in-scope hrefs to relative filesystem paths and makes the
// remaining relative hrefs absolute.
// Returns the number of links rewritten and the first non-fatal error.
func (cp *ContentProcessor) rewriteInternalLinks(
	mainContent *goquery.Selection,
	finalURL *url.URL,
	currentPageFullOutputPath string,
	siteCfg *config.SiteConfig,
	siteOutputDir string,
	taskLog *slog.Logger,
) (rewriteCount int, err error) {
	taskLog.Debug("Rewriting internal links...")
	var firstError error

	currentPageOutputDir := filepath.Dir(currentPageFullOutputPath)

	mainContent.Find("a[href]").Each(func(index int, element *goquery.Selection) {
		href, exists := element.Attr("href")
		if !exists || href == "" {
			return
		}

		if strings.HasPrefix(href, "#") {
			return
		}
		// Skip scheme-based URIs; a scheme only appears before the first slash, so
		// colons in paths like /foo:bar are safe.
		if colonIdx := strings.Index(href, ":"); colonIdx >= 0 {
			slashIdx := strings.Index(href, "/")
			if slashIdx < 0 || colonIdx < slashIdx {
				return
			}
		}

		linkURL, parseErr := finalURL.Parse(href)
		if parseErr != nil {
			taskLog.Warn(fmt.Sprintf("Skipping rewrite for unparseable link href '%s': %v", href, parseErr))
			if firstError == nil {
				firstError = parseErr
			}
			return
		}

		targetOutputPath, isInScope := cp.OutputPathForURL(linkURL, siteCfg, siteOutputDir)
		if !isInScope {
			// A relative href to a page outside the crawl resolves nowhere in the saved output.
			element.SetAttr("href", linkURL.String())
			return
		}

		relativePath, relErr := filepath.Rel(currentPageOutputDir, targetOutputPath)
		if relErr != nil {
			taskLog.Warn(fmt.Sprintf("Could not calculate relative path from '%s' to '%s' for link '%s': %v. Keeping original.", currentPageOutputDir, targetOutputPath, href, relErr))
			if firstError == nil {
				firstError = relErr
			}
			return
		}

		relativePath = filepath.ToSlash(relativePath)
		if linkURL.Fragment != "" {
			relativePath += "#" + linkURL.Fragment
		}

		element.SetAttr("href", relativePath)
		rewriteCount++
	})

	taskLog.Debug(fmt.Sprintf("Rewrote %d internal links.", rewriteCount))
	return rewriteCount, firstError
}

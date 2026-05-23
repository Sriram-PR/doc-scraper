package process

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/sirupsen/logrus"

	"github.com/Sriram-PR/doc-scraper/pkg/config"
	"github.com/Sriram-PR/doc-scraper/pkg/fetch"
	"github.com/Sriram-PR/doc-scraper/pkg/models"
	"github.com/Sriram-PR/doc-scraper/pkg/parse"
	"github.com/Sriram-PR/doc-scraper/pkg/storage"
	"github.com/Sriram-PR/doc-scraper/pkg/utils"

	"golang.org/x/sync/semaphore"
)

const (
	ImageDir = "images"
)

// ImageDownloadTask holds information needed for an image worker to process one image.
type ImageDownloadTask struct {
	AbsImgURL        string
	NormImgURL       string
	BaseImgURL       *url.URL // Parsed absolute URL
	ImgHost          string
	ExtractedCaption string
	ImgLogEntry      *logrus.Entry   // Logger with image-specific context
	Ctx              context.Context // Context for this specific task
}

// ImageProcessor handles the orchestration of image downloading and processing.
type ImageProcessor struct {
	store           storage.ImageStore
	fetcher         fetch.HTTPFetcher
	robotsHandler   *fetch.RobotsHandler
	rateLimiter     *fetch.RateLimiter
	globalSemaphore *semaphore.Weighted
	hostSemPool     *fetch.HostSemaphorePool
	resolved        *config.ResolvedSiteConfig
	appCfg          *config.AppConfig
	log             *logrus.Entry
}

// NewImageProcessor creates a new ImageProcessor.
func NewImageProcessor(
	store storage.ImageStore,
	fetcher fetch.HTTPFetcher,
	robotsHandler *fetch.RobotsHandler,
	rateLimiter *fetch.RateLimiter,
	globalSemaphore *semaphore.Weighted,
	hostSemPool *fetch.HostSemaphorePool,
	resolved *config.ResolvedSiteConfig,
	appCfg *config.AppConfig,
	log *logrus.Entry,
) *ImageProcessor {
	return &ImageProcessor{
		store:           store,
		fetcher:         fetcher,
		robotsHandler:   robotsHandler,
		rateLimiter:     rateLimiter,
		globalSemaphore: globalSemaphore,
		hostSemPool:     hostSemPool,
		resolved:        resolved,
		appCfg:          appCfg,
		log:             log,
	}
}

// ProcessImages finds images in mainContent, checks DB status, dispatches downloads to a worker
// pool, and returns a map of successfully processed images. Modifies the 'data-crawl-status'
// attribute on img tags in the selection.
func (ip *ImageProcessor) ProcessImages( //nolint:gocyclo // image processing pipeline with many edge cases
	mainContent *goquery.Selection, // Operate on the selection
	finalURL *url.URL, // Base URL of the page containing the images
	siteCfg *config.SiteConfig, // Need site-specific image settings
	siteOutputDir string, // Need for calculating local paths
	taskLog *logrus.Entry, // Logger for the parent page task
	ctx context.Context, // Parent context
) (imageMap map[string]models.ImageData, imageErrs []error) {
	taskLog.Debug("Processing images...")
	imageMap = make(map[string]models.ImageData)
	imageErrs = make([]error, 0)

	skipImages := ip.resolved.SkipImages
	allowedDomains := siteCfg.AllowedImageDomains
	disallowedDomains := siteCfg.DisallowedImageDomains

	if skipImages {
		taskLog.Info("Skipping all image processing based on configuration.")
		mainContent.Find("img").SetAttr("data-crawl-status", "skipped-config")
		return imageMap, imageErrs
	}

	var imgWg sync.WaitGroup
	var imgErrMu sync.Mutex

	numImageWorkers := ip.appCfg.NumImageWorkers
	if numImageWorkers <= 0 {
		numImageWorkers = ip.appCfg.NumWorkers
	}
	imageTaskChan := make(chan ImageDownloadTask, numImageWorkers*2)

	taskLog.Infof("Launching %d image download workers", numImageWorkers)
	for i := 1; i <= numImageWorkers; i++ {
		go ip.imageWorker(i, imageTaskChan, siteCfg, siteOutputDir, imageMap, &imageErrs, &imgErrMu, &imgWg)
	}

	// Ensure base image directory exists
	localImageDir := filepath.Join(siteOutputDir, ImageDir)
	if mkDirErr := os.MkdirAll(localImageDir, 0755); mkDirErr != nil {
		wrappedErr := fmt.Errorf("%w: creating base image directory '%s': %w", utils.ErrFilesystem, localImageDir, mkDirErr)
		taskLog.Error(wrappedErr)
		imgErrMu.Lock()
		imageErrs = append(imageErrs, wrappedErr)
		imgErrMu.Unlock()
	}

	mainContent.Find("img").Each(func(index int, element *goquery.Selection) {
		element.SetAttr("data-crawl-status", "pending")
		imgSrc, exists := element.Attr("src")
		if !exists || imgSrc == "" {
			element.SetAttr("data-crawl-status", "skipped-empty-src")
			return
		}
		if strings.HasPrefix(imgSrc, "data:") {
			element.SetAttr("data-crawl-status", "skipped-data-uri")
			return
		}

		imgURL, imgParseErr := finalURL.Parse(imgSrc)
		if imgParseErr != nil {
			taskLog.Warnf("Image src parse error '%s': %v", imgSrc, imgParseErr)
			element.SetAttr("data-crawl-status", "error-parse")
			return
		}
		absoluteImgURL := imgURL.String()
		imgHost := imgURL.Hostname()
		imgLog := taskLog.WithFields(logrus.Fields{"img_url": absoluteImgURL, "img_host": imgHost})

		if imgURL.Scheme != "http" && imgURL.Scheme != "https" {
			element.SetAttr("data-crawl-status", "skipped-scheme")
			return
		}

		if !isDomainAllowed(imgHost, allowedDomains, disallowedDomains) {
			element.SetAttr("data-crawl-status", "skipped-domain")
			return
		}

		if !ip.robotsHandler.TestAgent(imgURL, ip.resolved.UserAgent, ctx) {
			element.SetAttr("data-crawl-status", "skipped-robots")
			return
		}

		imgNormURLStr, _, imgNormErr := parse.ParseAndNormalize(absoluteImgURL)
		if imgNormErr != nil {
			imgLog.Warnf("Cannot normalize image URL: %v", imgNormErr)
			element.SetAttr("data-crawl-status", "error-normalize")
			return
		}

		dbStatus, dbEntry, dbErr := ip.store.CheckImageStatus(imgNormURLStr)
		if dbErr != nil {
			wrappedErr := fmt.Errorf("image DB check failed for '%s': %w", imgNormURLStr, dbErr)
			imgLog.Error(wrappedErr)
			imgErrMu.Lock()
			imageErrs = append(imageErrs, wrappedErr)
			imgErrMu.Unlock()
			element.SetAttr("data-crawl-status", "error-db")
			return
		}

		caption := ""
		figure := element.Closest("figure")
		if figure.Length() > 0 {
			figcaption := figure.Find("figcaption").First()
			if figcaption.Length() > 0 {
				caption = strings.TrimSpace(figcaption.Text())
			}
		}
		if caption == "" {
			if alt, altExists := element.Attr("alt"); altExists {
				caption = strings.TrimSpace(alt)
			}
		}

		shouldDispatch := false
		switch dbStatus {
		case models.ImageStatusSuccess:
			if dbEntry != nil && dbEntry.LocalPath != "" {
				element.SetAttr("data-crawl-status", "success")
				imgErrMu.Lock()
				imageMap[absoluteImgURL] = models.ImageData{
					OriginalURL: absoluteImgURL,
					LocalPath:   dbEntry.LocalPath,
					Caption:     caption,
				}
				imgErrMu.Unlock()
			} else {
				imgLog.Warnf("Image DB status 'success' but invalid entry (missing path) for '%s'. Re-scheduling download.", imgNormURLStr)
				shouldDispatch = true
				element.SetAttr("data-crawl-status", "pending-download")
			}
		case models.ImageStatusFailure:
			errMsg := "Unknown reason"
			if dbEntry != nil {
				errMsg = dbEntry.ErrorType
			}
			imgLog.Warnf("Image previously failed download ('%s'). Re-scheduling.", errMsg)
			shouldDispatch = true
			element.SetAttr("data-crawl-status", "pending-download")
		default:
			imgLog.Debugf("Image '%s' new or previously failed check ('%s'). Scheduling download.", imgSrc, dbStatus)
			shouldDispatch = true
			element.SetAttr("data-crawl-status", "pending-download")
		}

		if shouldDispatch {
			task := ImageDownloadTask{
				AbsImgURL:        absoluteImgURL,
				NormImgURL:       imgNormURLStr,
				BaseImgURL:       imgURL,
				ImgHost:          imgHost,
				ExtractedCaption: caption,
				ImgLogEntry:      imgLog,
				Ctx:              ctx,
			}
			imgWg.Add(1)

			select {
			case imageTaskChan <- task:
			case <-ctx.Done():
				imgLog.Warnf("Context cancelled while trying to dispatch image task for '%s': %v", imgSrc, ctx.Err())
				imgWg.Done()
				element.SetAttr("data-crawl-status", "error-dispatch-context")
			}
		}
	})

	taskLog.Debug("Finished dispatching all image tasks for this page. Closing task channel.")
	close(imageTaskChan)

	taskLog.Debug("Waiting for image download workers to finish...")
	imgWg.Wait()
	taskLog.Debug("All image download workers finished for this page.")

	if len(imageErrs) > 0 {
		taskLog.Warnf("Finished image processing for page with %d non-fatal error(s).", len(imageErrs))
	} else {
		taskLog.Debug("Image processing complete for page.")
	}
	return imageMap, imageErrs
}

func (ip *ImageProcessor) imageWorker(
	id int,
	taskChan <-chan ImageDownloadTask,
	siteCfg *config.SiteConfig,
	siteOutputDir string,
	imageMap map[string]models.ImageData,
	imageErrs *[]error,
	imgErrMu *sync.Mutex,
	imgWg *sync.WaitGroup,
) {
	workerLog := ip.log.WithField("image_worker_id", id)
	workerLog.Debug("Image worker started")

	for task := range taskChan {
		ip.processSingleImageTask(task, siteCfg, siteOutputDir, imageMap, imageErrs, imgErrMu, imgWg)
	}

	workerLog.Debug("Image worker finished (task channel closed)")
}

// processSingleImageTask handles the download, saving, and DB update for one image.
func (ip *ImageProcessor) processSingleImageTask(
	task ImageDownloadTask,
	_ *config.SiteConfig,
	siteOutputDir string,
	imageMap map[string]models.ImageData,
	imageErrs *[]error,
	imgErrMu *sync.Mutex,
	imgWg *sync.WaitGroup,
) {
	ctx := task.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	imgLogEntry := task.ImgLogEntry
	imgHost := task.ImgHost
	var imgTaskErr error
	imgDownloaded := false
	imgLocalPath := ""
	var copiedBytes int64

	defer func() {
		panicked := false
		if r := recover(); r != nil {
			panicked = true
			imgTaskErr = fmt.Errorf("panic processing img '%s': %v", task.AbsImgURL, r)
			stackTrace := string(debug.Stack())
			imgLogEntry.WithFields(logrus.Fields{"panic_info": r, "stack_trace": stackTrace}).Error("PANIC Recovered in processSingleImageTask")
			// Collect error (needs mutex)
			imgErrMu.Lock()
			*imageErrs = append(*imageErrs, imgTaskErr) // Use the error created above
			imgErrMu.Unlock()
		}

		now := time.Now()
		var entryToSave models.ImageDBEntry
		if imgTaskErr == nil && imgDownloaded {
			entryToSave = models.ImageDBEntry{
				Status:      models.ImageStatusSuccess,
				LocalPath:   imgLocalPath,
				Caption:     task.ExtractedCaption,
				LastAttempt: now,
			}
		} else {
			errorType := "UnknownDownloadFailure"
			if imgTaskErr != nil {
				errorType = utils.CategorizeError(imgTaskErr)
			}
			entryToSave = models.ImageDBEntry{
				Status:      models.ImageStatusFailure,
				ErrorType:   errorType,
				LastAttempt: now,
			}
			if imgTaskErr != nil && !panicked {
				imgLogEntry.Warnf("Image download/save failed: %v", imgTaskErr)
				imgErrMu.Lock()
				*imageErrs = append(*imageErrs, imgTaskErr)
				imgErrMu.Unlock()
			}
		}
		if updateErr := ip.store.UpdateImageStatus(task.NormImgURL, &entryToSave); updateErr != nil {
			dbUpdateErr := fmt.Errorf("failed update DB status img '%s' to '%s': %w", task.NormImgURL, entryToSave.Status, updateErr)
			imgLogEntry.Error(dbUpdateErr)
			imgErrMu.Lock()
			*imageErrs = append(*imageErrs, dbUpdateErr)
			imgErrMu.Unlock()
		}

		imgWg.Done()
	}()

	userAgent := ip.resolved.UserAgent
	imgHostDelay := ip.resolved.DelayPerHost
	semTimeout := config.DefaultSemaphoreAcquireTimeout
	effectiveMaxBytes := ip.resolved.MaxImageSizeBytes

	// Closure acquires host + global semaphores with scoped defers, then applies rate limit.
	semAcquireErr := func() error {
		ctxIH, cancelIH := context.WithTimeout(ctx, semTimeout)
		defer cancelIH()
		semErr := ip.hostSemPool.Acquire(ctxIH, imgHost)
		if semErr != nil {
			if errors.Is(semErr, context.DeadlineExceeded) {
				return fmt.Errorf("%w: acquiring host semaphore for img '%s': %w", utils.ErrSemaphoreTimeout, task.AbsImgURL, semErr)
			}
			return fmt.Errorf("failed acquiring host semaphore for img '%s': %w", task.AbsImgURL, semErr)
		}
		defer ip.hostSemPool.Release(imgHost)

		ctxIG, cancelIG := context.WithTimeout(ctx, semTimeout)
		defer cancelIG()
		semErr = ip.globalSemaphore.Acquire(ctxIG, 1)
		if semErr != nil {
			if errors.Is(semErr, context.DeadlineExceeded) {
				return fmt.Errorf("%w: acquiring global semaphore for img '%s': %w", utils.ErrSemaphoreTimeout, task.AbsImgURL, semErr)
			}
			return fmt.Errorf("failed acquiring global semaphore for img '%s': %w", task.AbsImgURL, semErr)
		}
		defer ip.globalSemaphore.Release(1)

		ip.rateLimiter.ApplyDelay(ctx, imgHost, imgHostDelay)

		return nil
	}()

	if semAcquireErr != nil {
		imgTaskErr = semAcquireErr
		return
	}

	imgResp, fetchErr := ip.fetchImageData(task, userAgent, effectiveMaxBytes)
	if fetchErr != nil {
		imgTaskErr = fetchErr
		return
	}
	defer imgResp.Body.Close()

	relPath, copied, saveErr := ip.saveImageToDisk(task, imgResp, effectiveMaxBytes, siteOutputDir)
	if saveErr != nil {
		imgTaskErr = saveErr
		return
	}

	imgDownloaded = true
	imgLocalPath = relPath
	copiedBytes = copied

	imgErrMu.Lock()
	imageMap[task.AbsImgURL] = models.ImageData{
		OriginalURL: task.AbsImgURL,
		LocalPath:   imgLocalPath,
		Caption:     task.ExtractedCaption,
	}
	imgErrMu.Unlock()

	imgLogEntry.Debugf("Successfully saved image (%d bytes)", copiedBytes)
}

// fetchImageData fetches the image with retries, validates Content-Length, and returns
// the response. Caller must close the body on success; on error the body is already closed.
func (ip *ImageProcessor) fetchImageData(task ImageDownloadTask, userAgent string, effectiveMaxBytes int64) (*http.Response, error) {
	ctx := task.Ctx
	imgLogEntry := task.ImgLogEntry
	imgHost := task.ImgHost

	imgReq, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, task.AbsImgURL, nil)
	if reqErr != nil {
		ip.rateLimiter.UpdateLastRequestTime(imgHost)
		return nil, fmt.Errorf("%w: creating request for img '%s': %w", utils.ErrRequestCreation, task.AbsImgURL, reqErr)
	}
	imgReq.Header.Set("User-Agent", userAgent)

	imgResp, imgFetchErr := ip.fetcher.FetchWithRetry(imgReq, ctx)
	ip.rateLimiter.UpdateLastRequestTime(imgHost)

	if imgFetchErr != nil {
		if imgResp != nil {
			io.Copy(io.Discard, imgResp.Body)
			imgResp.Body.Close()
		}
		return nil, fmt.Errorf("fetch failed for img '%s': %w", task.AbsImgURL, imgFetchErr)
	}

	headerSizeStr := imgResp.Header.Get("Content-Length")
	if headerSizeStr != "" {
		if headerSize, parseHdrErr := strconv.ParseInt(headerSizeStr, 10, 64); parseHdrErr == nil {
			if effectiveMaxBytes > 0 && headerSize > effectiveMaxBytes {
				io.Copy(io.Discard, imgResp.Body)
				imgResp.Body.Close()
				return nil, fmt.Errorf("image '%s' exceeds max size based on header (%d > %d bytes)", task.AbsImgURL, headerSize, effectiveMaxBytes)
			}
		} else {
			imgLogEntry.Warnf("Could not parse Content-Length header '%s'", headerSizeStr)
		}
	}

	return imgResp, nil
}

// saveImageToDisk generates the local filename, streams the response body with an optional size
// limit, and cleans up partial files on error. Returns the relative path and bytes written.
func (ip *ImageProcessor) saveImageToDisk(task ImageDownloadTask, imgResp *http.Response, effectiveMaxBytes int64, siteOutputDir string) (string, int64, error) {
	imgLogEntry := task.ImgLogEntry
	localImageDir := filepath.Join(siteOutputDir, ImageDir)

	localFilename, fileExtErr := generateLocalFilename(task.BaseImgURL, task.AbsImgURL, imgResp.Header.Get("Content-Type"), imgLogEntry)
	if fileExtErr != nil {
		io.Copy(io.Discard, imgResp.Body)
		return "", 0, fileExtErr
	}
	localFilePath := filepath.Join(localImageDir, localFilename)

	relativeFilePath, relErr := filepath.Rel(siteOutputDir, localFilePath)
	if relErr != nil {
		imgLogEntry.Warnf("Could not calculate relative path from '%s' to '%s': %v. Using filename only.", siteOutputDir, localFilePath, relErr)
		relativeFilePath = localFilename
	}
	relativeFilePath = filepath.ToSlash(relativeFilePath)
	imgLogEntry.Debugf("Final image save path: %s (Relative: %s)", localFilePath, relativeFilePath)

	if mkDirErr := os.MkdirAll(localImageDir, 0755); mkDirErr != nil {
		io.Copy(io.Discard, imgResp.Body)
		return "", 0, fmt.Errorf("%w: ensuring image directory '%s' exists: %w", utils.ErrFilesystem, localImageDir, mkDirErr)
	}

	outFile, createErr := os.Create(localFilePath)
	if createErr != nil {
		io.Copy(io.Discard, imgResp.Body)
		return "", 0, fmt.Errorf("%w: creating image file '%s': %w", utils.ErrFilesystem, localFilePath, createErr)
	}

	var reader io.Reader = imgResp.Body
	if effectiveMaxBytes > 0 {
		reader = io.LimitReader(imgResp.Body, effectiveMaxBytes)
	}

	imgLogEntry.Debugf("Streaming image data to %s", localFilePath)
	copiedBytes, copyErr := io.Copy(outFile, reader)

	// Drain any remaining body bytes to allow connection reuse.
	_, drainErr := io.Copy(io.Discard, imgResp.Body)
	if drainErr != nil {
		imgLogEntry.Warnf("Error draining response body after copy: %v", drainErr)
	}

	if copyErr != nil {
		outFile.Close()
		os.Remove(localFilePath)
		return "", 0, fmt.Errorf("%w: copying image data to '%s' (copied %d bytes): %w", utils.ErrFilesystem, localFilePath, copiedBytes, copyErr)
	}

	// If we hit the limit exactly, check Content-Length to distinguish truncation from a file
	// that happens to be exactly at the boundary before removing it.
	if effectiveMaxBytes > 0 && copiedBytes >= effectiveMaxBytes {
		sizeExceeded := true
		headerSizeStr := imgResp.Header.Get("Content-Length")
		if headerSizeStr != "" {
			if headerSize, _ := strconv.ParseInt(headerSizeStr, 10, 64); headerSize <= effectiveMaxBytes {
				imgLogEntry.Warnf("Copied bytes (%d) >= limit (%d), but Content-Length (%d) was <= limit. Keeping file.", copiedBytes, effectiveMaxBytes, headerSize)
				sizeExceeded = false
			}
		}

		if sizeExceeded {
			outFile.Close()
			os.Remove(localFilePath)
			return "", 0, fmt.Errorf("image '%s' exceeds max size (%d >= %d bytes, download truncated)", task.AbsImgURL, copiedBytes, effectiveMaxBytes)
		}
	}

	if closeErr := outFile.Close(); closeErr != nil {
		return "", 0, fmt.Errorf("%w: closing image file '%s' after write: %w", utils.ErrFilesystem, localFilePath, closeErr)
	}

	return relativeFilePath, copiedBytes, nil
}

// generateLocalFilename creates a unique, safe filename for a downloaded image.
func generateLocalFilename(baseImgURL *url.URL, absImgURL string, contentType string, imgLogEntry *logrus.Entry) (string, error) { //nolint:gocyclo // filename generation with many content-type and extension edge cases
	originalExt := path.Ext(baseImgURL.Path)
	imgBaseName := utils.SanitizeFilename(strings.TrimSuffix(path.Base(baseImgURL.Path), originalExt))
	if imgBaseName == "" || imgBaseName == "_" {
		urlHashOnly := fmt.Sprintf("%x", sha256.Sum256([]byte(absImgURL)))[:12]
		imgBaseName = "image_" + urlHashOnly
		imgLogEntry.Debugf("Sanitized base name was empty, using hash fallback: %s", imgBaseName)
	}

	finalExt := originalExt

	if contentType != "" {
		mimeType, _, mimeErr := mime.ParseMediaType(contentType)
		if mimeErr == nil {
			switch mimeType {
			case "image/jpeg":
				finalExt = ".jpg"
			case "image/png":
				finalExt = ".png"
			case "image/gif":
				finalExt = ".gif"
			case "image/webp":
				finalExt = ".webp"
			case "image/svg+xml":
				finalExt = ".svg"
			default:
				extensions, extErr := mime.ExtensionsByType(mimeType)
				if extErr == nil && len(extensions) > 0 {
					preferredExt := ""
					for _, ext := range extensions {
						if ext == ".jpg" || ext == ".jpeg" || ext == ".png" || ext == ".gif" || ext == ".webp" || ext == ".svg" {
							preferredExt = ext
							break
						}
					}
					if preferredExt != "" {
						finalExt = preferredExt
					} else if finalExt == "" {
						finalExt = extensions[0]
					}
				} else if finalExt == "" {
					return "", fmt.Errorf("cannot determine file extension (MIME: %s, MIME extensions error: %w, URL Ext: none)", mimeType, extErr)
				}
			}
		} else {
			imgLogEntry.Warnf("Could not parse Content-Type header '%s': %v", contentType, mimeErr)
			if finalExt == "" {
				return "", fmt.Errorf("cannot determine file extension (unparsable Content-Type, no URL extension)")
			}
		}
	} else if finalExt == "" {
		return "", fmt.Errorf("cannot determine file extension (no Content-Type, no URL extension)")
	}

	if finalExt != "" && !strings.HasPrefix(finalExt, ".") {
		finalExt = "." + finalExt
	}

	// Short hash of the full URL disambiguates files with the same base name but different paths.
	urlHash := fmt.Sprintf("%x", sha256.Sum256([]byte(absImgURL)))[:8]
	localFilename := fmt.Sprintf("%s_%s%s", imgBaseName, urlHash, finalExt)

	return localFilename, nil
}

// isDomainAllowed returns true if host passes the allowed/disallowed domain filters.
// A host is rejected if it matches any disallowed pattern.  When an allowed list is
// provided, the host must also match at least one allowed pattern.
func isDomainAllowed(host string, allowed, disallowed []string) bool {
	for _, pattern := range disallowed {
		if matchDomain(host, pattern) {
			return false
		}
	}
	if len(allowed) > 0 {
		for _, pattern := range allowed {
			if matchDomain(host, pattern) {
				return true
			}
		}
		return false
	}
	return true
}

// matchDomain checks if a host matches a pattern (exact or wildcard *.example.com).
func matchDomain(host string, pattern string) bool {
	host = strings.ToLower(host)
	pattern = strings.ToLower(pattern)

	if strings.HasPrefix(pattern, "*.") {
		suffix := pattern[1:] // ".example.com"
		return strings.HasSuffix(host, suffix) || (len(suffix) > 1 && host == suffix[1:])
	}
	return host == pattern
}

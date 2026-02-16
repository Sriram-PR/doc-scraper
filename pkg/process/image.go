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
	ImageDir = "images" // Subdirectory name within siteOutputDir for images
)

// ImageDownloadTask holds information needed for an image worker to process one image
type ImageDownloadTask struct {
	AbsImgURL        string
	NormImgURL       string
	BaseImgURL       *url.URL // Parsed absolute URL
	ImgHost          string
	ExtractedCaption string
	ImgLogEntry      *logrus.Entry   // Logger with image-specific context
	Ctx              context.Context // Context for this specific task
}

// ImageProcessor handles the orchestration of image downloading and processing
type ImageProcessor struct {
	store            storage.ImageStore             // DB interaction
	fetcher          *fetch.Fetcher                 // HTTP fetching
	robotsHandler    *fetch.RobotsHandler           // Robots checks
	rateLimiter      *fetch.RateLimiter             // Rate limiting
	globalSemaphore  *semaphore.Weighted            // Global concurrency limit
	hostSemaphores   map[string]*semaphore.Weighted // Per-host limits
	hostSemaphoresMu sync.Mutex                     // Mutex for hostSemaphores map
	appCfg           config.AppConfig               // Global config
	log              *logrus.Logger
}

// NewImageProcessor creates a new ImageProcessor
func NewImageProcessor(
	store storage.ImageStore,
	fetcher *fetch.Fetcher,
	robotsHandler *fetch.RobotsHandler,
	rateLimiter *fetch.RateLimiter,
	globalSemaphore *semaphore.Weighted,
	appCfg config.AppConfig,
	log *logrus.Logger,
) *ImageProcessor {
	return &ImageProcessor{
		store:           store,
		fetcher:         fetcher,
		robotsHandler:   robotsHandler,
		rateLimiter:     rateLimiter,
		globalSemaphore: globalSemaphore,
		hostSemaphores:  make(map[string]*semaphore.Weighted),
		appCfg:          appCfg,
		log:             log,
	}
}

// getHostSemaphore retrieves or creates a semaphore for rate limiting requests to a specific image host
// This is specific to the image processor as it uses its own map
func (ip *ImageProcessor) getHostSemaphore(host string) *semaphore.Weighted {
	ip.hostSemaphoresMu.Lock()
	defer ip.hostSemaphoresMu.Unlock()

	sem, exists := ip.hostSemaphores[host]
	if !exists {
		// Get limit from config, use a default if invalid
		limit := int64(ip.appCfg.MaxRequestsPerHost)
		if limit <= 0 {
			limit = 2 // Sensible default if config is 0 or negative
			ip.log.Warnf("max_requests_per_host invalid or zero for image host %s, defaulting to %d", host, limit)
		}
		sem = semaphore.NewWeighted(limit)
		ip.hostSemaphores[host] = sem
		ip.log.WithFields(logrus.Fields{"host": host, "limit": limit}).Debug("Created new image host semaphore")
	}
	return sem
}

// ProcessImages finds images within the main content, checks status, dispatches downloads to a worker pool, and returns a map of successfully processed images and any errors
// It modifies the 'data-crawl-status' attribute on img tags in the selection
func (ip *ImageProcessor) ProcessImages(
	mainContent *goquery.Selection, // Operate on the selection
	finalURL *url.URL, // Base URL of the page containing the images
	siteCfg config.SiteConfig, // Need site-specific image settings
	siteOutputDir string, // Need for calculating local paths
	taskLog *logrus.Entry, // Logger for the parent page task
	ctx context.Context, // Parent context
) (imageMap map[string]models.ImageData, imageErrs []error) {
	taskLog.Debug("Processing images...")
	imageMap = make(map[string]models.ImageData)
	imageErrs = make([]error, 0) // Collect non-fatal errors here

	// --- Determine Effective Image Handling Settings ---
	skipImages := config.GetEffectiveSkipImages(siteCfg, ip.appCfg)
	allowedDomains := siteCfg.AllowedImageDomains
	disallowedDomains := siteCfg.DisallowedImageDomains

	if skipImages {
		taskLog.Info("Skipping all image processing based on configuration.")
		mainContent.Find("img").SetAttr("data-crawl-status", "skipped-config")
		return imageMap, imageErrs // Return empty map and no errors
	}

	// --- Setup for Worker Pool ---
	var imgWg sync.WaitGroup
	var imgErrMu sync.Mutex // Protects imageMap and imageErrs slice

	// Create buffered channel for image download tasks
	numImageWorkers := ip.appCfg.NumImageWorkers
	if numImageWorkers <= 0 {
		numImageWorkers = ip.appCfg.NumWorkers // Fallback if image workers not set
	}
	imageTaskChan := make(chan ImageDownloadTask, numImageWorkers*2) // Buffer size heuristic

	// Launch the fixed pool of image workers
	taskLog.Infof("Launching %d image download workers", numImageWorkers)
	for i := 1; i <= numImageWorkers; i++ {
		go ip.imageWorker(i, imageTaskChan, siteCfg, siteOutputDir, imageMap, &imageErrs, &imgErrMu, &imgWg)
	}
	// --- End Worker Pool Setup ---

	// Ensure base image directory exists
	localImageDir := filepath.Join(siteOutputDir, ImageDir)
	if mkDirErr := os.MkdirAll(localImageDir, 0755); mkDirErr != nil {
		wrappedErr := fmt.Errorf("%w: creating base image directory '%s': %w", utils.ErrFilesystem, localImageDir, mkDirErr)
		taskLog.Error(wrappedErr)
		// Collect error but continue - workers might handle individual file errors
		imgErrMu.Lock()
		imageErrs = append(imageErrs, wrappedErr)
		imgErrMu.Unlock()
	}

	// --- Iterate Through Image Tags and Dispatch Tasks ---
	mainContent.Find("img").Each(func(index int, element *goquery.Selection) {
		// --- Synchronous Checks ---
		element.SetAttr("data-crawl-status", "pending") // Initial status
		imgSrc, exists := element.Attr("src")
		if !exists || imgSrc == "" {
			element.SetAttr("data-crawl-status", "skipped-empty-src")
			return
		}
		// Skip data URIs early
		if strings.HasPrefix(imgSrc, "data:") {
			element.SetAttr("data-crawl-status", "skipped-data-uri")
			return
		}

		// Resolve relative URL to absolute
		imgURL, imgParseErr := finalURL.Parse(imgSrc)
		if imgParseErr != nil {
			taskLog.Warnf("Image src parse error '%s': %v", imgSrc, imgParseErr)
			element.SetAttr("data-crawl-status", "error-parse")
			return
		}
		absoluteImgURL := imgURL.String()
		imgHost := imgURL.Hostname()
		imgLog := taskLog.WithFields(logrus.Fields{"img_url": absoluteImgURL, "img_host": imgHost})

		// Scheme Check
		if imgURL.Scheme != "http" && imgURL.Scheme != "https" {
			element.SetAttr("data-crawl-status", "skipped-scheme")
			return
		}

		// Domain Filtering
		isAllowed := true
		for _, pattern := range disallowedDomains {
			if matchDomain(imgHost, pattern) {
				isAllowed = false
				break
			}
		}
		if isAllowed && len(allowedDomains) > 0 {
			isAllowed = false
			for _, pattern := range allowedDomains {
				if matchDomain(imgHost, pattern) {
					isAllowed = true
					break
				}
			}
		}
		if !isAllowed {
			element.SetAttr("data-crawl-status", "skipped-domain")
			return
		}

		// Robots Check (uses the robots handler passed to ImageProcessor)
		// Determine user agent for robots check
		userAgent := siteCfg.UserAgent
		if userAgent == "" {
			userAgent = ip.appCfg.DefaultUserAgent
		}
		if !ip.robotsHandler.TestAgent(imgURL, userAgent, ctx) {
			element.SetAttr("data-crawl-status", "skipped-robots")
			return
		}

		// Normalize URL
		imgNormURLStr, _, imgNormErr := parse.ParseAndNormalize(absoluteImgURL)
		if imgNormErr != nil {
			imgLog.Warnf("Cannot normalize image URL: %v", imgNormErr)
			element.SetAttr("data-crawl-status", "error-normalize")
			return
		}

		// DB Check (uses the store passed to ImageProcessor)
		dbStatus, dbEntry, dbErr := ip.store.CheckImageStatus(imgNormURLStr)
		if dbErr != nil {
			// Log and collect DB error, Skip if DB check fails
			wrappedErr := fmt.Errorf("image DB check failed for '%s': %w", imgNormURLStr, dbErr)
			imgLog.Error(wrappedErr)
			imgErrMu.Lock()
			imageErrs = append(imageErrs, wrappedErr)
			imgErrMu.Unlock()
			element.SetAttr("data-crawl-status", "error-db")
			return
		}

		// Extract Caption (Alt/Figcaption)
		caption := ""
		figure := element.Closest("figure") // Find closest ancestor figure
		if figure.Length() > 0 {
			figcaption := figure.Find("figcaption").First() // Find figcaption within that figure
			if figcaption.Length() > 0 {
				caption = strings.TrimSpace(figcaption.Text())
			}
		}
		// Fallback to alt attribute if no figcaption found or caption is empty
		if caption == "" {
			if alt, altExists := element.Attr("alt"); altExists {
				caption = strings.TrimSpace(alt)
			}
		}

		// --- Determine if Download Task Needs Dispatching ---
		shouldDispatch := false
		if dbStatus == models.ImageStatusSuccess {
			if dbEntry != nil && dbEntry.LocalPath != "" {
				// Successfully downloaded previously, reuse data
				element.SetAttr("data-crawl-status", "success") // Mark success (cached)
				imgErrMu.Lock()
				imageMap[absoluteImgURL] = models.ImageData{
					OriginalURL: absoluteImgURL,
					LocalPath:   dbEntry.LocalPath,
					Caption:     caption, // Use newly extracted caption
				}
				imgErrMu.Unlock()
			} else {
				// DB state is inconsistent (success but no path)
				imgLog.Warnf("Image DB status 'success' but invalid entry (missing path) for '%s'. Re-scheduling download.", imgNormURLStr)
				shouldDispatch = true
				element.SetAttr("data-crawl-status", "pending-download") // Mark for download
			}
		} else if dbStatus == models.ImageStatusFailure {
			// Previously failed, try again
			errMsg := "Unknown reason"
			if dbEntry != nil {
				errMsg = dbEntry.ErrorType
			}
			imgLog.Warnf("Image previously failed download ('%s'). Re-scheduling.", errMsg)
			shouldDispatch = true
			element.SetAttr("data-crawl-status", "pending-download") // Mark for download
		} else { // ImageStatusNotFound or ImageStatusDBError (though we return early on db_error now)
			imgLog.Debugf("Image '%s' new or previously failed check ('%s'). Scheduling download.", imgSrc, dbStatus)
			shouldDispatch = true
			element.SetAttr("data-crawl-status", "pending-download") // Mark for download
		}

		// --- Dispatch Task to Worker Pool (if needed) ---
		if shouldDispatch {
			imgLog.Debug("Dispatching image download task to worker pool.")
			task := ImageDownloadTask{
				AbsImgURL:        absoluteImgURL,
				NormImgURL:       imgNormURLStr,
				BaseImgURL:       imgURL, // Pass the parsed URL object
				ImgHost:          imgHost,
				ExtractedCaption: caption,
				ImgLogEntry:      imgLog, // Pass the specific logger
				Ctx:              ctx,    // Pass the parent context
			}
			imgWg.Add(1) // Increment WG *before* sending

			// Send task to the channel (blocks if buffer is full)
			select {
			case imageTaskChan <- task:
				// Successfully sent
			case <-ctx.Done():
				imgLog.Warnf("Context cancelled while trying to dispatch image task for '%s': %v", imgSrc, ctx.Err())
				imgWg.Done()                                                   // Decrement WG as task won't be processed
				element.SetAttr("data-crawl-status", "error-dispatch-context") // Mark as error
			}
		}
	}) // End mainContent.Find("img").Each

	// --- Signal Workers and Wait ---
	taskLog.Debug("Finished dispatching all image tasks for this page. Closing task channel.")
	close(imageTaskChan) // Close channel to signal workers no more tasks are coming for *this page*

	taskLog.Debug("Waiting for image download workers to finish...")
	imgWg.Wait() // Wait for all tasks dispatched *for this page* to complete
	taskLog.Debug("All image download workers finished for this page.")
	// --- End Signal and Wait ---

	if len(imageErrs) > 0 {
		taskLog.Warnf("Finished image processing for page with %d non-fatal error(s).", len(imageErrs))
	} else {
		taskLog.Debug("Image processing complete for page.")
	}
	return imageMap, imageErrs
}

// imageWorker processes image download tasks received from a channel
func (ip *ImageProcessor) imageWorker(
	id int, // Worker ID for logging
	taskChan <-chan ImageDownloadTask, // Channel to receive tasks
	siteCfg config.SiteConfig, // Pass siteCfg for UA, delay etc.
	siteOutputDir string, // Pass output dir base
	imageMap map[string]models.ImageData, // Shared map for results (needs mutex)
	imageErrs *[]error, // Shared slice for errors (needs mutex)
	imgErrMu *sync.Mutex, // Mutex for map and slice
	imgWg *sync.WaitGroup, // WaitGroup to signal task completion
) {
	workerLog := ip.log.WithField("image_worker_id", id)
	workerLog.Debug("Image worker started")

	// Process tasks from the channel until it's closed
	for task := range taskChan {
		// Call the function to process this single task
		// Pass necessary dependencies down
		ip.processSingleImageTask(task, siteCfg, siteOutputDir, imageMap, imageErrs, imgErrMu, imgWg)
	}

	workerLog.Debug("Image worker finished (task channel closed)")
}

// processSingleImageTask handles the download, saving, and DB update for one image
func (ip *ImageProcessor) processSingleImageTask(
	task ImageDownloadTask, // The specific task data
	siteCfg config.SiteConfig, // Need site specific settings
	siteOutputDir string, // Need output base
	imageMap map[string]models.ImageData, // Shared map
	imageErrs *[]error, // Shared slice
	imgErrMu *sync.Mutex, // Mutex
	imgWg *sync.WaitGroup, // WaitGroup
) {
	// --- Get context from task ---
	ctx := task.Ctx
	if ctx == nil { // Safety check
		ctx = context.Background()
	}

	// --- Setup variables specific to this task ---
	imgLogEntry := task.ImgLogEntry // Use logger passed in task
	imgHost := task.ImgHost
	var imgTaskErr error // Primary error for this specific task
	imgDownloaded := false
	imgLocalPath := "" // Relative path, set on success
	var copiedBytes int64 = 0

	// --- Defer DB update, panic recovery, and WaitGroup decrement ---
	defer func() {
		// --- Panic Recovery ---
		if r := recover(); r != nil {
			imgTaskErr = fmt.Errorf("panic processing img '%s': %v", task.AbsImgURL, r)
			stackTrace := string(debug.Stack())
			imgLogEntry.WithFields(logrus.Fields{"panic_info": r, "stack_trace": stackTrace}).Error("PANIC Recovered in processSingleImageTask")
			// Collect error (needs mutex)
			imgErrMu.Lock()
			*imageErrs = append(*imageErrs, imgTaskErr) // Use the error created above
			imgErrMu.Unlock()
		}

		// --- DB Status Update ---
		now := time.Now()
		var entryToSave models.ImageDBEntry
		if imgTaskErr == nil && imgDownloaded { // Success path
			entryToSave = models.ImageDBEntry{
				Status:      models.ImageStatusSuccess,
				LocalPath:   imgLocalPath,
				Caption:     task.ExtractedCaption,
				LastAttempt: now,
			}
		} else { // Failure path
			errorType := "UnknownDownloadFailure"
			if imgTaskErr != nil {
				errorType = utils.CategorizeError(imgTaskErr) // Use utility function
			}
			entryToSave = models.ImageDBEntry{
				Status:      models.ImageStatusFailure,
				ErrorType:   errorType,
				LastAttempt: now,
			}
			// Log & Collect the primary error if it wasn't a panic
			if imgTaskErr != nil && recover() == nil { // Avoid double-collecting/logging panic errors
				imgLogEntry.Warnf("Image download/save failed: %v", imgTaskErr)
				imgErrMu.Lock()
				*imageErrs = append(*imageErrs, imgTaskErr)
				imgErrMu.Unlock()
			}
		}
		// Update DB using the store interface
		if updateErr := ip.store.UpdateImageStatus(task.NormImgURL, &entryToSave); updateErr != nil {
			// If DB update fails, log it and collect the error too
			dbUpdateErr := fmt.Errorf("failed update DB status img '%s' to '%s': %w", task.NormImgURL, entryToSave.Status, updateErr)
			imgLogEntry.Error(dbUpdateErr)
			imgErrMu.Lock()
			*imageErrs = append(*imageErrs, dbUpdateErr)
			imgErrMu.Unlock()
		}

		// --- Signal WaitGroup Completion ---
		imgWg.Done()
	}() // --- End Defer ---

	// --- Determine Effective Settings ---
	userAgent := siteCfg.UserAgent
	if userAgent == "" {
		userAgent = ip.appCfg.DefaultUserAgent
	}
	imgHostDelay := siteCfg.DelayPerHost
	if imgHostDelay <= 0 {
		imgHostDelay = ip.appCfg.DefaultDelayPerHost
	}
	semTimeout := ip.appCfg.SemaphoreAcquireTimeout
	effectiveMaxBytes := config.GetEffectiveMaxImageSize(siteCfg, ip.appCfg)
	localImageDir := filepath.Join(siteOutputDir, ImageDir) // Base image dir for this site

	// --- Acquire Semaphores & Apply Rate Limit ---
	// Use a closure to manage semaphore release with scoped defer
	semAcquireErr := func() error {
		// 1. Acquire Host Semaphore
		imgHostSem := ip.getHostSemaphore(imgHost) // Use processor's method
		ctxIH, cancelIH := context.WithTimeout(ctx, semTimeout)
		defer cancelIH()
		semErr := imgHostSem.Acquire(ctxIH, 1)
		if semErr != nil {
			if errors.Is(semErr, context.DeadlineExceeded) {
				return fmt.Errorf("%w: acquiring host semaphore for img '%s': %w", utils.ErrSemaphoreTimeout, task.AbsImgURL, semErr)
			}
			return fmt.Errorf("failed acquiring host semaphore for img '%s': %w", task.AbsImgURL, semErr)
		}
		// Release host semaphore when closure returns
		defer imgHostSem.Release(1)

		// 2. Acquire Global Semaphore
		ctxIG, cancelIG := context.WithTimeout(ctx, semTimeout)
		defer cancelIG()
		semErr = ip.globalSemaphore.Acquire(ctxIG, 1) // Use processor's global semaphore
		if semErr != nil {
			// Release host semaphore here as global failed *after* acquiring host
			// Note: defer above handles release if *this function* returns error
			if errors.Is(semErr, context.DeadlineExceeded) {
				return fmt.Errorf("%w: acquiring global semaphore for img '%s': %w", utils.ErrSemaphoreTimeout, task.AbsImgURL, semErr)
			}
			return fmt.Errorf("failed acquiring global semaphore for img '%s': %w", task.AbsImgURL, semErr)
		}
		// Release global semaphore when closure returns
		defer ip.globalSemaphore.Release(1)

		// 3. Apply Rate Limit (After acquiring semaphores)
		ip.rateLimiter.ApplyDelay(imgHost, imgHostDelay) // Use processor's rate limiter

		return nil // Success
	}() // Execute the closure immediately

	if semAcquireErr != nil {
		imgTaskErr = semAcquireErr // Assign the semaphore/rate limit error
		return                     // Return triggers defer cleanup (DB update to failure, WG done)
	}

	// --- Fetch Image Request ---
	imgLogEntry.Debug("Attempting fetch image request")
	imgReq, reqErr := http.NewRequestWithContext(ctx, "GET", task.AbsImgURL, nil)
	if reqErr != nil {
		imgTaskErr = fmt.Errorf("%w: creating request for img '%s': %w", utils.ErrRequestCreation, task.AbsImgURL, reqErr)
		ip.rateLimiter.UpdateLastRequestTime(imgHost)
		return
	}
	imgReq.Header.Set("User-Agent", userAgent)

	// --- Perform Fetch with Retries ---
	imgResp, imgFetchErr := ip.fetcher.FetchWithRetry(imgReq, ctx) // Use processor's fetcher
	ip.rateLimiter.UpdateLastRequestTime(imgHost)                  // Update last request time *after* the attempt

	if imgFetchErr != nil {
		imgTaskErr = fmt.Errorf("fetch failed for img '%s': %w", task.AbsImgURL, imgFetchErr)
		// Ensure body is closed if fetch failed but returned a response
		if imgResp != nil {
			io.Copy(io.Discard, imgResp.Body)
			imgResp.Body.Close()
		}
		return // Triggers defer
	}
	// If fetch succeeded, imgResp is non-nil and status is 2xx
	defer imgResp.Body.Close()

	// --- Header Size Check ---
	headerSizeStr := imgResp.Header.Get("Content-Length")
	if headerSizeStr != "" {
		if headerSize, parseHdrErr := strconv.ParseInt(headerSizeStr, 10, 64); parseHdrErr == nil {
			if effectiveMaxBytes > 0 && headerSize > effectiveMaxBytes {
				imgTaskErr = fmt.Errorf("image '%s' exceeds max size based on header (%d > %d bytes)", task.AbsImgURL, headerSize, effectiveMaxBytes)
				io.Copy(io.Discard, imgResp.Body) // Drain body before returning
				return                            // Triggers defer
			}
		} else {
			imgLogEntry.Warnf("Could not parse Content-Length header '%s'", headerSizeStr)
		}
	}

	// --- Generate Local Filename ---
	localFilename, fileExtErr := generateLocalFilename(task.BaseImgURL, task.AbsImgURL, imgResp.Header.Get("Content-Type"), imgLogEntry)
	if fileExtErr != nil {
		imgTaskErr = fileExtErr // Assign error from filename generation
		io.Copy(io.Discard, imgResp.Body)
		return // Triggers defer
	}
	localFilePath := filepath.Join(localImageDir, localFilename)

	// Calculate relative path for storing in DB and map
	relativeFilePath, relErr := filepath.Rel(siteOutputDir, localFilePath)
	if relErr != nil {
		// This shouldn't typically fail if siteOutputDir and localFilePath are sane
		imgLogEntry.Warnf("Could not calculate relative path from '%s' to '%s': %v. Using filename only.", siteOutputDir, localFilePath, relErr)
		relativeFilePath = localFilename // Fallback to just the filename
	}
	// Ensure forward slashes for storage consistency
	relativeFilePath = filepath.ToSlash(relativeFilePath)
	imgLogEntry.Debugf("Final image save path: %s (Relative: %s)", localFilePath, relativeFilePath)

	// --- Ensure Output Directory Exists ---
	// MkdirAll is idempotent, safe to call even if check was done earlier
	if mkDirErr := os.MkdirAll(localImageDir, 0755); mkDirErr != nil {
		imgTaskErr = fmt.Errorf("%w: ensuring image directory '%s' exists: %w", utils.ErrFilesystem, localImageDir, mkDirErr)
		io.Copy(io.Discard, imgResp.Body)
		return // Triggers defer
	}

	// --- Create Destination File ---
	outFile, createErr := os.Create(localFilePath)
	if createErr != nil {
		imgTaskErr = fmt.Errorf("%w: creating image file '%s': %w", utils.ErrFilesystem, localFilePath, createErr)
		io.Copy(io.Discard, imgResp.Body)
		return // Triggers defer
	}
	// Use defer for closing outFile to handle errors during copy
	defer func() {
		if err := outFile.Close(); err != nil && imgTaskErr == nil {
			// Only capture close error if no other error happened before it
			imgTaskErr = fmt.Errorf("%w: closing image file '%s' after write: %w", utils.ErrFilesystem, localFilePath, err)
		}
	}()

	// --- Stream Data using io.Copy with Size Limit ---
	var reader io.Reader = imgResp.Body
	if effectiveMaxBytes > 0 {
		reader = io.LimitReader(imgResp.Body, effectiveMaxBytes)
	}

	imgLogEntry.Debugf("Streaming image data to %s", localFilePath)
	var copyErr error
	copiedBytes, copyErr = io.Copy(outFile, reader)

	// Drain any remaining data from the original response body to allow connection reuse
	// This is important especially if LimitReader stopped reading early
	_, drainErr := io.Copy(io.Discard, imgResp.Body)
	if drainErr != nil {
		imgLogEntry.Warnf("Error draining response body after copy: %v", drainErr)
		// Don't override primary copy error if one occurred
	}
	// Response body is closed by the earlier defer imgResp.Body.Close()

	// --- Handle io.Copy Errors ---
	if copyErr != nil {
		imgTaskErr = fmt.Errorf("%w: copying image data to '%s' (copied %d bytes): %w", utils.ErrFilesystem, localFilePath, copiedBytes, copyErr)
		// Need to close before removing on some OS (Windows)
		outFile.Close()          // Close explicitly before remove
		os.Remove(localFilePath) // Attempt cleanup
		return                   // Triggers main defer
	}

	// --- Check if Size Limit Was Hit During Copy ---
	// We need to check this *after* draining the original body,
	// because LimitReader might have stopped reading *exactly* at the limit
	// The check needs to see if the limit was *reached*.
	if effectiveMaxBytes > 0 && copiedBytes >= effectiveMaxBytes {
		// Check if the original Content-Length was also above the limit
		sizeExceeded := true
		if headerSizeStr != "" {
			if headerSize, _ := strconv.ParseInt(headerSizeStr, 10, 64); headerSize <= effectiveMaxBytes {
				imgLogEntry.Warnf("Copied bytes (%d) >= limit (%d), but Content-Length (%d) was <= limit.", copiedBytes, effectiveMaxBytes, headerSize)
			}
		}

		if sizeExceeded {
			imgTaskErr = fmt.Errorf("image '%s' exceeds max size (%d >= %d bytes, download truncated)", task.AbsImgURL, copiedBytes, effectiveMaxBytes)
			outFile.Close()          // Close explicitly before remove
			os.Remove(localFilePath) // Attempt cleanup
			return                   // Triggers main defer
		}
	}

	// --- File Save Success ---
	// The deferred outFile.Close() will run. If it errors, it sets imgTaskErr
	// Check imgTaskErr *after* the defer has potentially run (conceptually)
	// The defer logic already handles this: it updates DB based on imgTaskErr

	// If no error occurred up to this point (including potential outFile.Close error)
	if imgTaskErr == nil {
		imgDownloaded = true
		imgLocalPath = relativeFilePath // Use the calculated relative path

		// Update the shared map (requires mutex)
		imgErrMu.Lock()
		imageMap[task.AbsImgURL] = models.ImageData{
			OriginalURL: task.AbsImgURL,
			LocalPath:   imgLocalPath,
			Caption:     task.ExtractedCaption,
		}
		imgErrMu.Unlock()

		imgLogEntry.Debugf("Successfully saved image (%d bytes)", copiedBytes)
	}
	// --- Task processing finished ---
	// Return will trigger the main defer block for cleanup/DB update/WG decrement
}

// generateLocalFilename creates a unique and safe filename for a downloaded image
func generateLocalFilename(baseImgURL *url.URL, absImgURL string, contentType string, imgLogEntry *logrus.Entry) (string, error) {
	// 1. Get Base Name and Original Extension from URL Path
	originalExt := path.Ext(baseImgURL.Path)
	imgBaseName := utils.SanitizeFilename(strings.TrimSuffix(path.Base(baseImgURL.Path), originalExt))
	if imgBaseName == "" || imgBaseName == "_" {
		// Handle cases where sanitization results in empty/underscore
		// Use a hash of the URL or a default name
		urlHashOnly := fmt.Sprintf("%x", sha256.Sum256([]byte(absImgURL)))[:12] // Longer hash if base name is missing
		imgBaseName = "image_" + urlHashOnly
		imgLogEntry.Debugf("Sanitized base name was empty, using hash fallback: %s", imgBaseName)
	}

	// 2. Determine Final Extension based on Content-Type and URL
	finalExt := originalExt // Start with extension from URL

	if contentType != "" {
		mimeType, _, mimeErr := mime.ParseMediaType(contentType)
		if mimeErr == nil {
			// Prefer common extensions if MIME type matches
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
				// Try getting extensions from MIME type if not common
				extensions, extErr := mime.ExtensionsByType(mimeType)
				if extErr == nil && len(extensions) > 0 {
					// Prioritize known good extensions if multiple exist
					preferredExt := ""
					for _, ext := range extensions {
						if ext == ".jpg" || ext == ".jpeg" || ext == ".png" || ext == ".gif" || ext == ".webp" || ext == ".svg" {
							preferredExt = ext
							break
						}
					}
					if preferredExt != "" {
						finalExt = preferredExt
					} else if finalExt == "" { // Use first extension if URL had none
						finalExt = extensions[0]
					}
					// Keep originalExt if MIME type didn't yield a better one
				} else if finalExt == "" {
					// Cannot determine extension from MIME, and URL had none
					return "", fmt.Errorf("cannot determine file extension (MIME: %s, MIME extensions error: %v, URL Ext: none)", mimeType, extErr)
				}
			}
		} else {
			imgLogEntry.Warnf("Could not parse Content-Type header '%s': %v", contentType, mimeErr)
			if finalExt == "" {
				// Cannot determine extension from Content-Type, and URL had none
				return "", fmt.Errorf("cannot determine file extension (unparsable Content-Type, no URL extension)")
			}
		}
	} else if finalExt == "" {
		// Cannot determine extension (no Content-Type, no URL extension)
		return "", fmt.Errorf("cannot determine file extension (no Content-Type, no URL extension)")
	}

	// Ensure extension starts with a dot
	if finalExt != "" && !strings.HasPrefix(finalExt, ".") {
		finalExt = "." + finalExt
	}

	// 3. Add Hash for Uniqueness
	// Use a short hash of the *full absolute URL* to disambiguate files with the same base name but different paths/queries
	urlHash := fmt.Sprintf("%x", sha256.Sum256([]byte(absImgURL)))[:8] // 8-char hex hash

	// 4. Construct Final Filename
	// Format: sanitizedBaseName_hash.finalExt
	localFilename := fmt.Sprintf("%s_%s%s", imgBaseName, urlHash, finalExt)

	return localFilename, nil
}

// matchDomain checks if a host matches a pattern (exact or simple wildcard *. TLD)
// This is a helper used by ProcessImages.
func matchDomain(host string, pattern string) bool {
	host = strings.ToLower(host)
	pattern = strings.ToLower(pattern)

	if strings.HasPrefix(pattern, "*.") {
		// Wildcard match: *.example.com matches host=sub.example.com or host=example.com
		suffix := pattern[1:] // Get ".example.com"
		// Check if host ends with the suffix OR if host is exactly the suffix without the leading dot
		return strings.HasSuffix(host, suffix) || (len(suffix) > 1 && host == suffix[1:])
	}
	// Exact match
	return host == pattern
}

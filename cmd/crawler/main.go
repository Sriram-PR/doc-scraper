package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"

	"doc-scraper/pkg/config"
	"doc-scraper/pkg/crawler"
	"doc-scraper/pkg/fetch"
	"doc-scraper/pkg/storage"
	"doc-scraper/pkg/utils"
)

func main() {
	// --- Set profiling rates ---=
	runtime.SetBlockProfileRate(1000)
	runtime.SetMutexProfileFraction(1000)

	// --- Early Initialization & Flags ---
	log := logrus.New()
	log.SetFormatter(&logrus.TextFormatter{FullTimestamp: true, TimestampFormat: "15:04:05.000"})
	log.SetLevel(logrus.InfoLevel) // Default level

	configFileFlag := flag.String("config", "config.yaml", "Path to YAML config file")
	siteKeyFlag := flag.String("site", "", "Site key from config file (required)")
	logLevelFlag := flag.String("loglevel", "info", "Log level (debug, info, warn, error, fatal)")
	resumeFlag := flag.Bool("resume", false, "Resume crawl using existing state DB")
	writeVisitedLogFlag := flag.Bool("write-visited-log", false, "Write a final log file of all visited URLs from DB")
	pprofAddr := flag.String("pprof", "localhost:6060", "Address for pprof HTTP server (e.g., ':6060', empty to disable)")
	flag.Parse()

	// --- Logger Configuration ---
	level, err := logrus.ParseLevel(*logLevelFlag)
	if err != nil {
		log.Warnf("Invalid log level '%s', using default 'info'. Error: %v", *logLevelFlag, err)
	} else {
		log.SetLevel(level)
		log.Infof("Setting log level to: %s", level.String())
	}

	// --- Load Application Configuration ---
	log.Infof("Loading configuration from %s", *configFileFlag)
	yamlFile, err := os.ReadFile(*configFileFlag)
	if err != nil {
		log.Fatalf("Read config file '%s' error: %v", *configFileFlag, err)
	}
	var appCfg config.AppConfig
	err = yaml.Unmarshal(yamlFile, &appCfg)
	if err != nil {
		log.Fatalf("Parse config file '%s' error: %v", *configFileFlag, err)
	}

	// --- Validate Global App Configuration ---
	validateAppConfig(&appCfg, log) // Pass by pointer to modify defaults

	// Log effective global config
	logAppConfig(&appCfg, log)

	// --- Select and Validate Site-Specific Configuration ---
	if *siteKeyFlag == "" {
		log.Fatalf("Error: -site flag is required.")
	}
	siteCfg, ok := appCfg.Sites[*siteKeyFlag]
	if !ok {
		log.Fatalf("Error: Site key '%s' not found in config file '%s'", *siteKeyFlag, *configFileFlag)
	}
	// Validate Site Config (Keep validation here or move to config package)
	if err := validateSiteConfig(&siteCfg, log); err != nil {
		log.Fatalf("Site '%s' configuration error: %v", *siteKeyFlag, err)
	}
	log.Infof("Site Config for '%s': Domain: %s, Prefix: %s, ContentSel: '%s', ...",
		*siteKeyFlag, siteCfg.AllowedDomain, siteCfg.AllowedPathPrefix, siteCfg.ContentSelector)

	// --- Start pprof HTTP Server (Optional) ---
	if *pprofAddr != "" {
		go func() {
			log.Infof("Starting pprof HTTP server on: http://%s/debug/pprof/", *pprofAddr)
			if err := http.ListenAndServe(*pprofAddr, nil); err != nil {
				log.Errorf("Pprof server failed to start on %s: %v", *pprofAddr, err)
			}
		}()
	} else {
		log.Info("Pprof server disabled (no -pprof address provided).")
	}

	// ===========================================================
	// == Setup Global Context & Signal Handling ==
	// ===========================================================
	var crawlCtx context.Context
	var cancelCrawl context.CancelFunc

	if appCfg.GlobalCrawlTimeout > 0 {
		log.Infof("Setting global crawl timeout: %v", appCfg.GlobalCrawlTimeout)
		crawlCtx, cancelCrawl = context.WithTimeout(context.Background(), appCfg.GlobalCrawlTimeout)
	} else {
		log.Info("No global crawl timeout set.")
		crawlCtx, cancelCrawl = context.WithCancel(context.Background())
	}
	// Defer cancel() *very early* to ensure it's called on any exit path
	defer cancelCrawl()

	// Channel to listen for OS signals for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Goroutine to handle signals -> cancel context -> force exit on second signal
	go func() {
		sig := <-sigChan
		log.Warnf("Received signal: %v. Initiating graceful shutdown...", sig)
		cancelCrawl() // Trigger shutdown via context cancellation

		// Allow force exit on second signal or timeout
		select {
		case sig = <-sigChan:
			log.Warnf("Received second signal: %v. Forcing exit.", sig)
			os.Exit(1)
		case <-time.After(30 * time.Second): // Graceful shutdown timeout
			log.Warn("Graceful shutdown period exceeded after signal. Forcing exit.")
			os.Exit(1)
		}
	}()
	// Stop signal handling explicitly *before* main exits normally
	defer signal.Stop(sigChan)

	// ===========================================================
	// == Initialize Components ==
	// ===========================================================
	log.Info("Initializing components...")

	// --- Storage ---
	store, err := storage.NewBadgerStore(crawlCtx, appCfg.StateDir, siteCfg.AllowedDomain, *resumeFlag, log)
	if err != nil {
		log.Fatalf("Failed to initialize visited DB: %v", err)
	}
	defer store.Close() // Ensure DB is closed on exit

	// Start DB GC goroutine
	go store.RunGC(crawlCtx, 10*time.Minute) // Pass context for cancellation

	// --- HTTP Fetching Components ---
	httpClient := fetch.NewClient(appCfg.HTTPClientSettings, log)
	fetcher := fetch.NewFetcher(httpClient, appCfg, log)
	rateLimiter := fetch.NewRateLimiter(appCfg.DefaultDelayPerHost, log)

	// --- Crawler Instance ---
	crawlerInstance, err := crawler.NewCrawler(
		appCfg,
		siteCfg,
		log,
		store,       // Inject store
		fetcher,     // Inject fetcher
		rateLimiter, // Inject rate limiter
		crawlCtx,    // Pass context
		cancelCrawl, // Pass cancel func
	)
	if err != nil {
		log.Fatalf("Failed to initialize crawler: %v", err)
	}

	// ===========================================================
	// == Start Crawler Execution ==
	// ===========================================================
	err = crawlerInstance.Run(*resumeFlag) // Run the crawl

	// ===========================================================
	// == Post-Crawl Actions ==
	// ===========================================================

	// --- Generate Directory Structure File (Only on Success) ---
	if err == nil {
		log.Info("Crawl completed successfully, generating directory structure file...")
		targetDir := filepath.Join(appCfg.OutputBaseDir, utils.SanitizeFilename(siteCfg.AllowedDomain))
		outputFileName := fmt.Sprintf("%s_structure.txt", utils.SanitizeFilename(siteCfg.AllowedDomain))
		outputFilePath := filepath.Join(appCfg.OutputBaseDir, outputFileName)

		// Call the utility function, passing the logger
		if treeErr := utils.GenerateAndSaveTreeStructure(targetDir, outputFilePath, log); treeErr != nil {
			log.Errorf("Failed to generate or save directory structure: %v", treeErr)
		} else {
			log.Infof("Successfully saved directory structure to %s", outputFilePath)
		}
	} else {
		log.Warnf("Skipping directory structure generation due to crawl error: %v", err)
	}

	// --- Final Visited Log File Generation (Optional) ---
	// Needs to run *after* crawler finishes but *before* DB is closed by defer
	// Check context error first, might not make sense to write log if cancelled early
	if crawlCtx.Err() != nil {
		log.Warnf("Skipping final visited log due to crawl context error: %v", crawlCtx.Err())
	} else if *writeVisitedLogFlag {
		visitedFilename := fmt.Sprintf("%s-visited.txt", utils.SanitizeFilename(siteCfg.AllowedDomain))
		visitedFilePath := filepath.Join(appCfg.OutputBaseDir, visitedFilename)
		if writeErr := store.WriteVisitedLog(visitedFilePath); writeErr != nil {
			log.Errorf("Error writing final visited log: %v", writeErr)
		}
	} else {
		log.Info("Skipping final visited URL log file generation.")
	}

	// --- Exit ---
	// Check the error returned by the crawler run
	if err != nil {
		if errors.Is(err, context.Canceled) {
			log.Warn("Crawl cancelled gracefully.")
			// os.Exit(1)
			os.Exit(0)
		} else if errors.Is(err, context.DeadlineExceeded) {
			log.Error("Crawl timed out (global timeout).")
			os.Exit(1)
		} else {
			log.Errorf("Crawl finished with error: %v", err)
			os.Exit(1)
		}
	}

	log.Info("Crawl completed successfully.")
	os.Exit(0)
}

// validateAppConfig checks global config values and sets defaults
// Operates on a pointer to modify the struct directly
func validateAppConfig(appCfg *config.AppConfig, log *logrus.Logger) {
	if appCfg.NumWorkers <= 0 {
		log.Warnf("num_workers should be > 0, defaulting to 4")
		appCfg.NumWorkers = 4
	}
	if appCfg.NumImageWorkers <= 0 {
		log.Warnf("num_image_workers not specified or invalid, defaulting to num_workers (%d)", appCfg.NumWorkers)
		appCfg.NumImageWorkers = appCfg.NumWorkers
	}
	if appCfg.MaxRequests <= 0 {
		log.Warnf("max_requests should be > 0, defaulting to 10")
		appCfg.MaxRequests = 10
	}
	if appCfg.MaxRequestsPerHost <= 0 {
		log.Warnf("max_requests_per_host should be > 0, defaulting to 2")
		appCfg.MaxRequestsPerHost = 2
	}
	if appCfg.OutputBaseDir == "" {
		log.Warn("output_base_dir is empty, defaulting to './crawled_docs'")
		appCfg.OutputBaseDir = "./crawled_docs"
	}
	if appCfg.StateDir == "" {
		log.Warn("state_dir is empty, defaulting to './crawler_state'")
		appCfg.StateDir = "./crawler_state"
	}
	if appCfg.MaxRetries < 0 { // Allow 0 retries
		log.Warnf("max_retries cannot be negative, setting to 0")
		appCfg.MaxRetries = 0
	} else if appCfg.MaxRetries == 0 && appCfg.InitialRetryDelay == 0 {
		// Set default only if not explicitly set
		appCfg.MaxRetries = 3 // Default from original
	}

	if appCfg.InitialRetryDelay <= 0 {
		if appCfg.MaxRetries > 0 { // Only default delay if retries are enabled
			appCfg.InitialRetryDelay = 1 * time.Second
		}
	}
	if appCfg.MaxRetryDelay <= 0 {
		if appCfg.MaxRetries > 0 {
			appCfg.MaxRetryDelay = 30 * time.Second
		}
	}
	if appCfg.InitialRetryDelay > appCfg.MaxRetryDelay && appCfg.MaxRetryDelay > 0 {
		log.Warnf("initial_retry_delay (%v) > max_retry_delay (%v), using max_retry_delay for initial", appCfg.InitialRetryDelay, appCfg.MaxRetryDelay)
		appCfg.InitialRetryDelay = appCfg.MaxRetryDelay
	}
	if appCfg.SemaphoreAcquireTimeout <= 0 {
		appCfg.SemaphoreAcquireTimeout = 30 * time.Second
	}
	if appCfg.GlobalCrawlTimeout < 0 {
		log.Warnf("global_crawl_timeout cannot be negative, disabling timeout")
		appCfg.GlobalCrawlTimeout = 0 // 0 means disabled
	}
	if appCfg.MaxImageSizeBytes < 0 {
		log.Warnf("max_image_size_bytes cannot be negative, setting to 0 (unlimited)")
		appCfg.MaxImageSizeBytes = 0 // 0 means unlimited
	}

	// Validate HTTP Client Settings
	if appCfg.HTTPClientSettings.Timeout <= 0 {
		appCfg.HTTPClientSettings.Timeout = 45 * time.Second
	}
	if appCfg.HTTPClientSettings.MaxIdleConns <= 0 {
		appCfg.HTTPClientSettings.MaxIdleConns = 100
	}
	if appCfg.HTTPClientSettings.MaxIdleConnsPerHost <= 0 {
		appCfg.HTTPClientSettings.MaxIdleConnsPerHost = 2
	}
	if appCfg.HTTPClientSettings.IdleConnTimeout <= 0 {
		appCfg.HTTPClientSettings.IdleConnTimeout = 90 * time.Second
	}
	if appCfg.HTTPClientSettings.TLSHandshakeTimeout <= 0 {
		appCfg.HTTPClientSettings.TLSHandshakeTimeout = 10 * time.Second
	}
	if appCfg.HTTPClientSettings.ExpectContinueTimeout <= 0 {
		appCfg.HTTPClientSettings.ExpectContinueTimeout = 1 * time.Second
	}
	if appCfg.HTTPClientSettings.DialerTimeout <= 0 {
		appCfg.HTTPClientSettings.DialerTimeout = 15 * time.Second
	}
	if appCfg.HTTPClientSettings.DialerKeepAlive <= 0 {
		appCfg.HTTPClientSettings.DialerKeepAlive = 30 * time.Second
	}
}

// logAppConfig logs the effective global configuration
func logAppConfig(appCfg *config.AppConfig, log *logrus.Logger) {
	log.Infof("Global Config: Workers:%d, ImageWorkers:%d, MaxReqs:%d, MaxReqPerHost:%d",
		appCfg.NumWorkers, appCfg.NumImageWorkers, appCfg.MaxRequests, appCfg.MaxRequestsPerHost)
	log.Infof("Global Config: DefaultDelay:%v, StateDir:%s, OutputDir:%s",
		appCfg.DefaultDelayPerHost, appCfg.StateDir, appCfg.OutputBaseDir)
	log.Infof("Global Config Retries: Max:%d, InitialDelay:%v, MaxDelay:%v",
		appCfg.MaxRetries, appCfg.InitialRetryDelay, appCfg.MaxRetryDelay)
	log.Infof("Global Config Timeouts: SemaphoreAcquire:%v, GlobalCrawl:%v",
		appCfg.SemaphoreAcquireTimeout, appCfg.GlobalCrawlTimeout)
	log.Infof("Global Config Images: Skip:%t, MaxSize:%d bytes",
		appCfg.SkipImages, appCfg.MaxImageSizeBytes)
	log.Infof("Global Config HTTP Client: Timeout:%v, MaxIdle:%d, MaxIdlePerHost:%d, IdleTimeout:%v, TLSTimeout:%v, DialerTimeout:%v",
		appCfg.HTTPClientSettings.Timeout, appCfg.HTTPClientSettings.MaxIdleConns, appCfg.HTTPClientSettings.MaxIdleConnsPerHost,
		appCfg.HTTPClientSettings.IdleConnTimeout, appCfg.HTTPClientSettings.TLSHandshakeTimeout, appCfg.HTTPClientSettings.DialerTimeout)
}

// validateSiteConfig checks site-specific config - Operates on pointer to modify prefix
func validateSiteConfig(siteCfg *config.SiteConfig, log *logrus.Logger) error {
	if len(siteCfg.StartURLs) == 0 {
		return fmt.Errorf("%w: site has no start_urls", utils.ErrConfigValidation)
	}
	if siteCfg.AllowedDomain == "" {
		return fmt.Errorf("%w: site needs allowed_domain", utils.ErrConfigValidation)
	}
	// Normalize path prefix
	if siteCfg.AllowedPathPrefix == "" {
		siteCfg.AllowedPathPrefix = "/"
	} else if !strings.HasPrefix(siteCfg.AllowedPathPrefix, "/") {
		siteCfg.AllowedPathPrefix = "/" + siteCfg.AllowedPathPrefix
	}
	if siteCfg.ContentSelector == "" {
		return fmt.Errorf("%w: site needs content_selector", utils.ErrConfigValidation)
	}
	// Check integer/duration fields for sanity if needed
	if siteCfg.MaxDepth < 0 {
		log.Warnf("Site MaxDepth cannot be negative, setting to 0 (unlimited)")
		siteCfg.MaxDepth = 0
	}
	if siteCfg.MaxImageSizeBytes != nil && *siteCfg.MaxImageSizeBytes < 0 {
		log.Warnf("Site MaxImageSizeBytes cannot be negative, setting to 0 (unlimited override)")
		*siteCfg.MaxImageSizeBytes = 0
	}

	// Note: Start URL validation (format, scope) happens in Crawler.Run
	return nil
}

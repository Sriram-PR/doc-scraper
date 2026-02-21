package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"

	"github.com/Sriram-PR/doc-scraper/pkg/config"
	"github.com/Sriram-PR/doc-scraper/pkg/crawler"
	"github.com/Sriram-PR/doc-scraper/pkg/fetch"
	"github.com/Sriram-PR/doc-scraper/pkg/orchestrate"
	"github.com/Sriram-PR/doc-scraper/pkg/storage"
	"github.com/Sriram-PR/doc-scraper/pkg/utils"
	"github.com/Sriram-PR/doc-scraper/pkg/watch"
)

const version = "1.3.2"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "crawl":
		runCrawl(os.Args[2:], false)
	case "resume":
		runCrawl(os.Args[2:], true)
	case "watch":
		runWatch(os.Args[2:])
	case "validate":
		runValidate(os.Args[2:])
	case "list-sites":
		runListSites(os.Args[2:])
	case "mcp-server":
		runMcpServer(os.Args[2:])
	case "version":
		fmt.Printf("doc-scraper %s\n", version)
	case "-h", "--help", "help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	printUsageTo(os.Stdout)
}

// printUsageTo writes usage information to the provided writer.
func printUsageTo(w io.Writer) {
	fmt.Fprintln(w, `doc-scraper - Documentation site crawler

Usage:
  doc-scraper <command> [options]

Commands:
  crawl       Start a fresh crawl
  resume      Resume an interrupted crawl
  watch       Watch sites and re-crawl on schedule
  validate    Validate configuration file
  list-sites  List available site keys
  mcp-server  Start MCP server for AI tool integration
  version     Show version info

Run 'doc-scraper <command> -h' for command-specific help.`)
}

// loadConfig loads and parses the config file
func loadConfig(path string) (*config.AppConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg config.AppConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	return &cfg, nil
}

// runCrawl handles both crawl and resume subcommands
func runCrawl(args []string, isResume bool) {
	cmdName := "crawl"
	if isResume {
		cmdName = "resume"
	}

	fs := flag.NewFlagSet(cmdName, flag.ExitOnError)
	configFile := fs.String("config", "config.yaml", "Path to config file")
	siteKey := fs.String("site", "", "Site key from config (single site)")
	sites := fs.String("sites", "", "Comma-separated site keys for parallel crawling")
	allSites := fs.Bool("all-sites", false, "Crawl all configured sites in parallel")
	logLevel := fs.String("loglevel", "info", "Log level (debug, info, warn, error, fatal)")
	pprofAddr := fs.String("pprof", "", "pprof address, e.g. localhost:6060 (disabled by default)")
	writeVisitedLog := fs.Bool("write-visited-log", false, "Write visited URLs log on completion")
	incrementalMode := fs.Bool("incremental", false, "Enable incremental crawling (skip unchanged pages)")
	fullMode := fs.Bool("full", false, "Force full crawl (ignore incremental settings)")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: doc-scraper %s [options]\n\nOptions:\n", cmdName)
		fs.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  doc-scraper %s -site pytorch_docs\n", cmdName)
		fmt.Fprintf(os.Stderr, "  doc-scraper %s -sites pytorch_docs,tensorflow_docs\n", cmdName)
		fmt.Fprintf(os.Stderr, "  doc-scraper %s --all-sites\n", cmdName)
	}

	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	// Determine which sites to crawl
	var siteKeys []string

	if *allSites {
		// Will be populated after loading config
		siteKeys = nil // Signal to use all sites
	} else if *sites != "" {
		// Parse comma-separated site keys
		for _, s := range strings.Split(*sites, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				siteKeys = append(siteKeys, s)
			}
		}
	} else if *siteKey != "" {
		siteKeys = []string{*siteKey}
	} else {
		fmt.Fprintln(os.Stderr, "Error: one of -site, -sites, or --all-sites is required")
		fs.Usage()
		os.Exit(1)
	}

	// Check for parallel mode (multiple sites or all sites)
	if *allSites || len(siteKeys) > 1 {
		executeParallelCrawl(*configFile, siteKeys, *allSites, *logLevel, *pprofAddr, isResume, *incrementalMode, *fullMode)
	} else {
		executeCrawl(*configFile, siteKeys[0], *logLevel, *pprofAddr, *writeVisitedLog, isResume, *incrementalMode, *fullMode)
	}
}

// runValidate handles the validate subcommand
func runValidate(args []string) {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	configFile := fs.String("config", "config.yaml", "Path to config file")
	siteKey := fs.String("site", "", "Site key to validate (optional, validates all if empty)")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: doc-scraper validate [options]\n\nOptions:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	exitCode := doValidate(*configFile, *siteKey, os.Stdout, os.Stderr)
	os.Exit(exitCode)
}

// doValidate performs validation and writes output to provided writers.
// Returns exit code (0 = success, 1 = error).
func doValidate(configPath, siteKey string, stdout, stderr io.Writer) int {
	appCfg, err := loadConfig(configPath)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}

	// Validate app config
	warnings, _ := appCfg.Validate()
	for _, w := range warnings {
		fmt.Fprintf(stdout, "WARN: %s\n", w)
	}

	if siteKey != "" {
		// Validate specific site
		siteCfg, ok := appCfg.Sites[siteKey]
		if !ok {
			fmt.Fprintf(stderr, "Error: site '%s' not found in config\n", siteKey)
			return 1
		}
		siteWarnings, err := siteCfg.Validate()
		if err != nil {
			fmt.Fprintf(stderr, "ERROR: [%s] %v\n", siteKey, err)
			return 1
		}
		for _, w := range siteWarnings {
			fmt.Fprintf(stdout, "WARN: [%s] %s\n", siteKey, w)
		}
		fmt.Fprintf(stdout, "OK: Site '%s' configuration is valid\n", siteKey)
	} else {
		// Validate all sites
		hasError := false
		keys := make([]string, 0, len(appCfg.Sites))
		for k := range appCfg.Sites {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		for _, key := range keys {
			siteCfg := appCfg.Sites[key]
			siteWarnings, err := siteCfg.Validate()
			if err != nil {
				fmt.Fprintf(stderr, "ERROR: [%s] %v\n", key, err)
				hasError = true
				continue
			}
			for _, w := range siteWarnings {
				fmt.Fprintf(stdout, "WARN: [%s] %s\n", key, w)
			}
			fmt.Fprintf(stdout, "OK: [%s]\n", key)
		}
		if hasError {
			return 1
		}
	}

	fmt.Fprintln(stdout, "\nConfiguration valid.")
	return 0
}

// runWatch handles the watch subcommand
func runWatch(args []string) {
	fs := flag.NewFlagSet("watch", flag.ExitOnError)
	configFile := fs.String("config", "config.yaml", "Path to config file")
	siteKey := fs.String("site", "", "Site key from config (single site)")
	sites := fs.String("sites", "", "Comma-separated site keys")
	allSites := fs.Bool("all-sites", false, "Watch all configured sites")
	interval := fs.String("interval", "24h", "Crawl interval (e.g., 30m, 1h, 24h, 7d)")
	logLevel := fs.String("loglevel", "info", "Log level (debug, info, warn, error, fatal)")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: doc-scraper watch [options]\n\nOptions:\n")
		fs.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  doc-scraper watch -site pytorch_docs --interval 24h\n")
		fmt.Fprintf(os.Stderr, "  doc-scraper watch -sites pytorch_docs,tensorflow_docs --interval 12h\n")
		fmt.Fprintf(os.Stderr, "  doc-scraper watch --all-sites --interval 6h\n")
	}

	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	// Determine which sites to watch
	var siteKeys []string

	if *allSites {
		siteKeys = nil // Signal to use all sites
	} else if *sites != "" {
		for _, s := range strings.Split(*sites, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				siteKeys = append(siteKeys, s)
			}
		}
	} else if *siteKey != "" {
		siteKeys = []string{*siteKey}
	} else {
		fmt.Fprintln(os.Stderr, "Error: one of -site, -sites, or --all-sites is required")
		fs.Usage()
		os.Exit(1)
	}

	executeWatch(*configFile, siteKeys, *allSites, *interval, *logLevel)
}

// executeWatch runs the watch scheduler
func executeWatch(configFile string, siteKeys []string, allSites bool, intervalStr, logLevelStr string) {
	// --- Logger Setup ---
	log := logrus.New()
	log.SetFormatter(&logrus.TextFormatter{FullTimestamp: true, TimestampFormat: "15:04:05.000"})
	log.SetLevel(logrus.InfoLevel)

	level, err := logrus.ParseLevel(logLevelStr)
	if err != nil {
		log.Warnf("Invalid log level '%s', using default 'info'. Error: %v", logLevelStr, err)
	} else {
		log.SetLevel(level)
	}

	// --- Parse interval ---
	interval, err := watch.ParseInterval(intervalStr)
	if err != nil {
		log.Fatalf("Invalid interval: %v", err)
	}
	log.Infof("Watch interval: %v", interval)

	// --- Load Configuration ---
	log.Infof("Loading configuration from %s", configFile)
	appCfg, err := loadConfig(configFile)
	if err != nil {
		log.Fatalf("Config error: %v", err)
	}

	// --- Validate App Config ---
	appWarnings, _ := appCfg.Validate()
	for _, w := range appWarnings {
		log.Warn(w)
	}

	// Enable incremental mode for watch
	appCfg.EnableIncremental = true
	log.Info("Incremental mode enabled for watch")

	// --- Determine site keys ---
	if allSites {
		siteKeys = orchestrate.GetAllSiteKeys(appCfg)
		log.Infof("All sites mode: found %d sites", len(siteKeys))
	}

	// --- Validate site keys ---
	if err := orchestrate.ValidateSiteKeys(appCfg, siteKeys); err != nil {
		log.Fatalf("Invalid site keys: %v", err)
	}

	// --- Validate each site config ---
	for _, key := range siteKeys {
		siteCfg := appCfg.Sites[key]
		siteWarnings, err := siteCfg.Validate()
		if err != nil {
			log.Fatalf("Site '%s' configuration error: %v", key, err)
		}
		for _, w := range siteWarnings {
			log.Warnf("[%s] %s", key, w)
		}
	}

	// --- Create and run scheduler ---
	logEntry := log.WithField("component", "watch")
	scheduler := watch.NewScheduler(appCfg, siteKeys, interval, logEntry)

	// --- Handle signals for graceful shutdown ---
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigChan
		log.Warnf("Received signal %v, stopping watch...", sig)
		scheduler.Stop()
	}()

	// --- Run scheduler (blocks until stopped) ---
	if err := scheduler.Run(); err != nil {
		log.Fatalf("Watch scheduler error: %v", err)
	}

	log.Info("Watch mode stopped")
}

// runListSites handles the list-sites subcommand
func runListSites(args []string) {
	fs := flag.NewFlagSet("list-sites", flag.ExitOnError)
	configFile := fs.String("config", "config.yaml", "Path to config file")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: doc-scraper list-sites [options]\n\nOptions:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	exitCode := doListSites(*configFile, os.Stdout, os.Stderr)
	os.Exit(exitCode)
}

// doListSites lists sites and writes output to provided writers.
// Returns exit code (0 = success, 1 = error).
func doListSites(configPath string, stdout, stderr io.Writer) int {
	appCfg, err := loadConfig(configPath)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}

	// Sort keys for consistent output
	keys := make([]string, 0, len(appCfg.Sites))
	for k := range appCfg.Sites {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	fmt.Fprintf(stdout, "Sites in %s:\n\n", configPath)
	for _, key := range keys {
		site := appCfg.Sites[key]
		fmt.Fprintf(stdout, "  %s\n", key)
		fmt.Fprintf(stdout, "    Domain: %s\n", site.AllowedDomain)
		fmt.Fprintf(stdout, "    Start URLs: %d\n", len(site.StartURLs))
		if site.AllowedPathPrefix != "" && site.AllowedPathPrefix != "/" {
			fmt.Fprintf(stdout, "    Path Prefix: %s\n", site.AllowedPathPrefix)
		}
		fmt.Fprintln(stdout)
	}
	return 0
}

// setupLogger creates a configured logrus.Logger with the given log level.
func setupLogger(logLevelStr string) *logrus.Logger {
	log := logrus.New()
	log.SetFormatter(&logrus.TextFormatter{FullTimestamp: true, TimestampFormat: "15:04:05.000"})
	log.SetLevel(logrus.InfoLevel)

	level, err := logrus.ParseLevel(logLevelStr)
	if err != nil {
		log.Warnf("Invalid log level '%s', using default 'info'. Error: %v", logLevelStr, err)
	} else {
		log.SetLevel(level)
		log.Infof("Setting log level to: %s", level.String())
	}

	return log
}

// loadAndValidateConfig loads the config file, validates it, and logs warnings.
func loadAndValidateConfig(configFile string, log *logrus.Logger) *config.AppConfig {
	log.Infof("Loading configuration from %s", configFile)
	appCfg, err := loadConfig(configFile)
	if err != nil {
		log.Fatalf("Config error: %v", err)
	}

	appWarnings, _ := appCfg.Validate()
	for _, w := range appWarnings {
		log.Warn(w)
	}

	return appCfg
}

// applyIncrementalOverride applies CLI flag overrides for incremental/full crawl mode.
func applyIncrementalOverride(appCfg *config.AppConfig, incremental, full bool, log *logrus.Logger) {
	if incremental {
		appCfg.EnableIncremental = true
		log.Info("Incremental mode enabled via CLI flag")
	}
	if full {
		appCfg.EnableIncremental = false
		log.Info("Full crawl mode forced via CLI flag")
	}
}

// startPprof starts the pprof HTTP server if addr is non-empty.
func startPprof(addr string, log *logrus.Logger) {
	if addr != "" {
		go func() {
			log.Infof("Starting pprof server at http://%s/debug/pprof/", addr)
			if err := http.ListenAndServe(addr, nil); err != nil {
				log.Errorf("pprof server error: %v", err)
			}
		}()
	}
}

// validateSiteConfigs validates the configuration for each site key and logs warnings.
func validateSiteConfigs(appCfg *config.AppConfig, siteKeys []string, log *logrus.Logger) {
	for _, key := range siteKeys {
		siteCfg := appCfg.Sites[key]
		siteWarnings, err := siteCfg.Validate()
		if err != nil {
			log.Fatalf("Site '%s' configuration error: %v", key, err)
		}
		for _, w := range siteWarnings {
			log.Warnf("[%s] %s", key, w)
		}
	}
}

// executeCrawl contains the main crawl logic
// executeParallelCrawl handles crawling multiple sites in parallel
func executeParallelCrawl(configFile string, siteKeys []string, allSites bool, logLevelStr, pprofAddr string, isResume, incrementalMode, fullMode bool) {
	runtime.SetBlockProfileRate(1000)
	runtime.SetMutexProfileFraction(1000)

	log := setupLogger(logLevelStr)
	appCfg := loadAndValidateConfig(configFile, log)
	applyIncrementalOverride(appCfg, incrementalMode, fullMode, log)

	// --- Determine site keys ---
	if allSites {
		siteKeys = orchestrate.GetAllSiteKeys(appCfg)
		log.Infof("All sites mode: found %d sites", len(siteKeys))
	}

	// --- Validate site keys ---
	if err := orchestrate.ValidateSiteKeys(appCfg, siteKeys); err != nil {
		log.Fatalf("Invalid site keys: %v", err)
	}

	validateSiteConfigs(appCfg, siteKeys, log)
	startPprof(pprofAddr, log)

	// --- Create and run orchestrator ---
	logEntry := log.WithField("component", "parallel_crawl")
	orch := orchestrate.NewOrchestrator(appCfg, siteKeys, isResume, logEntry)

	// --- Handle signals for graceful shutdown ---
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigChan
		log.Warnf("Received signal %v, initiating graceful shutdown...", sig)
		orch.Cancel()
	}()

	// --- Run parallel crawl ---
	results := orch.Run()

	// --- Check for failures ---
	hasFailure := false
	for _, r := range results {
		if !r.Success {
			hasFailure = true
			break
		}
	}

	if hasFailure {
		os.Exit(1)
	}
}

func executeCrawl(configFile, siteKey, logLevelStr, pprofAddr string, writeVisitedLog, isResume, incrementalMode, fullMode bool) {
	runtime.SetBlockProfileRate(1000)
	runtime.SetMutexProfileFraction(1000)

	log := setupLogger(logLevelStr)
	appCfg := loadAndValidateConfig(configFile, log)
	logAppConfig(appCfg, log)

	// --- Get Site Config ---
	siteCfg, ok := appCfg.Sites[siteKey]
	if !ok {
		log.Fatalf("Error: Site key '%s' not found in config file '%s'", siteKey, configFile)
	}

	validateSiteConfigs(appCfg, []string{siteKey}, log)
	log.Infof("Site Config for '%s': Domain: %s, Prefix: %s, ContentSel: '%s', ...",
		siteKey, siteCfg.AllowedDomain, siteCfg.AllowedPathPrefix, siteCfg.ContentSelector)

	applyIncrementalOverride(appCfg, incrementalMode, fullMode, log)

	// Log the effective incremental mode
	if appCfg.EnableIncremental {
		log.Info("Incremental crawling: ENABLED - will skip unchanged pages")
	} else {
		log.Info("Incremental crawling: DISABLED - will process all pages")
	}

	startPprof(pprofAddr, log)

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
	defer cancelCrawl()

	// Channel to listen for OS signals for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Goroutine to handle signals
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Errorf("PANIC in signal handler: %v", r)
			}
		}()
		sig := <-sigChan
		log.Warnf("Received signal: %v. Initiating graceful shutdown...", sig)
		cancelCrawl()

		select {
		case sig = <-sigChan:
			log.Warnf("Received second signal: %v. Forcing exit.", sig)
			os.Exit(1)
		case <-time.After(30 * time.Second):
			log.Warn("Graceful shutdown period exceeded after signal. Forcing exit.")
			os.Exit(1)
		}
	}()
	defer signal.Stop(sigChan)

	// ===========================================================
	// == Initialize Components ==
	// ===========================================================
	log.Info("Initializing components...")
	logEntry := log.WithField("component", "crawl")

	// --- Storage ---
	store, err := storage.NewBadgerStore(crawlCtx, appCfg.StateDir, siteCfg.AllowedDomain, isResume, logEntry)
	if err != nil {
		log.Fatalf("Failed to initialize visited DB: %v", err)
	}
	defer store.Close()

	go store.RunGC(crawlCtx, appCfg.DBGCInterval)

	// --- HTTP Fetching Components ---
	httpClient := fetch.NewClient(appCfg.HTTPClientSettings, logEntry)
	fetcher := fetch.NewFetcher(httpClient, appCfg, logEntry)
	rateLimiter := fetch.NewRateLimiter(appCfg.DefaultDelayPerHost, logEntry)

	// --- Crawler Instance ---
	crawlerInstance, err := crawler.NewCrawler(
		appCfg,
		siteCfg,
		siteKey,
		logEntry,
		store,
		fetcher,
		rateLimiter,
		crawlCtx,
		cancelCrawl,
		isResume,
	)
	if err != nil {
		log.Fatalf("Failed to initialize crawler: %v", err)
	}

	// ===========================================================
	// == Start Crawler Execution ==
	// ===========================================================
	err = crawlerInstance.Run(isResume)

	// ===========================================================
	// == Post-Crawl Actions ==
	// ===========================================================

	// --- Generate Directory Structure File (Only on Success) ---
	if err == nil {
		log.Info("Crawl completed successfully, generating directory structure file...")
		targetDir := filepath.Join(appCfg.OutputBaseDir, utils.SanitizeFilename(siteCfg.AllowedDomain))
		outputFileName := fmt.Sprintf("%s_structure.txt", utils.SanitizeFilename(siteCfg.AllowedDomain))
		outputFilePath := filepath.Join(appCfg.OutputBaseDir, outputFileName)

		if treeErr := utils.GenerateAndSaveTreeStructure(targetDir, outputFilePath, logEntry); treeErr != nil {
			log.Errorf("Failed to generate or save directory structure: %v", treeErr)
		} else {
			log.Infof("Successfully saved directory structure to %s", outputFilePath)
		}
	} else {
		log.Warnf("Skipping directory structure generation due to crawl error: %v", err)
	}

	// --- Final Visited Log File Generation (Optional) ---
	if crawlCtx.Err() != nil {
		log.Warnf("Skipping final visited log due to crawl context error: %v", crawlCtx.Err())
	} else if writeVisitedLog {
		visitedFilename := fmt.Sprintf("%s-visited.txt", utils.SanitizeFilename(siteCfg.AllowedDomain))
		visitedFilePath := filepath.Join(appCfg.OutputBaseDir, visitedFilename)
		if writeErr := store.WriteVisitedLog(visitedFilePath); writeErr != nil {
			log.Errorf("Error writing final visited log: %v", writeErr)
		}
	} else {
		log.Info("Skipping final visited URL log file generation.")
	}

	// --- Exit ---
	if err != nil {
		if errors.Is(err, context.Canceled) {
			log.Warn("Crawl cancelled gracefully.")
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

// logAppConfig logs the effective global configuration
func logAppConfig(appCfg *config.AppConfig, log *logrus.Logger) {
	log.Infof("Global Config: Workers:%d, ImageWorkers:%d, MaxReqs:%d, MaxReqPerHost:%d",
		appCfg.NumWorkers, appCfg.NumImageWorkers, appCfg.MaxRequests, appCfg.MaxRequestsPerHost)
	log.Infof("Global Config: DefaultDelay:%v, StateDir:%s, OutputDir:%s",
		appCfg.DefaultDelayPerHost, appCfg.StateDir, appCfg.OutputBaseDir)
	log.Infof("Global Config Retries: Max:%d, InitialDelay:%v, MaxDelay:%v",
		appCfg.MaxRetries, appCfg.InitialRetryDelay, appCfg.MaxRetryDelay)
	log.Infof("Global Config Timeouts: SemaphoreAcquire:%v, GlobalCrawl:%v, PerPage:%v",
		appCfg.SemaphoreAcquireTimeout, appCfg.GlobalCrawlTimeout, appCfg.PerPageTimeout)
	log.Infof("Global Config Images: Skip:%t, MaxSize:%d bytes",
		appCfg.SkipImages, appCfg.MaxImageSizeBytes)
	log.Infof("Global Config HTTP Client: Timeout:%v, MaxIdle:%d, MaxIdlePerHost:%d, IdleTimeout:%v, TLSTimeout:%v, DialerTimeout:%v",
		appCfg.HTTPClientSettings.Timeout, appCfg.HTTPClientSettings.MaxIdleConns, appCfg.HTTPClientSettings.MaxIdleConnsPerHost,
		appCfg.HTTPClientSettings.IdleConnTimeout, appCfg.HTTPClientSettings.TLSHandshakeTimeout, appCfg.HTTPClientSettings.DialerTimeout)
	log.Infof("Global Config Output Mapping: Enabled Globally:%t, Default Global Filename:'%s'",
		appCfg.EnableOutputMapping, appCfg.OutputMappingFilename)
	log.Infof("Global Config YAML Metadata: Enabled Globally:%t, Default Global Filename:'%s'",
		appCfg.EnableMetadataYAML, appCfg.MetadataYAMLFilename)
}

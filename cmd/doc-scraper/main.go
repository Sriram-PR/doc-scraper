package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"log/slog"

	"gopkg.in/yaml.v3"

	"github.com/Sriram-PR/doc-scraper/pkg/config"
	"github.com/Sriram-PR/doc-scraper/pkg/crawler"
	"github.com/Sriram-PR/doc-scraper/pkg/fetch"
	pkglog "github.com/Sriram-PR/doc-scraper/pkg/log"
	"github.com/Sriram-PR/doc-scraper/pkg/orchestrate"
	"github.com/Sriram-PR/doc-scraper/pkg/storage"
	"github.com/Sriram-PR/doc-scraper/pkg/storage/index"
	"github.com/Sriram-PR/doc-scraper/pkg/taskspec"
	"github.com/Sriram-PR/doc-scraper/pkg/watch"
)

// fatal logs the message at error level and exits the process. Replaces
// logrus's Fatalf; slog has no built-in equivalent because it deliberately
// keeps process-control out of the logging API.
func fatal(log *slog.Logger, format string, args ...interface{}) {
	log.Error(fmt.Sprintf(format, args...))
	os.Exit(1)
}

// setupLogger parses logLevelStr and returns a *slog.Logger on stderr using
// the given format (pkglog.FormatText or pkglog.FormatJSON). Mirrors the
// parse-then-fallback behavior of the original logrus setup: invalid levels
// warn and fall back to info.
func setupLogger(logLevelStr, format string) *slog.Logger {
	level, parseErr := pkglog.ParseLevel(logLevelStr)
	log := pkglog.New(level, format, os.Stderr)
	if parseErr != nil {
		log.Warn(fmt.Sprintf("Invalid log level '%s', using default 'info'. Error: %v", logLevelStr, parseErr))
	}
	return log
}

// logFormatFor returns the pkglog format string for the --json flag.
func logFormatFor(jsonOut bool) string {
	if jsonOut {
		return pkglog.FormatJSON
	}
	return pkglog.FormatText
}

const version = "2.6.1"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "crawl":
		runCrawl(os.Args[2:])
	case "watch":
		runWatch(os.Args[2:])
	case "run":
		runStdinTask(os.Args[2:])
	case "config":
		runConfig(os.Args[2:])
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

func printUsageTo(w io.Writer) {
	fmt.Fprintln(w, `doc-scraper - Documentation site crawler

Usage:
  doc-scraper <command> [options]

Commands:
  crawl       Start a crawl (use --resume to continue an interrupted one)
  watch       Watch sites and re-crawl on schedule
  run         Read a JSON task spec from stdin and execute it (for agents driving doc-scraper as a subprocess)
  config      Inspect configuration: 'config validate' or 'config list'
  mcp-server  Start MCP server for AI tool integration
  version     Show version info

Run 'doc-scraper <command> -h' for command-specific help.`)
}

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

// resolveSiteKeys resolves the effective site keys from the -site/-sites/
// --all-sites flags. ok is false when none of the three were supplied; the
// caller is responsible for printing the usage error and exiting in that case.
func resolveSiteKeys(siteKey, sites string, allSites bool) (siteKeys []string, ok bool) {
	if allSites {
		return nil, true // Signal to use all sites
	}
	if sites != "" {
		for _, s := range strings.Split(sites, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				siteKeys = append(siteKeys, s)
			}
		}
		return siteKeys, true
	}
	if siteKey != "" {
		return []string{siteKey}, true
	}
	return nil, false
}

// runCrawl handles the crawl subcommand. A fresh crawl wipes prior state;
// passing --resume continues an interrupted crawl from existing BadgerDB state.
func runCrawl(args []string) {
	fs := flag.NewFlagSet("crawl", flag.ExitOnError)
	configFile := fs.String("config", "config.yaml", "Path to config file")
	siteKey := fs.String("site", "", "Site key from config (single site)")
	sites := fs.String("sites", "", "Comma-separated site keys for parallel crawling")
	allSites := fs.Bool("all-sites", false, "Crawl all configured sites in parallel")
	resume := fs.Bool("resume", false, "Resume an interrupted crawl from existing state")
	logLevel := fs.String("loglevel", "info", "Log level (debug, info, warn, error)")
	jsonLogs := fs.Bool("json", false, "Emit logs as JSON (one record per line) instead of slog's text format")
	pprofAddr := fs.String("pprof", "", "pprof address, e.g. localhost:6060 (disabled by default)")
	incrementalMode := fs.Bool("incremental", false, "Enable incremental crawling (skip unchanged pages)")
	fullMode := fs.Bool("full", false, "Force full crawl (ignore incremental settings)")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: doc-scraper crawl [options]\n\nOptions:\n")
		fs.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  doc-scraper crawl -site pytorch_docs\n")
		fmt.Fprintf(os.Stderr, "  doc-scraper crawl -site pytorch_docs --resume\n")
		fmt.Fprintf(os.Stderr, "  doc-scraper crawl -sites pytorch_docs,tensorflow_docs\n")
		fmt.Fprintf(os.Stderr, "  doc-scraper crawl --all-sites\n")
	}

	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	if *incrementalMode && *fullMode {
		fmt.Fprintln(os.Stderr, "Error: --incremental and --full are mutually exclusive")
		os.Exit(1)
	}

	// --incremental implies --resume; an incremental crawl is meaningless if
	// state gets wiped first.
	isResume := *resume || *incrementalMode

	siteKeys, ok := resolveSiteKeys(*siteKey, *sites, *allSites)
	if !ok {
		fmt.Fprintln(os.Stderr, "Error: one of -site, -sites, or --all-sites is required")
		fs.Usage()
		os.Exit(1)
	}

	logFormat := logFormatFor(*jsonLogs)
	if *allSites || len(siteKeys) > 1 {
		executeParallelCrawl(*configFile, siteKeys, *allSites, *logLevel, logFormat, *pprofAddr, isResume, *incrementalMode, *fullMode)
	} else {
		executeCrawl(*configFile, siteKeys[0], *logLevel, logFormat, *pprofAddr, isResume, *incrementalMode, *fullMode)
	}
}

// runConfig dispatches the config subcommand (validate | list).
func runConfig(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Error: config requires an action: validate | list")
		fmt.Fprintln(os.Stderr, "\nUsage:")
		fmt.Fprintln(os.Stderr, "  doc-scraper config validate [-config <path>] [-site <key>]")
		fmt.Fprintln(os.Stderr, "  doc-scraper config list [-config <path>]")
		os.Exit(1)
	}

	action, rest := args[0], args[1:]
	switch action {
	case "validate":
		fs := flag.NewFlagSet("config validate", flag.ExitOnError)
		configFile := fs.String("config", "config.yaml", "Path to config file")
		siteKey := fs.String("site", "", "Site key to validate (optional, validates all if empty)")
		jsonOut := fs.Bool("json", false, "Emit a single JSON object instead of human-readable text")
		fs.Usage = func() {
			fmt.Fprintf(os.Stderr, "Usage: doc-scraper config validate [options]\n\nOptions:\n")
			fs.PrintDefaults()
		}
		if err := fs.Parse(rest); err != nil {
			os.Exit(1)
		}
		os.Exit(doValidate(*configFile, *siteKey, *jsonOut, os.Stdout, os.Stderr))
	case "list":
		fs := flag.NewFlagSet("config list", flag.ExitOnError)
		configFile := fs.String("config", "config.yaml", "Path to config file")
		jsonOut := fs.Bool("json", false, "Emit a single JSON object instead of human-readable text")
		fs.Usage = func() {
			fmt.Fprintf(os.Stderr, "Usage: doc-scraper config list [options]\n\nOptions:\n")
			fs.PrintDefaults()
		}
		if err := fs.Parse(rest); err != nil {
			os.Exit(1)
		}
		os.Exit(doListSites(*configFile, *jsonOut, os.Stdout, os.Stderr))
	default:
		fmt.Fprintf(os.Stderr, "Unknown config action: %s (expected: validate | list)\n", action)
		os.Exit(1)
	}
}

// doValidate returns exit code 0 on success, 1 on error. jsonOut writes a
// single JSON object to stdout and leaves stderr empty on the success path.
func doValidate(configPath, siteKey string, jsonOut bool, stdout, stderr io.Writer) int {
	appCfg, err := loadConfig(configPath)
	if err != nil {
		if jsonOut {
			emitValidateJSON(stdout, configPath, false, nil, []string{err.Error()}, nil)
			return 1
		}
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}

	globalWarnings, _ := appCfg.Validate()

	type siteResult struct {
		Key      string   `json:"key"`
		Valid    bool     `json:"valid"`
		Warnings []string `json:"warnings,omitempty"`
		Error    string   `json:"error,omitempty"`
	}

	var results []siteResult
	hasError := false

	if siteKey != "" {
		siteCfg, ok := appCfg.Sites[siteKey]
		if !ok {
			msg := fmt.Sprintf("site '%s' not found in config", siteKey)
			if jsonOut {
				emitValidateJSON(stdout, configPath, false, globalWarnings, []string{msg}, nil)
				return 1
			}
			fmt.Fprintf(stderr, "Error: %s\n", msg)
			return 1
		}
		siteWarnings, err := siteCfg.Validate()
		r := siteResult{Key: siteKey, Valid: err == nil, Warnings: siteWarnings}
		if err != nil {
			r.Error = err.Error()
			hasError = true
		}
		results = append(results, r)
	} else {
		keys := make([]string, 0, len(appCfg.Sites))
		for k := range appCfg.Sites {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, key := range keys {
			siteCfg := appCfg.Sites[key]
			siteWarnings, err := siteCfg.Validate()
			r := siteResult{Key: key, Valid: err == nil, Warnings: siteWarnings}
			if err != nil {
				r.Error = err.Error()
				hasError = true
			}
			results = append(results, r)
		}
	}

	if jsonOut {
		anyResults := make([]map[string]any, 0, len(results))
		for _, r := range results {
			entry := map[string]any{"key": r.Key, "valid": r.Valid}
			if len(r.Warnings) > 0 {
				entry["warnings"] = r.Warnings
			}
			if r.Error != "" {
				entry["error"] = r.Error
			}
			anyResults = append(anyResults, entry)
		}
		emitValidateJSON(stdout, configPath, !hasError, globalWarnings, nil, anyResults)
		if hasError {
			return 1
		}
		return 0
	}

	// Text mode (legacy human output).
	for _, w := range globalWarnings {
		fmt.Fprintf(stdout, "WARN: %s\n", w)
	}
	for _, r := range results {
		if r.Error != "" {
			fmt.Fprintf(stderr, "ERROR: [%s] %s\n", r.Key, r.Error)
			continue
		}
		for _, w := range r.Warnings {
			fmt.Fprintf(stdout, "WARN: [%s] %s\n", r.Key, w)
		}
		if siteKey != "" {
			fmt.Fprintf(stdout, "OK: Site '%s' configuration is valid\n", r.Key)
		} else {
			fmt.Fprintf(stdout, "OK: [%s]\n", r.Key)
		}
	}
	if hasError {
		return 1
	}
	fmt.Fprintln(stdout, "\nConfiguration valid.")
	return 0
}

// emitValidateJSON writes the structured validate result. errors holds top-level
// failures (config load failures, unknown site key) separately from per-site
// errors carried inside results.
func emitValidateJSON(w io.Writer, configPath string, valid bool, globalWarnings, errors []string, results []map[string]any) {
	payload := map[string]any{
		"config_path": configPath,
		"valid":       valid,
		"site_count":  len(results),
	}
	if len(globalWarnings) > 0 {
		payload["global_warnings"] = globalWarnings
	}
	if len(errors) > 0 {
		payload["errors"] = errors
	}
	if results != nil {
		payload["sites"] = results
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		fmt.Fprintf(w, "{\"valid\": false, \"errors\": [\"failed to marshal result: %s\"]}\n", err.Error())
		return
	}
	fmt.Fprintln(w, string(b))
}

// runStdinTask reads a JSON TaskSpec from stdin and dispatches to the same
// execute* functions the flag-driven subcommands use. Only -h is accepted as
// an argument; all task parameters come from the JSON payload to keep the
// contract unambiguous for orchestration agents.
func runStdinTask(args []string) {
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "help" {
			printRunUsage(os.Stdout)
			return
		}
	}
	if len(args) > 0 {
		fmt.Fprintf(os.Stderr, "Error: run takes no flags; pass the task spec on stdin\n\n")
		printRunUsage(os.Stderr)
		os.Exit(1)
	}

	spec, err := taskspec.Parse(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if err := spec.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	dispatchTaskSpec(spec)
}

// dispatchTaskSpec routes a validated TaskSpec to the same execute* entry
// points the flag-driven subcommands use.
func dispatchTaskSpec(spec *taskspec.TaskSpec) {
	logFormat := logFormatFor(spec.JSONLogs)
	siteKeys := spec.SiteKeys()
	switch spec.Command {
	case taskspec.CommandCrawl:
		if spec.AllSites || len(siteKeys) > 1 {
			executeParallelCrawl(spec.Config, siteKeys, spec.AllSites, spec.Loglevel, logFormat, spec.Pprof, spec.Resume || spec.Incremental, spec.Incremental, spec.Full)
		} else {
			executeCrawl(spec.Config, siteKeys[0], spec.Loglevel, logFormat, spec.Pprof, spec.Resume || spec.Incremental, spec.Incremental, spec.Full)
		}
	case taskspec.CommandWatch:
		executeWatch(spec.Config, siteKeys, spec.AllSites, spec.Interval, spec.Loglevel, logFormat)
	}
}

func printRunUsage(w io.Writer) {
	fmt.Fprintln(w, `Usage: doc-scraper run

Reads a single JSON object from stdin describing a task and executes it.
Designed for orchestration agents that prefer building a JSON payload over
constructing shell arguments.

JSON schema:
  {
    "command":     "crawl" | "watch",   // required
    "config":      "config.yaml",        // optional, defaults to config.yaml
    "site":        "site_key",           // OR
    "sites":       ["a", "b"],           // OR
    "all_sites":   true,                 // exactly one of site|sites|all_sites
    "resume":      false,                // crawl only
    "incremental": false,                // crawl only (implies resume)
    "full":        false,                // crawl only (mutually exclusive with incremental)
    "interval":    "24h",                // watch only, defaults to 24h
    "loglevel":    "info",               // defaults to info
    "json_logs":   false,                // emit slog records as JSON on stderr
    "pprof":       ""                    // crawl only, e.g. localhost:6060
  }

Examples:
  echo '{"command":"crawl","site":"rust_cli_small"}' | doc-scraper run
  echo '{"command":"crawl","all_sites":true,"incremental":true,"json_logs":true}' | doc-scraper run
  echo '{"command":"watch","sites":["a","b"],"interval":"6h"}' | doc-scraper run

Unknown JSON fields are rejected to surface typos. Logs are written to stderr;
the process exit code matches the equivalent flag-driven subcommand.`)
}

func runWatch(args []string) {
	fs := flag.NewFlagSet("watch", flag.ExitOnError)
	configFile := fs.String("config", "config.yaml", "Path to config file")
	siteKey := fs.String("site", "", "Site key from config (single site)")
	sites := fs.String("sites", "", "Comma-separated site keys")
	allSites := fs.Bool("all-sites", false, "Watch all configured sites")
	interval := fs.String("interval", "24h", "Crawl interval (e.g., 30m, 1h, 24h, 7d)")
	logLevel := fs.String("loglevel", "info", "Log level (debug, info, warn, error)")
	jsonLogs := fs.Bool("json", false, "Emit logs as JSON (one record per line) instead of slog's text format")

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

	siteKeys, ok := resolveSiteKeys(*siteKey, *sites, *allSites)
	if !ok {
		fmt.Fprintln(os.Stderr, "Error: one of -site, -sites, or --all-sites is required")
		fs.Usage()
		os.Exit(1)
	}

	executeWatch(*configFile, siteKeys, *allSites, *interval, *logLevel, logFormatFor(*jsonLogs))
}

func executeWatch(configFile string, siteKeys []string, allSites bool, intervalStr, logLevelStr, logFormat string) {
	log := setupLogger(logLevelStr, logFormat)

	interval, err := watch.ParseInterval(intervalStr)
	if err != nil {
		fatal(log, "Invalid interval: %v", err)
	}
	log.Info(fmt.Sprintf("Watch interval: %v", interval))

	log.Info(fmt.Sprintf("Loading configuration from %s", configFile))
	appCfg, err := loadConfig(configFile)
	if err != nil {
		fatal(log, "Config error: %v", err)
	}

	appWarnings, _ := appCfg.Validate()
	for _, w := range appWarnings {
		log.Warn(w)
	}

	appCfg.EnableIncremental = true
	log.Info("Incremental mode enabled for watch")

	if allSites {
		siteKeys = config.GetAllSiteKeys(appCfg)
		log.Info(fmt.Sprintf("All sites mode: found %d sites", len(siteKeys)))
	}

	if err := config.ValidateSiteKeys(appCfg, siteKeys); err != nil {
		fatal(log, "Invalid site keys: %v", err)
	}

	for _, key := range siteKeys {
		siteCfg := appCfg.Sites[key]
		siteWarnings, err := siteCfg.Validate()
		if err != nil {
			fatal(log, "Site '%s' configuration error: %v", key, err)
		}
		for _, w := range siteWarnings {
			log.Warn(fmt.Sprintf("[%s] %s", key, w))
		}
	}

	logEntry := log.With("component", "watch")
	idx, err := index.OpenAt(appCfg.StateDir, appCfg.CrawlHistoryRetention, logEntry)
	if err != nil {
		fatal(log, "Failed to open crawl-history index: %v", err)
	}
	defer func() { _ = idx.Close() }()

	scheduler := watch.NewScheduler(appCfg, siteKeys, interval, logEntry).WithIndex(idx)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigChan
		log.Warn(fmt.Sprintf("Received signal %v, stopping watch...", sig))
		scheduler.Stop()
	}()

	if err := scheduler.Run(); err != nil {
		fatal(log, "Watch scheduler error: %v", err)
	}

	log.Info("Watch mode stopped")
}

// doListSites lists sites and writes output to provided writers.
// Returns exit code (0 = success, 1 = error).
func doListSites(configPath string, jsonOut bool, stdout, stderr io.Writer) int {
	appCfg, err := loadConfig(configPath)
	if err != nil {
		if jsonOut {
			b, _ := json.MarshalIndent(map[string]any{
				"config_path": configPath,
				"sites":       []any{},
				"count":       0,
				"errors":      []string{err.Error()},
			}, "", "  ")
			fmt.Fprintln(stdout, string(b))
			return 1
		}
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}

	keys := make([]string, 0, len(appCfg.Sites))
	for k := range appCfg.Sites {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	if jsonOut {
		sites := make([]map[string]any, 0, len(keys))
		for _, key := range keys {
			site := appCfg.Sites[key]
			entry := map[string]any{
				"key":              key,
				"domain":           site.AllowedDomain,
				"start_urls_count": len(site.StartURLs),
			}
			if site.AllowedPathPrefix != "" {
				entry["path_prefix"] = site.AllowedPathPrefix
			}
			sites = append(sites, entry)
		}
		payload := map[string]any{
			"config_path": configPath,
			"sites":       sites,
			"count":       len(sites),
		}
		b, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			fmt.Fprintf(stdout, "{\"count\": 0, \"errors\": [\"failed to marshal result: %s\"]}\n", err.Error())
			return 1
		}
		fmt.Fprintln(stdout, string(b))
		return 0
	}

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

// loadAndValidateConfig loads the config file, validates it, and logs warnings.
func loadAndValidateConfig(configFile string, log *slog.Logger) *config.AppConfig {
	log.Info(fmt.Sprintf("Loading configuration from %s", configFile))
	appCfg, err := loadConfig(configFile)
	if err != nil {
		fatal(log, "Config error: %v", err)
	}

	appWarnings, _ := appCfg.Validate()
	for _, w := range appWarnings {
		log.Warn(w)
	}

	return appCfg
}

// applyIncrementalOverride applies CLI flag overrides for incremental mode.
// -full takes precedence, forcing EnableIncremental=false even over -incremental.
func applyIncrementalOverride(appCfg *config.AppConfig, incremental, full bool, log *slog.Logger) {
	if incremental {
		appCfg.EnableIncremental = true
		log.Info("Incremental mode enabled via CLI flag")
	}
	if full {
		appCfg.EnableIncremental = false
		log.Info("Full crawl mode forced via CLI flag")
	}
}

// validateSiteConfigs validates the configuration for each site key and logs warnings.
func validateSiteConfigs(appCfg *config.AppConfig, siteKeys []string, log *slog.Logger) {
	for _, key := range siteKeys {
		siteCfg := appCfg.Sites[key]
		siteWarnings, err := siteCfg.Validate()
		if err != nil {
			fatal(log, "Site '%s' configuration error: %v", key, err)
		}
		for _, w := range siteWarnings {
			log.Warn(fmt.Sprintf("[%s] %s", key, w))
		}
	}
}

func executeParallelCrawl(configFile string, siteKeys []string, allSites bool, logLevelStr, logFormat, pprofAddr string, isResume, incrementalMode, fullMode bool) {
	runtime.SetBlockProfileRate(1000)
	runtime.SetMutexProfileFraction(1000)

	log := setupLogger(logLevelStr, logFormat)
	appCfg := loadAndValidateConfig(configFile, log)
	applyIncrementalOverride(appCfg, incrementalMode, fullMode, log)

	if allSites {
		siteKeys = config.GetAllSiteKeys(appCfg)
		log.Info(fmt.Sprintf("All sites mode: found %d sites", len(siteKeys)))
	}

	if err := config.ValidateSiteKeys(appCfg, siteKeys); err != nil {
		fatal(log, "Invalid site keys: %v", err)
	}

	validateSiteConfigs(appCfg, siteKeys, log)
	startPprof(pprofAddr, log)

	logEntry := log.With("component", "parallel_crawl")
	idx, err := index.OpenAt(appCfg.StateDir, appCfg.CrawlHistoryRetention, logEntry)
	if err != nil {
		fatal(log, "Failed to open crawl-history index: %v", err)
	}
	defer func() { _ = idx.Close() }()

	orch := orchestrate.NewOrchestrator(context.Background(), appCfg, siteKeys, isResume, logEntry).WithIndex(idx)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigChan
		log.Warn(fmt.Sprintf("Received signal %v, initiating graceful shutdown...", sig))
		orch.Cancel()
	}()

	results := orch.Run()

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

func executeCrawl(configFile, siteKey, logLevelStr, logFormat, pprofAddr string, isResume, incrementalMode, fullMode bool) {
	runtime.SetBlockProfileRate(1000)
	runtime.SetMutexProfileFraction(1000)

	log := setupLogger(logLevelStr, logFormat)
	appCfg := loadAndValidateConfig(configFile, log)
	logAppConfig(appCfg, log)

	siteCfg, ok := appCfg.Sites[siteKey]
	if !ok {
		fatal(log, "Error: Site key '%s' not found in config file '%s'", siteKey, configFile)
	}

	validateSiteConfigs(appCfg, []string{siteKey}, log)
	log.Info(fmt.Sprintf("Site Config for '%s': Domain: %s, Prefix: %s, ContentSel: '%s', ...",
		siteKey, siteCfg.AllowedDomain, siteCfg.AllowedPathPrefix, siteCfg.ContentSelector))

	applyIncrementalOverride(appCfg, incrementalMode, fullMode, log)

	if appCfg.EnableIncremental {
		log.Info("Incremental crawling: ENABLED - will skip unchanged pages")
	} else {
		log.Info("Incremental crawling: DISABLED - will process all pages")
	}

	startPprof(pprofAddr, log)

	var crawlCtx context.Context
	var cancelCrawl context.CancelFunc

	if appCfg.GlobalCrawlTimeout > 0 {
		log.Info(fmt.Sprintf("Setting global crawl timeout: %v", appCfg.GlobalCrawlTimeout))
		crawlCtx, cancelCrawl = context.WithTimeout(context.Background(), appCfg.GlobalCrawlTimeout)
	} else {
		log.Info("No global crawl timeout set.")
		crawlCtx, cancelCrawl = context.WithCancel(context.Background())
	}
	defer cancelCrawl()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Error(fmt.Sprintf("PANIC in signal handler: %v", r))
			}
		}()
		sig := <-sigChan
		log.Warn(fmt.Sprintf("Received signal: %v. Initiating graceful shutdown...", sig))
		cancelCrawl()

		select {
		case sig = <-sigChan:
			log.Warn(fmt.Sprintf("Received second signal: %v. Forcing exit.", sig))
			os.Exit(1)
		case <-time.After(30 * time.Second):
			log.Warn("Graceful shutdown period exceeded after signal. Forcing exit.")
			os.Exit(1)
		}
	}()
	defer signal.Stop(sigChan)

	log.Info("Initializing components...")
	logEntry := log.With("component", "crawl")

	store, err := storage.NewBadgerStore(crawlCtx, appCfg.StateDir, siteCfg.AllowedDomain, isResume, logEntry)
	if err != nil {
		fatal(log, "Failed to initialize visited DB: %v", err)
	}
	defer store.Close()

	go store.RunGC(crawlCtx, 0) // 0 = use the store's built-in default (10m)

	httpClient := fetch.NewClient(appCfg.HTTPClientSettings, logEntry)
	fetcher := fetch.NewFetcher(httpClient, appCfg, logEntry)
	rateLimiter := fetch.NewRateLimiter(appCfg.DefaultDelayPerHost, logEntry)

	idx, err := index.OpenAt(appCfg.StateDir, appCfg.CrawlHistoryRetention, logEntry)
	if err != nil {
		fatal(log, "Failed to open crawl-history index: %v", err)
	}
	defer func() { _ = idx.Close() }()

	crawlerInstance, err := crawler.NewCrawlerWithOptions(
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
		&crawler.CrawlerOptions{Index: idx},
	)
	if err != nil {
		fatal(log, "Failed to initialize crawler: %v", err)
	}

	err = crawlerInstance.Run(isResume)

	if err != nil {
		if errors.Is(err, context.Canceled) {
			log.Warn("Crawl cancelled gracefully.")
			os.Exit(0)
		} else if errors.Is(err, context.DeadlineExceeded) {
			log.Error("Crawl timed out (global timeout).")
			os.Exit(1)
		} else {
			log.Error(fmt.Sprintf("Crawl finished with error: %v", err))
			os.Exit(1)
		}
	}

	log.Info("Crawl completed successfully.")
	os.Exit(0)
}

// logAppConfig logs the effective global configuration
func logAppConfig(appCfg *config.AppConfig, log *slog.Logger) {
	log.Info(fmt.Sprintf("Global Config: Workers:%d, ImageWorkers:%d, MaxReqs:%d, MaxReqPerHost:%d",
		appCfg.NumWorkers, appCfg.NumImageWorkers, appCfg.MaxRequests, appCfg.MaxRequestsPerHost))
	log.Info(fmt.Sprintf("Global Config: DefaultDelay:%v, StateDir:%s, OutputDir:%s",
		appCfg.DefaultDelayPerHost, appCfg.StateDir, appCfg.OutputBaseDir))
	log.Info(fmt.Sprintf("Global Config Retries: Max:%d, InitialDelay:%v, MaxDelay:%v",
		appCfg.MaxRetries, appCfg.InitialRetryDelay, appCfg.MaxRetryDelay))
	log.Info(fmt.Sprintf("Global Config Timeouts: GlobalCrawl:%v, PerPage:%v",
		appCfg.GlobalCrawlTimeout, appCfg.PerPageTimeout))
	globalSkipImages := true // Default: skip image downloads (text-first)
	if appCfg.SkipImages != nil {
		globalSkipImages = *appCfg.SkipImages
	}
	log.Info(fmt.Sprintf("Global Config Images: Skip(default):%t, MaxSize:%d bytes",
		globalSkipImages, appCfg.MaxImageSizeBytes))
	log.Info(fmt.Sprintf("Global Config HTTP Client: Timeout:%v, MaxIdlePerHost:%d, AllowPrivateNetworks:%t",
		appCfg.HTTPClientSettings.Timeout, appCfg.HTTPClientSettings.MaxIdleConnsPerHost, appCfg.HTTPClientSettings.AllowPrivateNetworks))
	log.Info(fmt.Sprintf("Global Config JSONL Output: Enabled Globally:%t, Default Global Filename:'%s'",
		appCfg.EnableJSONLOutput, appCfg.JSONLOutputFilename))
}

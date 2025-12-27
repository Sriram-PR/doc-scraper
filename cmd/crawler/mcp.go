package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/sirupsen/logrus"

	"doc-scraper/pkg/mcp"
)

// runMcpServer handles the mcp-server subcommand
func runMcpServer(args []string) {
	fs := flag.NewFlagSet("mcp-server", flag.ExitOnError)
	configFile := fs.String("config", "config.yaml", "Path to config file")
	transport := fs.String("transport", "stdio", "Transport type (stdio, sse)")
	port := fs.Int("port", 8080, "HTTP port (for sse transport)")
	logLevel := fs.String("loglevel", "info", "Log level (debug, info, warn, error)")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `Usage: doc-scraper mcp-server [options]

Start an MCP (Model Context Protocol) server for AI tool integration.

Options:
`)
		fs.PrintDefaults()
		fmt.Fprintf(os.Stderr, `
Examples:
  # Start with stdio transport (for Claude Desktop)
  doc-scraper mcp-server -config config.yaml

  # Start with SSE transport on port 8080
  doc-scraper mcp-server -config config.yaml -transport sse -port 8080

Available MCP Tools:
  list_sites      List all configured sites
  get_page        Fetch a single URL as markdown
  crawl_site      Start a background crawl for a site
  search_crawled  Search previously crawled content
`)
	}

	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	exitCode := doMcpServer(*configFile, *transport, *port, *logLevel, os.Stdout, os.Stderr)
	os.Exit(exitCode)
}

// doMcpServer is the testable implementation of the MCP server
func doMcpServer(configPath, transport string, port int, logLevel string, stdout, stderr io.Writer) int {
	// Setup logger
	log := logrus.New()
	log.SetOutput(stderr) // MCP protocol uses stdout, logs go to stderr
	level, err := logrus.ParseLevel(logLevel)
	if err != nil {
		fmt.Fprintf(stderr, "Invalid log level: %s\n", logLevel)
		return 1
	}
	log.SetLevel(level)
	log.SetFormatter(&logrus.TextFormatter{
		FullTimestamp:   true,
		TimestampFormat: "15:04:05",
	})

	// Load config
	appCfg, err := loadConfig(configPath)
	if err != nil {
		fmt.Fprintf(stderr, "Error loading config: %v\n", err)
		return 1
	}

	// Create and run MCP server
	serverCfg := &mcp.ServerConfig{
		AppConfig:  appCfg,
		ConfigPath: configPath,
		Transport:  transport,
		Port:       port,
		Logger:     log,
	}

	server, err := mcp.NewServer(serverCfg)
	if err != nil {
		fmt.Fprintf(stderr, "Error creating MCP server: %v\n", err)
		return 1
	}

	log.Infof("Starting MCP server (transport: %s)", transport)

	if err := server.Run(); err != nil {
		fmt.Fprintf(stderr, "MCP server error: %v\n", err)
		return 1
	}

	return 0
}

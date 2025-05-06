# 🕸️ LLM Documentation Scraper (`doc-scraper`)

[![Go Version](https://img.shields.io/github/go-mod/go-version/Sriram-PR/doc-scraper)](https://golang.org/)

> A configurable, concurrent, and resumable web crawler written in Go. Specifically designed to scrape technical documentation websites, extract core content, convert it cleanly to Markdown format suitable for ingestion by Large Language Models (LLMs), and save the results locally.

## 📖 Overview

This project provides a powerful command-line tool to crawl documentation sites based on settings defined in a `config.yaml` file. It navigates the site structure, extracts content from specified HTML sections using CSS selectors, and converts it into clean Markdown files. 

### Why Use This Tool?

- **Built for LLM Training & RAG Systems** - Creates clean, consistent Markdown optimized for ingestion
- **Preserves Documentation Structure** - Maintains the original site hierarchy for context preservation
- **Production-Ready Features** - Offers resumable crawls, rate limiting, and graceful error handling
- **High Performance** - Uses Go's concurrency model for efficient parallel processing

## 🎯 Goal: Preparing Documentation for LLMs

The main objective of this tool is to automate the often tedious process of gathering and cleaning web-based documentation for use with Large Language Models. By converting structured web content into clean Markdown, it aims to provide a dataset that is:

* **Text-Focused:** Prioritizes the textual content extracted via CSS selectors
* **Structured:** Maintains the directory hierarchy of the original documentation site, preserving context
* **Cleaned:** Converts HTML to Markdown, removing web-specific markup and clutter
* **Locally Accessible:** Provides the content as local files for easier processing and pipeline integration

## ✨ Key Features

| Feature | Description |
|---------|-------------|
| **Configurable Crawling** | Uses YAML for global and site-specific settings |
| **Scope Control** | Limits crawling by domain, path prefix, and disallowed path patterns (regex) |
| **Content Extraction** | Extracts main content using CSS selectors |
| **HTML-to-Markdown** | Converts extracted HTML to clean Markdown |
| **Image Handling** | Optional downloading and local rewriting of image links with domain and size filtering |
| **Link Rewriting** | Rewrites internal links to relative paths for local structure |
| **Concurrency** | Configurable worker pools and semaphore-based request limits (global and per-host) |
| **Rate Limiting** | Configurable per-host delays with jitter |
| **Robots.txt & Sitemaps** | Respects `robots.txt` and processes discovered sitemaps |
| **State Persistence** | Uses BadgerDB for state; supports resuming crawls via `-resume` flag |
| **Graceful Shutdown** | Handles `SIGINT`/`SIGTERM` with proper cleanup |
| **HTTP Retries** | Exponential backoff with jitter for transient errors |
| **Observability** | Structured logging (`logrus`) and optional `pprof` endpoint |
| **Modular Code** | Organized into packages for clarity and maintainability |

## 🚀 Getting Started

### Prerequisites

* **Go:** Version 1.21 or later
* **Git:** For cloning the repository
* **Disk Space:** Sufficient for storing crawled content and state database

### Installation

1. **Clone the repository:**
   ```bash
   git clone https://github.com/Sriram-PR/doc-scraper.git
   cd doc-scraper
   ```

2. **Install Dependencies:**
   ```bash
   go mod tidy
   ```

3. **Build the Binary:**
   ```bash
   go build -o crawler.exe ./cmd/crawler/...
   ```
   This creates an executable file named `crawler` in the project root directory.

### Quick Start

1. Create a basic `config.yaml` file (see [Configuration](#-configuration-configyaml) section)
2. Run the crawler:
   ```bash
   ./crawler.exe -site your_site_key -loglevel info
   ```
3. Find your crawled documentation in the `./crawled_docs/` directory

## ⚙️ Configuration (`config.yaml`)

A `config.yaml` file is **required** to run the crawler. Create this file in the project root or specify its path using the `-config` flag.

### Key Settings for LLM Use

When configuring for LLM documentation processing, pay special attention to these settings:

- `sites.<your_site_key>.content_selector`: Define precisely to capture only relevant text
- `sites.<your_site_key>.allowed_domain` / `allowed_path_prefix`: Define scope accurately
- `skip_images`: Set to `true` globally or per-site if images aren't needed for the LLM
- Adjust concurrency/delay settings based on the target site and your resources

### Example Configuration

```yaml
# Global settings (applied if not overridden by site)
default_delay_per_host: 500ms
num_workers: 8
num_image_workers: 8
max_requests: 48
max_requests_per_host: 4
output_base_dir: "./crawled_docs"
state_dir: "./crawler_state"
max_retries: 4
initial_retry_delay: 1s
max_retry_delay: 30s
semaphore_acquire_timeout: 30s
global_crawl_timeout: 0s
skip_images: false # Set to true to skip images globally
max_image_size_bytes: 10485760 # 10 MiB

# HTTP Client Settings
http_client_settings:
  timeout: 45s
  max_idle_conns_per_host: 6

# Site-specific configurations
sites:
  # Key used with -site flag
  pytorch_docs:
    start_urls:
      - "https://pytorch.org/docs/stable/"
    allowed_domain: "pytorch.org"
    allowed_path_prefix: "/docs/stable/"
    content_selector: "article.pytorch-article .body"
    max_depth: 0 # 0 for unlimited depth
    skip_images: false
    disallowed_path_patterns:
      - "/docs/stable/.*/_modules/.*"
      - "/docs/stable/.*\.html#.*"

  tensorflow_docs:
    start_urls:
      - "https://www.tensorflow.org/guide"
      - "https://www.tensorflow.org/tutorials"
    allowed_domain: "www.tensorflow.org"
    allowed_path_prefix: "/"
    content_selector: ".devsite-article-body"
    max_depth: 0
    delay_per_host: 1s  # Site-specific override
    disallowed_path_patterns:
      - "/install/.*"
      - "/js/.*"
```

### Full Configuration Options

| Option | Type | Description | Default |
|--------|------|-------------|---------|
| `default_delay_per_host` | Duration | Time to wait between requests to the same host | `500ms` |
| `num_workers` | Integer | Number of concurrent crawl workers | `8` |
| `num_image_workers` | Integer | Number of concurrent image download workers | `8` |
| `max_requests` | Integer | Maximum concurrent requests (global) | `48` |
| `max_requests_per_host` | Integer | Maximum concurrent requests per host | `4` |
| `output_base_dir` | String | Base directory for crawled content | `"./crawled_docs"` |
| `state_dir` | String | Directory for BadgerDB state data | `"./crawler_state"` |
| `max_retries` | Integer | Maximum retry attempts for HTTP requests | `4` |
| `initial_retry_delay` | Duration | Initial delay for retry backoff | `1s` |
| `max_retry_delay` | Duration | Maximum delay for retry backoff | `30s` |
| `skip_images` | Boolean | Whether to skip downloading images | `false` |
| `max_image_size_bytes` | Integer | Maximum allowed image size | `10485760` (10 MiB) |
| `http_client_settings` | Object | HTTP client configuration | *(see below)* |
| `sites` | Map | Site-specific configurations | *(required)* |

**HTTP Client Settings:**
- `timeout`: Request timeout (Default: `30s`)
- `max_idle_conns_per_host`: Maximum idle connections per host (Default: `4`)

**Site-Specific Configuration Options:**
- `start_urls`: Array of starting URLs for crawling (Required)
- `allowed_domain`: Restrict crawling to this domain (Required)
- `allowed_path_prefix`: Further restrict crawling to URLs with this prefix (Required)
- `content_selector`: CSS selector for main content extraction (Required)
- `max_depth`: Maximum crawl depth from start URLs (0 = unlimited)
- `delay_per_host`: Override global delay setting for this site
- `disallowed_path_patterns`: Array of regex patterns for URLs to skip
- `skip_images`: Override global image setting for this site
- `allowed_image_domains`: Array of domains from which to download images

## 🛠️ Usage

Execute the compiled binary from the project root directory:

```bash
./crawler -config <path_to_config.yaml> -site <site_key> [flags...]
```

### Command-Line Flags

| Flag | Description | Default |
|------|-------------|---------|
| `-config <path>` | Path to config file | `config.yaml` |
| `-site <key>` | **Required.** Key identifying the site config entry | - |
| `-loglevel <level>` | Logging level (`trace`, `debug`, `info`, `warn`, `error`, `fatal`) | `info` |
| `-resume` | Attempt to resume using the existing state database | `false` |
| `-write-visited-log` | Output a list of visited URLs after the crawl | `false` |
| `-pprof <addr>` | Enable pprof endpoint | `localhost:6060` |

### Example Usage Scenarios

**Basic Crawl:**
```bash
./crawler -site tensorflow_docs -loglevel info
```

**Resume a Large Crawl:**
```bash
./crawler -site pytorch_docs -resume -loglevel info
```

**High Performance Crawl with Profiling:**
```bash
./crawler -site small_docs -loglevel warn -pprof localhost:6060
```

**Debug Mode for Troubleshooting:**
```bash
./crawler -site test_site -loglevel debug
```

## 📁 Output Structure

Crawled content is saved under the `output_base_dir` defined in the config, organized by domain and preserving the site structure:

```
<output_base_dir>/
└── <sanitized_allowed_domain>/  # e.g., docs.example.com
    ├── images/                 # Only present if skip_images: false
    │   ├── image1.png
    │   └── image2.jpg
    ├── index.md                # Markdown for the root path
    ├── topic_one/
    │   ├── index.md
    │   └── subtopic_a.md
    └── topic_two.md
    └── ... (files/dirs mirroring site structure)
```

### Output Format

Each generated Markdown file contains:
- Original page title as level-1 heading
- Clean content converted from HTML to Markdown
- Relative links to other pages (when within the allowed domain)
- Local image references (if images are enabled)
- A footer with metadata including source URL and crawl timestamp

## 🔍 Directory Structure Utility

The codebase includes a utility (`pkg/utils/tree.go`) to generate a text file visualizing the directory structure of a crawled site's output. This can be helpful for verification and analysis.

**Usage Example:**
```go
package main

import (
    "log"
    "github.com/Sriram-PR/doc-scraper/pkg/utils"
)

func main() {
    err := utils.GenerateAndSaveTreeStructure("./crawled_docs/docs.example.com", "./tree_output.txt")
    if err != nil {
        log.Fatalf("Failed to generate tree: %v", err)
    }
}
```

## 🤝 Contributing

Contributions are welcome! Please feel free to open an issue to discuss bugs, suggest features, or propose changes.

**Pull Request Process:**
1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add some amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

Please ensure code adheres to Go best practices and includes appropriate documentation.

## 📝 License

This project is licensed under the **[MIT License]** - see the [LICENSE](LICENSE) file for details.

## 🙏 Acknowledgements

- [GoQuery](https://github.com/PuerkitoBio/goquery) for HTML parsing
- [html-to-markdown](https://github.com/JohannesKaufmann/html-to-markdown) for conversion
- [BadgerDB](https://github.com/dgraph-io/badger) for state persistence
- [Logrus](https://github.com/sirupsen/logrus) for structured logging

---

*Made with ❤️ for the LLM and Machine Learning community*
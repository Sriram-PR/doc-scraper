# LLM Documentation Scraper (`doc-scraper`)

[![Go Version](https://img.shields.io/github/go-mod/go-version/Sriram-PR/doc-scraper)](https://golang.org/)

> A configurable, concurrent, and resumable web crawler written in Go. Specifically designed to scrape technical documentation websites, extract core content, convert it cleanly to Markdown format suitable for ingestion by Large Language Models (LLMs), and save the results locally.

## Overview

This project provides a powerful command-line tool to crawl documentation sites based on settings defined in a `config.yaml` file. It navigates the site structure, extracts content from specified HTML sections using CSS selectors, and converts it into clean Markdown files.

### Why Use This Tool?

- **Built for LLM Training & RAG Systems** - Creates clean, consistent Markdown optimized for ingestion
- **Preserves Documentation Structure** - Maintains the original site hierarchy for context preservation
- **Production-Ready Features** - Offers resumable crawls, rate limiting, and graceful error handling
- **High Performance** - Uses Go's concurrency model for efficient parallel processing

## Goal: Preparing Documentation for LLMs

The main objective of this tool is to automate the often tedious process of gathering and cleaning web-based documentation for use with Large Language Models. By converting structured web content into clean Markdown, it aims to provide a dataset that is:

- **Text-Focused:** Prioritizes the textual content extracted via CSS selectors
- **Structured:** Maintains the directory hierarchy of the original documentation site, preserving context
- **Cleaned:** Converts HTML to Markdown, removing web-specific markup and clutter
- **Locally Accessible:** Provides the content as local files for easier processing and pipeline integration

## Key Features

| Feature | Description |
|---------|-------------|
| **Configurable Crawling** | Uses YAML for global and site-specific settings |
| **Scope Control** | Limits crawling by domain, path prefix, and disallowed path patterns (regex) |
| **Content Extraction** | Extracts main content using CSS selectors |
| **HTML-to-Markdown** | Converts extracted HTML to clean Markdown |
| **Image Handling** | Optional downloading and local rewriting of image links with domain and size filtering |
| **Link Rewriting** | Rewrites internal links to relative paths for local structure |
| **URL-to-File Mapping** | Optional TSV file logging saved file paths and their corresponding original URLs |
| **YAML Metadata Output**  | Optional detailed YAML file per site with crawl stats and per-page metadata (including content hashes) |
| **Concurrency** | Configurable worker pools and semaphore-based request limits (global and per-host) |
| **Rate Limiting** | Configurable per-host delays with jitter |
| **Robots.txt & Sitemaps** | Respects `robots.txt` and processes discovered sitemaps |
| **State Persistence** | Uses BadgerDB for state; supports resuming crawls via `resume` subcommand |
| **Graceful Shutdown** | Handles `SIGINT`/`SIGTERM` with proper cleanup |
| **HTTP Retries** | Exponential backoff with jitter for transient errors |
| **Observability** | Structured logging (`logrus`) and optional `pprof` endpoint |
| **Modular Code** | Organized into packages for clarity and maintainability |
| **CLI Utilities** | Built-in `validate` and `list-sites` commands for configuration management |
| **MCP Server Mode** | Expose as Model Context Protocol server for Claude Code/Cursor integration |

## Getting Started

### Prerequisites

- **Go:** Version 1.24 or later
- **Git:** For cloning the repository
- **Disk Space:** Sufficient for storing crawled content and state database

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

   This creates an executable named `crawler.exe` (Windows) or `crawler` (Linux/macOS) in the project root.

### Quick Start

1. Create a basic `config.yaml` file (see [Configuration](#configuration-configyaml) section)
2. Run the crawler:

   ```bash
   ./crawler crawl -site your_site_key -loglevel info
   ```

3. Find your crawled documentation in the `./crawled_docs/` directory

## Configuration (`config.yaml`)

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
enable_output_mapping: true
output_mapping_filename: "global_url_map.tsv"
enable_metadata_yaml: true
metadata_yaml_filename: "crawl_meta.yaml"

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
    # Override global mapping filename for this site
    output_mapping_filename: "pytorch_docs_map.txt"
    metadata_yaml_filename: "pytorch_metadata_output.yaml"
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
    # Disable mapping for this site, overriding global
    enable_output_mapping: false
    enable_metadata_yaml: false
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
| `enable_output_mapping` | Boolean | Enable URL-to-file mapping log | `false` |
| `output_mapping_filename` | String | Filename for the URL-to-file mapping log | `"url_to_file_map.tsv"` (if enabled and not set) |
| `enable_metadata_yaml` | Boolean | Enable detailed YAML metadata output file | `false` |
| `metadata_yaml_filename` | String  | Filename for the YAML metadata output file | `"metadata.yaml"` (if enabled & not set)   |
| `http_client_settings` | Object | HTTP client configuration | *(see below)* |
| `sites` | Map | Site-specific configurations | *(required)* |

**HTTP Client Settings:**
*(These are global and cannot be overridden per site in the current structure)*

- `timeout`: Overall request timeout (Default in code: `45s`)
- `max_idle_conns`: Total idle connections (Default in code: `100`)
- `max_idle_conns_per_host`: Idle connections per host (Default in code: `6`)
- `idle_conn_timeout`: Timeout for idle connections (Default in code: `90s`)
- `tls_handshake_timeout`: TLS handshake timeout (Default in code: `10s`)
- `expect_continue_timeout`: "100 Continue" timeout (Default in code: `1s`)
- `force_attempt_http2`: `null` (use Go default), `true`, or `false`. (Default in code: `null`)
- `dialer_timeout`: TCP connection timeout (Default in code: `15s`)
- `dialer_keep_alive`: TCP keep-alive interval (Default in code: `30s`)

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
- `enable_output_mapping`: `true` or `false`. Override global URL-to-file mapping enablement for this site.
- `output_mapping_filename`: String. Override global URL-to-file mapping filename for this site.
- `enable_metadata_yaml`: `true` or `false`. Override global YAML metadata output enablement for this site.
- `metadata_yaml_filename`: String. Override global YAML metadata filename for this site.

## Usage

Execute the compiled binary from the project root directory:

```bash
./crawler <command> [options]
```

### Commands

| Command | Description |
|---------|-------------|
| `crawl` | Start a fresh crawl |
| `resume` | Resume an interrupted crawl |
| `validate` | Validate configuration file without crawling |
| `list-sites` | List available site keys from config |
| `mcp-server` | Start MCP server for AI tool integration |
| `version` | Show version information |

### Command Options

**crawl / resume:**

| Flag | Description | Default |
|------|-------------|---------|
| `-config <path>` | Path to config file | `config.yaml` |
| `-site <key>` | **Required.** Site key from config | - |
| `-loglevel <level>` | Log level (`debug`, `info`, `warn`, `error`, `fatal`) | `info` |
| `-pprof <addr>` | pprof server address (empty to disable) | `localhost:6060` |
| `-write-visited-log` | Write visited URLs log on completion | `false` |

**validate:**

| Flag | Description | Default |
|------|-------------|---------|
| `-config <path>` | Path to config file | `config.yaml` |
| `-site <key>` | Site key to validate (optional, validates all if empty) | - |

**list-sites:**

| Flag | Description | Default |
|------|-------------|---------|
| `-config <path>` | Path to config file | `config.yaml` |

**mcp-server:**

| Flag | Description | Default |
|------|-------------|---------|
| `-config <path>` | Path to config file | `config.yaml` |
| `-transport <type>` | Transport type (`stdio`, `sse`) | `stdio` |
| `-port <num>` | HTTP port (for SSE transport) | `8080` |
| `-loglevel <level>` | Log level (`debug`, `info`, `warn`, `error`) | `info` |

### Example Usage Scenarios

**Basic Crawl:**

```bash
./crawler crawl -site tensorflow_docs -loglevel info
```

**Resume a Large Crawl:**

```bash
./crawler resume -site pytorch_docs -loglevel info
```

**Validate Configuration:**

```bash
./crawler validate -config config.yaml
./crawler validate -site pytorch_docs  # Validate specific site
```

**List Available Sites:**

```bash
./crawler list-sites
```

**High Performance Crawl with Profiling:**

```bash
./crawler crawl -site small_docs -loglevel warn -pprof localhost:6060
```

**Debug Mode for Troubleshooting:**

```bash
./crawler crawl -site test_site -loglevel debug
```

**Start MCP Server for Claude Desktop:**

```bash
./crawler mcp-server -config config.yaml
```

**Start MCP Server with SSE Transport:**

```bash
./crawler mcp-server -config config.yaml -transport sse -port 8080
```

## Output Structure

Crawled content is saved under the `output_base_dir` defined in the config, organized by domain and preserving the site structure:

```
<output_base_dir>/
└── <sanitized_allowed_domain>/  # e.g., docs.example.com
    ├── images/                 # Only present if skip_images: false
    │   ├── image1.png
    │   └── image2.jpg
    ├── index.md                # Markdown for the root path
    ├── <metadata_yaml_filename.yaml>
    ├── <output_mapping_filename.tsv>
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

## Directory Structure Output

After a successful crawl for a specific site, the crawler automatically generates a text file named `<sanitized_domain>_structure.txt` within the global `output_base_dir` (alongside the site's content folder). This file contains a visual tree representation of the generated directory structure for the crawled site, which can be helpful for verification and analysis.

**Example Location:**
If `output_base_dir` is `./crawled_docs` and you crawled `docs.example.com`, the structure file will be:
`./crawled_docs/docs.example.com_structure.txt`

## URL-to-File Mapping Output

When enabled via configuration, the crawler generates a mapping file (typically a `.tsv` or `.txt` file) for each crawled site. This file logs each successfully processed page's final absolute URL and the corresponding local filesystem path where its content was saved.

**Format:**
Each line in the file typically follows a tab-separated format:
`<FINAL_ABSOLUTE_URL><TAB><LOCAL_FILESYSTEM_PATH>`

This feature is controlled by the `enable_output_mapping` and `output_mapping_filename` settings in `config.yaml`.

## YAML Metadata Output

In addition to (or instead of) the simple TSV mapping, the crawler can generate a comprehensive YAML file for each crawled site. This file (`metadata.yaml` by default, configurable) contains overall crawl statistics and detailed metadata for every successfully processed page.

The filename can be configured globally and overridden per site using `enable_metadata_yaml` and `metadata_yaml_filename` in `config.yaml`.

## MCP Server Mode

The crawler can run as a [Model Context Protocol (MCP)](https://modelcontextprotocol.io/) server, enabling integration with AI assistants like Claude Code and Cursor.

### Available MCP Tools

| Tool | Description |
|------|-------------|
| `list_sites` | List all configured sites from config file |
| `get_page` | Fetch a single URL and return content as markdown |
| `crawl_site` | Start a background crawl for a site (returns job ID) |
| `get_job_status` | Check the status of a background crawl job |
| `search_crawled` | Search previously crawled content in JSONL files |

### Usage

**Stdio Transport (for Claude Desktop/Cursor):**

```bash
./crawler mcp-server -config config.yaml
```

**SSE Transport (HTTP-based):**

```bash
./crawler mcp-server -config config.yaml -transport sse -port 8080
```

### Claude Code Integration

Add to your Claude Code configuration (`claude_code_config.json`):

```json
{
  "mcpServers": {
    "doc-scraper": {
      "command": "/path/to/crawler",
      "args": ["mcp-server", "-config", "/path/to/config.yaml"]
    }
  }
}
```

### Tool Examples

**List available sites:**

```
Tool: list_sites
Result: Returns all configured sites with their domains and crawl status
```

**Fetch a single page:**

```
Tool: get_page
Arguments: { "url": "https://docs.example.com/guide", "content_selector": "article" }
Result: Returns page content as markdown with metadata
```

**Start a background crawl:**

```
Tool: crawl_site
Arguments: { "site_key": "pytorch_docs", "incremental": true }
Result: Returns job ID for tracking progress
```

**Check crawl progress:**

```
Tool: get_job_status
Arguments: { "job_id": "abc-123-def" }
Result: Returns status, pages processed, and completion info
```

**Search crawled content:**

```
Tool: search_crawled
Arguments: { "query": "neural network", "site_key": "pytorch_docs", "max_results": 10 }
Result: Returns matching pages with snippets
```

## Contributing

Contributions are welcome! Please feel free to open an issue to discuss bugs, suggest features, or propose changes.

**Pull Request Process:**

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add some amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

Please ensure code adheres to Go best practices and includes appropriate documentation.

## License

This project is licensed under the [Apache-2.0 License](https://github.com/Sriram-PR/doc-scraper/blob/main/LICENSE.txt).

## Acknowledgements

- [GoQuery](https://github.com/PuerkitoBio/goquery) for HTML parsing
- [html-to-markdown](https://github.com/JohannesKaufmann/html-to-markdown) for conversion
- [BadgerDB](https://github.com/dgraph-io/badger) for state persistence
- [Logrus](https://github.com/sirupsen/logrus) for structured logging
- [mcp-go](https://github.com/mark3labs/mcp-go) for MCP server implementation

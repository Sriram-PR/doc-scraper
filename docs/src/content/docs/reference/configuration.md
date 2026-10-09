---
title: Configuration
description: Every config.yaml option, with types and defaults.
---

## Overview

A `config.yaml` file is **required** to run the crawler. Create this file in the project root or specify its path using the `-config` flag. [`doc-scraper add <url>`](/doc-scraper/reference/cli/#add) drafts a site entry for you.

When configuring for LLM documentation processing, pay special attention to these settings:

- `sites.<your_site_key>.content_selector`: Define precisely to capture only relevant text
- `sites.<your_site_key>.allowed_domain` / `allowed_path_prefix`: Define scope accurately
- `enable_jsonl_output`: Must be `true` for search (`search`, `search_docs`) and crawl history (`get_freshness`, `diff_crawl`); it is off by default
- `skip_images`: Images are **not** downloaded by default (text-first). Set to `false` globally or per-site to download and localize images for offline consumption
- Adjust concurrency/delay settings based on the target site and your resources

## Example

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
global_crawl_timeout: 0s
skip_images: true # Default. Set to false to download and localize images
max_image_size_bytes: 10485760 # 10 MiB (applies only when images are downloaded)
enable_jsonl_output: true
jsonl_output_filename: "pages.jsonl"

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
    skip_images: false # Opt in to downloading images for this site
    disallowed_path_patterns:
      - "/docs/stable/.*/_modules/.*"
      - '/docs/stable/.*\.html#.*'

  tensorflow_docs:
    start_urls:
      - "https://www.tensorflow.org/guide"
      - "https://www.tensorflow.org/tutorials"
    allowed_domain: "www.tensorflow.org"
    allowed_path_prefix: "/"
    content_selector: ".devsite-article-body"
    max_depth: 0
    delay_per_host: 1s  # Site-specific override
    # Disable JSONL output for this site, overriding global
    enable_jsonl_output: false
    disallowed_path_patterns:
      - "/install/.*"
      - "/js/.*"
```

## Global options

| Option | Type | Description | Default |
|--------|------|-------------|---------|
| `default_user_agent` | String | Default User-Agent header for requests | `""` (Go default) |
| `default_delay_per_host` | Duration | Time to wait between requests to the same host | `0s` (no delay) |
| `num_workers` | Integer | Number of concurrent crawl workers | `4` |
| `num_image_workers` | Integer | Number of concurrent image download workers | same as `num_workers` |
| `max_requests` | Integer | Maximum concurrent requests (global) | `10` |
| `max_requests_per_host` | Integer | Maximum concurrent requests per host | `2` |
| `output_base_dir` | String | Base directory for crawled content | `"./crawled_docs"` |
| `state_dir` | String | Directory for BadgerDB state data | `"./crawler_state"` |
| `max_retries` | Integer | Maximum retry attempts for HTTP requests. To disable retries, set this to `0` together with a non-zero `initial_retry_delay`; `max_retries: 0` on its own is treated as unset and falls back to the default | `3` |
| `initial_retry_delay` | Duration | Initial delay for retry backoff | `1s` |
| `max_retry_delay` | Duration | Maximum delay for retry backoff | `30s` |
| `global_crawl_timeout` | Duration | Overall timeout for the entire crawl | `0s` (no timeout) |
| `per_page_timeout` | Duration | Timeout for processing a single page | `0s` (no timeout) |
| `skip_images` | Boolean | Whether to skip downloading images. Image downloading is opt-in | `true` (skip) |
| `max_image_size_bytes` | Integer | Maximum allowed image size (applies only when images are downloaded) | `0` (unlimited) |
| `max_page_size_bytes` | Integer | Maximum HTML page body size | `52428800` (50 MiB) |
| `enable_jsonl_output` | Boolean | Enable JSONL page output (one record per page plus a trailing crawl_meta record) for RAG pipelines. The search index (`search`, `search_docs`) and crawl history (`get_freshness`, `diff_crawl`) are built from this file, so turn it on for any site an agent will search | `false` |
| `jsonl_output_filename` | String | Filename for JSONL output | `"pages.jsonl"` |
| `enable_incremental` | Boolean | Enable incremental crawling globally | `false` |
| `crawl_history_retention` | Integer | Number of past crawls per site kept in the SQLite history index (powers `get_freshness`/`diff_crawl`) | `10` |
| `http_client_settings` | Object | HTTP client configuration, see [HTTP client settings](#http-client-settings) | |
| `sites` | Map | Site-specific configurations keyed by site key, see [Site options](#site-options) | required |

## HTTP client settings

*Global; cannot be overridden per site. Pool, dialer, and TLS timings are built in with sane defaults and are not exposed as config knobs.*

| Option | Type | Description | Default |
|--------|------|-------------|---------|
| `timeout` | Duration | Overall request timeout | `45s` |
| `max_idle_conns_per_host` | Integer | Idle connections per host | `2` |
| `allow_private_networks` | Boolean | Disables the SSRF guard that blocks dials to loopback / private / link-local / CGNAT / multicast addresses. Set to `true` only if you intentionally crawl internal documentation servers reachable via private IPs. The guard also works behind a proxy set with `HTTP_PROXY` / `HTTPS_PROXY`: the proxy itself may be on a private address, and each target is checked before it is sent to the proxy. If a target's hostname cannot be resolved locally, the request is refused | `false` |

## Site options

Each entry under `sites:` accepts these keys.

| Option | Type | Description | Default |
|--------|------|-------------|---------|
| `start_urls` | Array | Starting URLs for crawling | required |
| `allowed_domain` | String | Restrict crawling to this domain | required |
| `allowed_path_prefix` | String | Restrict crawling to URLs under this path prefix. Setting it is strongly recommended to bound scope | `/` (the whole domain) |
| `content_selector` | String | CSS selector for main content extraction, or `"auto"` for automatic detection | required |
| `link_extraction_selectors` | Array | CSS selectors for additional link extraction areas | |
| `disallowed_path_patterns` | Array | Regex patterns for URLs to skip | |
| `respect_nofollow` | Boolean | Whether to respect `rel="nofollow"` links | `false` |
| `user_agent` | String | Override the global user agent for this site | `default_user_agent` |
| `delay_per_host` | Duration | Override the global delay setting for this site | `default_delay_per_host` |
| `max_depth` | Integer | Exclusive upper bound on crawl depth from start URLs. Start pages are depth 0, so `1` crawls only the start pages, `2` adds their directly-linked pages, and so on. `0` = unlimited. URLs discovered from a `sitemap.xml` are seeded at depth 1 (one hop from the site root), so they are still bounded by `max_depth`: `max_depth: 1` stays start-only and skips sitemap expansion | `0` (unlimited) |
| `skip_images` | Boolean | Override the global image setting for this site. Images are skipped unless this (or the global `skip_images`) is set to `false` | global `skip_images` |
| `max_image_size_bytes` | Integer | Override the global max image size for this site | global `max_image_size_bytes` |
| `allowed_image_domains` | Array | Domains from which to download images | |
| `disallowed_image_domains` | Array | Domains to block image downloads from | |
| `enable_jsonl_output` | Boolean | Override the global JSONL output setting for this site. Search and crawl history need it on | global `enable_jsonl_output` |
| `jsonl_output_filename` | String | Override the global JSONL output filename for this site | global `jsonl_output_filename` |

Check a config without crawling with [`doc-scraper config validate`](/doc-scraper/reference/cli/#config-validate).

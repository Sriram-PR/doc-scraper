---
title: Parallel crawls
description: Crawl several sites at once with shared rate limits and a results summary.
sidebar:
  order: 2
---

Crawl multiple documentation sites concurrently with shared resource management. The orchestrator coordinates multiple crawlers while respecting global rate limits and semaphores.

## Usage

```bash
# Crawl specific sites in parallel
./doc-scraper crawl -sites pytorch_docs,tensorflow_docs,langchain_docs

# Crawl all configured sites
./doc-scraper crawl --all-sites

# Resume parallel crawl
./doc-scraper crawl -sites pytorch_docs,tensorflow_docs --resume
```

## Resource Sharing

When running parallel crawls, the following resources are shared across all site crawlers:
- **Global semaphore**: Limits total concurrent requests across all sites
- **HTTP client**: Shared connection pooling
- **Rate limiter**: Respects per-host delays

Each site still maintains its own:
- BadgerDB store for state persistence
- Output directory for crawled content
- Per-host semaphores for domain-specific limiting

## Results Summary

After all sites complete, the orchestrator logs a summary at `info` level (shown here with the timestamp and `component=parallel_crawl` attributes trimmed from each line):

```
level=INFO msg="============================================"
level=INFO msg="Parallel crawl completed in 14.775572588s"
level=INFO msg="Site Results:"
level=INFO msg="  rust_cli_book: SUCCESS - 18 pages in 647.885849ms"
level=INFO msg="  broken_docs: FAILED - 1 pages in 14.723297247s"
level=INFO msg="    Error: crawl completed with zero successful pages: all 1 attempted page tasks failed"
level=INFO msg="--------------------------------------------"
level=INFO msg="Total: 2 sites (1 success, 1 failed), 19 pages processed"
level=INFO msg="============================================"
```

The failing site here is one whose start URL host does not resolve. The per-site page count is the number of page tasks processed, so a failed site can still report a nonzero count. The process exits non-zero when any site fails.

Unknown or misspelled site keys are rejected **before** the crawl starts, so they never appear as a `FAILED` row in this summary. For example, `crawl -sites pytorch_docs,typo_key` exits immediately (non-zero) with:

```
Invalid site keys: site 'typo_key' not found. Available sites: [pytorch_docs tensorflow_docs langchain_docs]
```

The `FAILED` rows in the summary are for sites that exist in the config but errored during the crawl itself.

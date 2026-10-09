# LLM Documentation Scraper (`doc-scraper`)

[![CI](https://github.com/Sriram-PR/doc-scraper/actions/workflows/ci.yml/badge.svg)](https://github.com/Sriram-PR/doc-scraper/actions/workflows/ci.yml)
[![Go Version](https://img.shields.io/github/go-mod/go-version/Sriram-PR/doc-scraper)](https://golang.org/)
[![Go Reference](https://pkg.go.dev/badge/github.com/Sriram-PR/doc-scraper/v2.svg)](https://pkg.go.dev/github.com/Sriram-PR/doc-scraper/v2)
[![License](https://img.shields.io/github/license/Sriram-PR/doc-scraper)](https://github.com/Sriram-PR/doc-scraper/blob/main/LICENSE)
[![Glama score](https://glama.ai/mcp/servers/Sriram-PR/doc-scraper/badges/score.svg)](https://glama.ai/mcp/servers/Sriram-PR/doc-scraper)

> A configurable, concurrent, and resumable web crawler written in Go. Specifically designed to scrape technical documentation websites, extract core content, convert it cleanly to Markdown format suitable for ingestion by Large Language Models (LLMs), and save the results locally.

![doc-scraper crawling a docs site and answering search queries offline](demo/demo.gif)

doc-scraper crawls documentation sites into clean Markdown and serves them to coding agents over MCP. Your agent searches and reads the docs you chose, offline, instead of guessing from training data or a live web search. Point `doc-scraper add` at any docs page and it detects the framework (30+ docs generators, including Docusaurus, Starlight, Sphinx, VitePress, Material for MkDocs, and mdBook), validates the content selector, and drafts the config for you.

**Documentation: https://sriram-pr.github.io/doc-scraper/**

## Features

- **Offline search for agents:** BM25 full-text search (`search_docs`) and page reads (`read_page`) over the stored corpus, with no network access
- **Framework detection:** 30+ docs generators recognized, each selector validated on the page, Readability fallback, JavaScript-only shells reported instead of crawled empty
- **Clean output:** GitHub-Flavored Markdown per page with frontmatter, plus `pages.jsonl`, `llms.txt`, and `llms-full.txt`
- **Polite and resumable:** robots.txt and sitemaps, per-host rate limits, retries with backoff, resumable crawls, and staged output that never clobbers the last good copy
- **Stays fresh:** incremental re-crawls, watch mode, and crawl history (`get_freshness`, `diff_crawl`)
- **Many sites at once:** parallel crawls with shared limits

## Install

Download a release binary (Linux, macOS, Windows; amd64 and arm64) from the [Releases page](https://github.com/Sriram-PR/doc-scraper/releases/latest), or:

```bash
go install github.com/Sriram-PR/doc-scraper/v2/cmd/doc-scraper@latest
```

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$PWD":/data ghcr.io/sriram-pr/doc-scraper:latest crawl -site rust_cli_book
```

Claude Desktop users can install `doc-scraper.mcpb` from the latest release instead. Details: [Install](https://sriram-pr.github.io/doc-scraper/getting-started/install/).

## Quick start

```bash
echo 'enable_jsonl_output: true' > config.yaml    # JSONL feeds the search index
doc-scraper add -site rust_cli_book https://rust-cli.github.io/book/index.html   # detect, preview, confirm
doc-scraper crawl -site rust_cli_book             # Markdown lands in ./crawled_docs/rust_cli_book/
doc-scraper search -site rust_cli_book "error handling"
```

Then give it to Claude Code:

```bash
claude mcp add --transport stdio doc-scraper -- /path/to/doc-scraper mcp-server -config /path/to/config.yaml
```

See [Claude Code setup](https://sriram-pr.github.io/doc-scraper/mcp/claude-code/) for scopes and a first prompt.

## Docs

- [Getting started](https://sriram-pr.github.io/doc-scraper/getting-started/install/): install, quick start, adding a site
- [Use with agents (MCP)](https://sriram-pr.github.io/doc-scraper/mcp/overview/): Claude Code, Claude Desktop, any stdio client, and the 11 tools
- [Docs frameworks](https://sriram-pr.github.io/doc-scraper/frameworks/overview/): how detection works, with guides for Docusaurus, Starlight, Sphinx, VitePress, Material for MkDocs, and mdBook
- [Configuration](https://sriram-pr.github.io/doc-scraper/reference/configuration/) and [CLI](https://sriram-pr.github.io/doc-scraper/reference/cli/) reference
- [Detection benchmark](https://sriram-pr.github.io/doc-scraper/benchmark/)

## Contributing

Contributions are welcome. Read [CONTRIBUTING.md](CONTRIBUTING.md) before starting: it covers setup, the testing rules CI enforces, how to claim an issue, and the policy on AI-assisted contributions. Issues labeled [`good first issue`](https://github.com/Sriram-PR/doc-scraper/labels/good%20first%20issue) are a good place to begin.

Report security problems privately as described in [SECURITY.md](SECURITY.md). Everyone taking part follows the [Code of Conduct](CODE_OF_CONDUCT.md).

## Privacy Policy

doc-scraper collects nothing: no telemetry, no analytics, no accounts. All output and state stays on your machine, and the only network requests it makes are the crawls and fetches you explicitly ask for. Full policy: [PRIVACY.md](https://github.com/Sriram-PR/doc-scraper/blob/main/PRIVACY.md).

## License

This project is licensed under the [Apache-2.0 License](https://github.com/Sriram-PR/doc-scraper/blob/main/LICENSE).

## Acknowledgements

- [GoQuery](https://github.com/PuerkitoBio/goquery) for HTML parsing
- [html-to-markdown](https://github.com/JohannesKaufmann/html-to-markdown) for conversion
- [BadgerDB](https://github.com/dgraph-io/badger) for state persistence
- [mcp-go](https://github.com/mark3labs/mcp-go) for MCP server implementation
- [go-readability](https://github.com/go-shiori/go-readability) for content extraction fallback
- [modernc.org/sqlite](https://gitlab.com/cznic/sqlite) for the pure-Go crawl-history index
- [robotstxt](https://github.com/temoto/robotstxt) for `robots.txt` parsing
- [yaml.v3](https://github.com/go-yaml/yaml) for configuration parsing
- [google/uuid](https://github.com/google/uuid) for crawl job IDs
- [x/sync](https://pkg.go.dev/golang.org/x/sync) for the weighted semaphores that cap concurrency

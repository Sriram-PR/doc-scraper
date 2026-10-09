---
title: CLI
description: doc-scraper commands, their flags, and examples.
---

Run the binary with a command and its options:

```bash
doc-scraper <command> [options]
```

Flags accept one or two dashes (`-all-sites` and `--all-sites` are the same flag). Every command prints its own flags with `-h`.

| Command | Description |
|---------|-------------|
| `crawl` | Start a crawl (add `--resume` to continue an interrupted one) |
| `add` | Probe a docs site and draft a config entry for it: detects the framework, proposes crawl scope from the sitemap, previews one extracted page, and writes only after confirmation |
| `config validate` | Validate configuration file without crawling |
| `config list` | List available site keys from config |
| `mcp-server` | Start MCP server for AI tool integration |
| `search` | Ranked full-text search over the crawled corpus (BM25, stemming, section anchors) |
| `watch` | Watch sites and re-crawl on schedule |
| `version` | Show version information |
| `run` | Read a JSON task spec from stdin and dispatch a crawl or watch (for orchestration/automation) |

## `crawl`

Crawl one site, several sites in parallel, or every configured site. One of `-site`, `-sites`, or `-all-sites` is required.

| Flag | Type | Description | Default |
|------|------|-------------|---------|
| `-config` | string | Path to config file | `config.yaml` |
| `-site` | string | Site key from config (single site) | |
| `-sites` | string | Comma-separated site keys for parallel crawling | |
| `-all-sites` | bool | Crawl all configured sites in parallel | `false` |
| `-resume` | bool | Resume an interrupted crawl from existing state | `false` |
| `-incremental` | bool | Enable incremental crawling (skip unchanged pages) | `false` |
| `-full` | bool | Force full crawl (ignore incremental settings) | `false` |
| `-loglevel` | string | Log level (`debug`, `info`, `warn`, `error`) | `info` |
| `-json` | bool | Emit logs as JSON (one record per line) instead of text | `false` |
| `-pprof` | string | pprof server address, e.g. `localhost:6060`. Only effective in builds with `-tags pprof`; default builds log a warning and ignore the flag | `""` (disabled) |

## `watch`

Re-crawl sites on a schedule. One of `-site`, `-sites`, or `-all-sites` is required.

| Flag | Type | Description | Default |
|------|------|-------------|---------|
| `-config` | string | Path to config file | `config.yaml` |
| `-site` | string | Site key from config (single site) | |
| `-sites` | string | Comma-separated site keys to watch | |
| `-all-sites` | bool | Watch all configured sites | `false` |
| `-interval` | string | Crawl interval (e.g. `30m`, `1h`, `24h`, `7d`) | `24h` |
| `-loglevel` | string | Log level (`debug`, `info`, `warn`, `error`) | `info` |
| `-json` | bool | Emit logs as JSON (one record per line) instead of text | `false` |

## `add`

```bash
doc-scraper add https://vitepress.dev/guide/what-is-vitepress
```

Probes the site with a handful of polite requests (the page, robots.txt, llms.txt, the sitemap), then shows what it found before anything is written: the detected framework and content selector (validated against the fetched page), a crawl scope clustered from the sitemap with page counts as evidence, sibling version/locale trees proposed as exclusions, and a markdown preview of the extracted page with code-block fidelity numbers. The entry is appended to your config only after you confirm; the rest of the file is preserved byte-for-byte, comments included.

| Flag | Type | Description | Default |
|------|------|-------------|---------|
| `-config` | string | Path to config file (created if missing) | `config.yaml` |
| `-site` | string | Site key to use instead of the derived one | |
| `-selector` | string | Content CSS selector, skipping auto-detection | |
| `-depth` | int | Override the proposed `max_depth` | |
| `-yes` | bool | Write without prompting | `false` |
| `-dry-run` | bool | Draft only, never write (exit code 2) | `false` |
| `-json` | bool | Emit the draft as JSON on stdout (human text goes to stderr) | `false` |

Exit codes: `0` written, `1` error, `2` drafted but not written. For agents and scripts: `add -dry-run -json <url>` inspects, then `add -yes <url>` commits; with no terminal attached the command fails fast instead of waiting on stdin. Sites whose robots.txt disallows crawling the given path are refused, and robots rules that restrict AI crawlers are surfaced as a warning.

## `search`

```bash
doc-scraper search -site rust_cli_book -limit 5 "error handling"
```

Searches the crawled corpus offline, with no network access. The non-flag arguments form the query (FTS5 query syntax is supported). Options may appear before or after the query; use `--` to end option parsing.

| Flag | Type | Description | Default |
|------|------|-------------|---------|
| `-config` | string | Path to config file | `config.yaml` |
| `-site` | string | Limit results to one site key | all sites |
| `-limit` | int | Maximum results to return (must be positive) | `10` |
| `-json` | bool | Emit results as a JSON array instead of human-readable text | `false` |

## `mcp-server`

Start the MCP server over the stdio transport. See [MCP tools](/doc-scraper/mcp/tools/) for what it exposes.

| Flag | Type | Description | Default |
|------|------|-------------|---------|
| `-config` | string | Path to config file | `config.yaml` |
| `-loglevel` | string | Log level (`debug`, `info`, `warn`, `error`) | `info` |

## `config validate`

Validate the configuration file without crawling.

| Flag | Type | Description | Default |
|------|------|-------------|---------|
| `-config` | string | Path to config file | `config.yaml` |
| `-site` | string | Site key to validate (validates all if empty) | |
| `-json` | bool | Emit a single JSON object instead of human-readable text | `false` |

## `config list`

List the site keys in the configuration file.

| Flag | Type | Description | Default |
|------|------|-------------|---------|
| `-config` | string | Path to config file | `config.yaml` |
| `-json` | bool | Emit a single JSON object instead of human-readable text | `false` |

## `run`

Reads a single JSON task spec from stdin and dispatches a crawl or watch. It takes no flags; see [task specs](/doc-scraper/guides/task-specs/).

## Examples

**Basic Crawl:**

```bash
./doc-scraper crawl -site tensorflow_docs -loglevel info
```

**Resume a Large Crawl:**

```bash
./doc-scraper crawl -site pytorch_docs --resume -loglevel info
```

**Validate Configuration:**

```bash
./doc-scraper config validate -config config.yaml
./doc-scraper config validate -site pytorch_docs  # Validate specific site
```

**List Available Sites:**

```bash
./doc-scraper config list
```

**High Performance Crawl with Profiling:**

```bash
./doc-scraper crawl -site small_docs -loglevel warn -pprof localhost:6060
```

**Debug Mode for Troubleshooting:**

```bash
./doc-scraper crawl -site test_site -loglevel debug
```

**Parallel Crawl of Multiple Sites:**

```bash
./doc-scraper crawl -sites pytorch_docs,tensorflow_docs,langchain_docs
```

**Crawl All Configured Sites:**

```bash
./doc-scraper crawl --all-sites
```

**Start MCP Server for Claude Desktop:**

```bash
./doc-scraper mcp-server -config config.yaml
```

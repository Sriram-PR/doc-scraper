---
title: MCP tools
description: The tools the doc-scraper MCP server exposes, with example calls.
sidebar:
  order: 5
---

`doc-scraper mcp-server` exposes these tools over stdio to any [Model Context Protocol](https://modelcontextprotocol.io/) client.
Its `initialize` response carries `instructions` that tell the agent to call `describe_server` first. The tool list is fixed, so the server does not advertise `listChanged`.

## Tools

| Tool | Description |
|------|-------------|
| `describe_server` | Orientation manifest: server identity + sites + recent jobs in one call (call this first) |
| `list_sites` | List all configured sites (key, domain, path prefix, depth, last crawled time, and a running flag) |
| `get_page` | Fetch a single HTML page live over the network and return content as markdown, paginated like `read_page` |
| `crawl_site` | Start a background crawl for a site (returns job ID) |
| `get_job_status` | Check the status of a background crawl job, optionally waiting up to 60 seconds for it to finish |
| `cancel_crawl` | Cancel a running or pending crawl job by job ID |
| `draft_site` | Draft a site entry for a new docs URL, or start from a configured site and change fields with `overrides`; previews one extracted page and never writes the config |
| `save_site` | Save a drafted entry to the config file and load it into the running server; needs `mcp-server -allow-config-writes` |
| `list_pages` | Enumerate crawled pages for a site (paginated, metadata only) |
| `read_page` | Return a crawled page's markdown from the stored output, without network access; pass a search result's `anchor` to read only that section |
| `search_docs` | Full-text search across crawled docs (BM25, stemming, snippets), without network access |
| `get_freshness` | Report how stale a site's latest crawl is, from the crawl-history index |
| `diff_crawl` | Report pages added, removed, or changed since the previous crawl, or since a given timestamp |

## Adding and fixing sites

`draft_site` and `save_site` let an agent bring in a docs site that isn't configured, or fix one that extracts badly, without leaving the session. `draft_site` takes a `url` (it probes the site the way `doc-scraper add` does) or the `site_key` of a configured site, plus `overrides` for any entry field by its config name, and previews one page extracted with the result. It never writes. `save_site` writes the entry to the config file, changing only that entry's lines, and loads it into the running server; overwriting a configured site needs `replace: true`, and a save is refused while that site is crawling.

`save_site` works only when the server starts with `-allow-config-writes`; `describe_server` reports `config_writes: enabled` or `disabled`. With the flag, the server also starts from a config with no sites. Entries can set every per-site field except `enable_jsonl_output` and `jsonl_output_filename`, which would turn off the stored output `read_page` and `search_docs` read; a replace keeps the entry's existing values for those two.

## Results

Every tool declares an `outputSchema` and returns its result as `structuredContent`, with the same JSON value in a text content block for clients that read only text. The text is not byte-identical to other encoders' output: Go escapes `<`, `>` and `&` in strings as `\u003c`, `\u003e` and `\u0026`, which decode to the same characters. Schemas allow additional fields, so newer servers can add fields without breaking clients. Errors are returned as `isError` results with a text message and no `structuredContent`.

Numeric limits (`max_results`, `limit`, `max_bytes`, `wait_seconds`) above their maximum are capped, and 0 or a negative value means the default. Results echo the values actually used: `max_results` and `offset` in `list_pages` and `diff_crawl`, `limit` in `search_docs`, and `offset` in `read_page` and `get_page`. An offset past the end returns nothing and is echoed as the end. All timestamps are UTC in RFC 3339 form, such as `2026-10-18T13:52:28Z`.

`get_freshness` reports the latest crawl as a `last_crawl` object (`crawl_id`, `crawl_started_at`, `crawl_ended_at`, `total_pages`, `mode`), the same shape as `diff_crawl`'s `current_crawl` and `baseline_crawl`. To see what a crawl changed, call `diff_crawl` without `since` once it finishes: the latest crawl is compared with the one before it. With `since`, the baseline is the most recent crawl whose `crawl_ended_at` is at or before it, so pass a `crawl_ended_at` read before the new crawl, not after.

`search_docs` returns `results` best first. Each result's `match` is `all` when it contains every query word (common words such as "how" or "the" aside) or `partial` when it came from the fallback that fills the remaining slots up to `limit` with results containing only some of the words, or built-in synonyms in their place; partial results always follow full ones. Queries using FTS5 syntax are never relaxed. A query made only of common words, unless quoted, is rejected as having no searchable terms. `rank` is the raw BM25 score and compares only results with the same `match`.

Jobs have one shape everywhere: `describe_server`'s `recent_jobs` and `get_freshness`'s `running_job` carry the same fields as `get_job_status` (`job_id`, `site_key`, `status`, page counts (`pages_processed`, `pages_queued`, `pages_unchanged`, `pages_saved`, `pages_failed`, `pages_carried_forward`), `failures`, `refused`, `incremental`, and `completed_at`, `duration_seconds` and `error_message` once they apply).

## Examples

**List available sites:**

```
Tool: list_sites
Result: Returns each configured site's key, domain, path prefix, max depth, last crawled time, and a running status while a crawl is active
```

**Fetch a single page:**

```
Tool: get_page
Arguments: { "url": "https://docs.example.com/guide", "content_selector": "article" }
Result: Returns up to max_bytes (default 102400) of the page as markdown with metadata; when truncated, pass next_offset as offset to read the rest
```

**Start a background crawl:**

```
Tool: crawl_site
Arguments: { "site_key": "pytorch_docs", "incremental": true }
Result: Returns job ID for tracking progress. Add "allow_shrink": true to publish a crawl that would otherwise be kept back for losing pages
```

**Check crawl progress:**

```
Tool: get_job_status
Arguments: { "job_id": "abc-123-def", "wait_seconds": 60 }
Result: Waits until the job finishes or 60 seconds pass, then returns status, live page counts (pages_processed, pages_queued, pages_unchanged, pages_saved, pages_failed, pages_carried_forward), failures by class, and completion info. On an incremental crawl, pages_unchanged counts re-checked pages whose content had not changed; pages_processed excludes them.
```

`failures` lists failed pages by class: `temporary` (server errors, rate limiting, network errors, timeouts), `gone` (404 or 410), `selector` (the content selector matched nothing), `policy` (robots.txt, scope, non-HTML, depth) and `other`, each with a count and one sample URL and error. A job whose crawl finished but lost a large share of its pages fails with `refused` set to `empty`, `temporary_failures` or `shrink`; the stored corpus is kept, and `error_message` says how to continue: `crawl_site` with `incremental:true` retries the failed pages, and adding `allow_shrink:true` publishes the kept crawl as it is.

**Enumerate crawled pages:**

```
Tool: list_pages
Arguments: { "site_key": "pytorch_docs", "max_results": 50, "offset": 0 }
Result: Returns up to 50 page entries (URL, title, depth, crawled_at, content_length), sorted by URL. Use offset for pagination.
```

**Cancel a running crawl:**

```
Tool: cancel_crawl
Arguments: { "job_id": "abc-123-def" }
Result: Returns cancelled: true/false and the job's current status. Has no effect on jobs already in a terminal state.
```

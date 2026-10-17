---
title: MCP tools
description: The tools the doc-scraper MCP server exposes, with example calls.
sidebar:
  order: 5
---

`doc-scraper mcp-server` exposes these tools over stdio to any [Model Context Protocol](https://modelcontextprotocol.io/) client.

## Tools

| Tool | Description |
|------|-------------|
| `describe_server` | Orientation manifest: server identity + sites + recent jobs in one call (call this first) |
| `list_sites` | List all configured sites (key, domain, path prefix, depth, last crawled time, and a running flag) |
| `get_page` | Fetch a single URL live over the network and return content as markdown |
| `crawl_site` | Start a background crawl for a site (returns job ID) |
| `get_job_status` | Check the status of a background crawl job |
| `cancel_crawl` | Cancel a running or pending crawl job by job ID |
| `list_pages` | Enumerate crawled pages for a site (paginated, metadata only) |
| `read_page` | Return a crawled page's markdown from the stored output, without network access |
| `search_docs` | Full-text search across crawled docs (BM25, stemming, snippets), without network access |
| `get_freshness` | Report how stale a site's latest crawl is, from the crawl-history index |
| `diff_crawl` | Report pages added, removed, or changed since a given timestamp |

## Results

Every tool declares an `outputSchema` and returns its result as `structuredContent`, with the same JSON in a text content block for clients that read only text. Schemas allow additional fields, so newer servers can add fields without breaking clients. Errors are returned as `isError` results with a text message and no `structuredContent`.

`get_freshness` reports the latest crawl as a `last_crawl` object (`crawl_id`, `crawl_started_at`, `crawl_ended_at`, `total_pages`, `mode`), the same shape as `diff_crawl`'s `current_crawl` and `baseline_crawl`. Pass `last_crawl.crawl_ended_at` as `diff_crawl`'s `since`.

`search_docs` returns `results` best first. Each result's `match` is `all` when it contains every query word (common words such as "how" or "the" aside) or `partial` when it came from the fallback that fills the remaining slots up to `limit` with results containing only some of the words; partial results always follow full ones. Queries using FTS5 syntax are never relaxed. `rank` is the raw BM25 score and compares only results with the same `match`.

Jobs have one shape everywhere: `describe_server`'s `recent_jobs` and `get_freshness`'s `running_job` carry the same fields as `get_job_status` (`job_id`, `site_key`, `status`, page counts, `incremental`, and `completed_at`, `duration_seconds` and `error_message` once they apply).

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
Result: Returns status, live page counts (pages_processed, pages_queued, pages_unchanged), and completion info. On an incremental crawl, pages_unchanged counts re-checked pages whose content had not changed; pages_processed excludes them.
```

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

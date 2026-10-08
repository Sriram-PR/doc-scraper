---
title: MCP tools
description: The tools the doc-scraper MCP server exposes, with example calls.
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
Result: Returns status, pages processed, and completion info
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

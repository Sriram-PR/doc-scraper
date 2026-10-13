---
title: Overview
description: What a coding agent gets from the doc-scraper MCP server, and how a typical session flows.
sidebar:
  order: 1
---

`doc-scraper mcp-server` gives an agent its own copy of the documentation it needs. You crawl a site once; after that the agent searches and reads the stored pages without touching the network, so answers come from the docs you chose, not from a live web search.

## What the agent gets

- **Offline search and reading.** `search_docs` runs ranked full-text search (BM25, stemming, snippets, section anchors) over everything crawled, and `read_page` returns a stored page's Markdown. Neither makes a network request.
- **Crawls on demand.** `crawl_site` starts a background crawl of a configured site and returns a job ID; `get_job_status` and `cancel_crawl` manage it.
- **Freshness.** `get_freshness` reports how old a site's latest crawl is, and `diff_crawl` lists pages added, removed, or changed since a timestamp.
- **An explicit live fetch.** `get_page` is the one tool that goes to the network: it fetches a single URL and returns it as Markdown.

The full list is on the [MCP tools](/doc-scraper/mcp/tools/) page.

## A typical session

1. You list the sites in `config.yaml` ([`doc-scraper add <url>`](/doc-scraper/getting-started/add-a-site/) drafts each entry). The crawl's JSONL output, on by default, feeds the search index and crawl history.
2. The agent calls `describe_server` to see the configured sites and recent jobs, then `crawl_site` for any site that has not been crawled.
3. It polls `get_job_status` until the crawl completes.
4. From then on it answers questions with `search_docs`, then `read_page` on the best hits.

## Connect a client

The server speaks MCP over stdio, so any client that launches a local command can use it.

- [Claude Code](/doc-scraper/mcp/claude-code/)
- [Claude Desktop](/doc-scraper/mcp/claude-desktop/)
- [Any stdio client](/doc-scraper/mcp/generic-stdio/)

Use absolute paths for `output_base_dir` and `state_dir` in a config that an MCP client launches: relative paths resolve against the server's working directory, which the client chooses.

---
title: Any stdio client
description: Run the doc-scraper MCP server from any MCP client that launches a local command.
sidebar:
  order: 4
---

The server uses the stdio transport only (the SSE transport was removed in v2.x). A client launches it as a subprocess and exchanges JSON-RPC messages over stdin and stdout; logs go to stderr.

## Command

```bash
doc-scraper mcp-server -config /path/to/config.yaml
```

Most clients take the command in this shape:

```json
{
  "command": "/path/to/doc-scraper",
  "args": ["mcp-server", "-config", "/path/to/config.yaml"],
  "env": {}
}
```

Use absolute paths, both here and for `output_base_dir` and `state_dir` inside the config: the client decides the server's working directory.

## A raw session

An `initialize` handshake followed by a `list_sites` call, piped straight into the server:

```bash
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"demo","version":"0"}}}' \
  '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_sites","arguments":{}}}' \
  | doc-scraper mcp-server -config /tmp/mcpdemo/config.yaml 2>/dev/null
```

```json
{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25","capabilities":{"tools":{}},"serverInfo":{"name":"doc-scraper","version":"2.19.0"},"instructions":"Call describe_server first: it lists the configured sites, when each was last crawled, and recent crawl jobs. search_docs, list_pages and read_page work offline on stored crawls; run crawl_site for a site that has not been crawled yet. get_page fetches a URL live."}}
{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"{\"sites\":[{\"key\":\"demo\",\"domain\":\"example.com\",\"path_prefix\":\"\",\"start_urls_count\":1,\"max_depth\":0}],\"config_path\":\"/tmp/mcpdemo/config.yaml\",\"total_sites\":1}"}],"structuredContent":{"sites":[{"key":"demo","domain":"example.com","path_prefix":"","start_urls_count":1,"max_depth":0}],"config_path":"/tmp/mcpdemo/config.yaml","total_sites":1}}}
```

Output from doc-scraper 2.19.0, 2026-10-20. Each tool result carries the data twice: as structuredContent, which matches the tool's outputSchema, and as the same JSON value in a text content block. The tools and their arguments are listed on [MCP tools](/doc-scraper/mcp/tools/).

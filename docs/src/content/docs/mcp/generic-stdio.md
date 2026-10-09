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
{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25","capabilities":{"logging":{},"tools":{"listChanged":true}},"serverInfo":{"name":"doc-scraper","version":"2.10.1"}}}
{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"{\n  \"config_path\": \"/tmp/mcpdemo/config.yaml\",\n  \"sites\": [\n    {\n      \"domain\": \"example.com\",\n      \"key\": \"demo\",\n      \"max_depth\": 0,\n      \"path_prefix\": \"\",\n      \"start_urls_count\": 1\n    }\n  ],\n  \"total_sites\": 1\n}"}]}}
```

Output from doc-scraper 2.10.1 (built from `main`), 2026-10-10. Tool results are JSON documents carried in a text content block. The tools and their arguments are listed on [MCP tools](/doc-scraper/mcp/tools/).

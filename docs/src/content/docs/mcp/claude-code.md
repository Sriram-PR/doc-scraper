---
title: Claude Code
description: Register the doc-scraper MCP server with Claude Code and give your agent offline docs search.
sidebar:
  order: 2
---

## Add the server

Register the server with `claude mcp add` (everything after `--` is the server command):

```bash
claude mcp add --transport stdio doc-scraper -- /path/to/doc-scraper mcp-server -config /path/to/config.yaml
```

This uses the default `local` scope: the server is available to you in the current project only. Add `--scope user` to make it available in all your projects.

## Share it with your team

Add `--scope project` to the command above and Claude Code writes a `.mcp.json` at the project root that you can commit:

```json
{
  "mcpServers": {
    "doc-scraper": {
      "type": "stdio",
      "command": "/path/to/doc-scraper",
      "args": ["mcp-server", "-config", "/path/to/config.yaml"],
      "env": {}
    }
  }
}
```

Claude Code asks each person to approve project-scoped servers from `.mcp.json` before first use in an interactive session.

## Check the connection

```bash
claude mcp get doc-scraper
```

Inside a session, `/mcp` shows the server's status and tools.

## First prompt

Ask Claude to orient itself and fetch a corpus, for example:

> Call `describe_server` from doc-scraper. If `rust_cli_book` has never been crawled, crawl it and wait for the job to finish. Then search it for "error handling" and summarize the best page.

That prompt leads Claude through `describe_server`, `crawl_site`, `get_job_status`, `search_docs`, and `read_page`. Once the crawl exists, later sessions can go straight to search.

See the [Claude Code MCP docs](https://code.claude.com/docs/en/mcp) for scopes and approvals. Checked against the Claude Code docs and `claude` 2.1.286 on 2026-10-10.

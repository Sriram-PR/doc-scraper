---
title: Claude Desktop
description: Install doc-scraper in Claude Desktop as a one-click MCP bundle.
sidebar:
  order: 3
---

Every release ships `doc-scraper.mcpb`, an MCP Bundle that Claude Desktop installs like a browser extension. It contains the doc-scraper binaries (a universal macOS binary, plus Windows and Linux amd64), so nothing else needs to be installed.

## Install

1. Write a `config.yaml` listing the sites you want (see the [quick start](/doc-scraper/getting-started/quick-start/) and [configuration reference](/doc-scraper/reference/configuration/)). Use absolute paths for `output_base_dir` and `state_dir`, because the server's working directory is chosen by Claude Desktop. Set `enable_jsonl_output: true` so `search_docs` has an index to search.
2. Download `doc-scraper.mcpb` from the [latest release](https://github.com/Sriram-PR/doc-scraper/releases/latest).
3. Install it in Claude Desktop by any of:
   - double-clicking the `.mcpb` file;
   - dragging it into the Claude Desktop window;
   - **Settings > Extensions > Advanced settings > Install Extension…** and selecting the file.
4. In the installation dialog, set **doc-scraper config file** to the path of your `config.yaml`. This is the bundle's only setting and it is required.

The bundle runs `doc-scraper mcp-server -config <your config file>`.

## Verify

Start a new chat and ask Claude to call `describe_server` from doc-scraper. It should list the sites from your config. Then ask it to crawl one with `crawl_site` and search it with `search_docs`.

Install steps checked against Anthropic's [MCP Bundle documentation](https://claude.com/docs/connectors/building/mcpb) on 2026-10-10.

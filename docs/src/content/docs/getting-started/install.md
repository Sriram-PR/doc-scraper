---
title: Install
description: Install doc-scraper from a release binary, Docker, the Claude Desktop extension, go install, or source.
sidebar:
  order: 1
---

## Prerequisites

- **Disk Space:** Sufficient for storing crawled content and state database
- **Go:** Version 1.26 or later, only for `go install` or building from source
- **Git:** Only for building from source

## Release Binaries

Download the archive for your OS and architecture (Linux, macOS, Windows; amd64 and arm64) from the [Releases page](https://github.com/Sriram-PR/doc-scraper/releases/latest). Each release includes a `checksums.txt` to verify the download:

```bash
sha256sum --check --ignore-missing checksums.txt
```

Extract the archive and put `doc-scraper` on your `PATH`. The archive also ships a sample `config.yaml`.

## Docker

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$PWD":/data ghcr.io/sriram-pr/doc-scraper:latest crawl -site rust_cli_book
```

The image (linux/amd64 and linux/arm64) uses `/data` as its working directory and `doc-scraper` as its entrypoint, so the arguments after the image name are the subcommand. Mount a directory containing `config.yaml` at `/data` and point `output_base_dir` and `state_dir` at relative paths so crawl output lands in the mounted directory. The image runs as a non-root user, so `--user` makes the bind mount writable. Versioned tags (for example `2.9.2`) are published alongside `latest`.

## Claude Desktop Extension

Download `doc-scraper.mcpb` from the [latest release](https://github.com/Sriram-PR/doc-scraper/releases/latest) and open it in Claude Desktop. It prompts for the path to your `config.yaml` and runs the bundled binary as an MCP server (see [MCP tools](/doc-scraper/mcp/tools/)).

## Go Install

Install the latest version directly from GitHub:

```bash
go install github.com/Sriram-PR/doc-scraper/v2/cmd/doc-scraper@latest
```

This installs the `doc-scraper` binary to your `GOPATH/bin` directory (usually `~/go/bin` or `%USERPROFILE%\go\bin`). Make sure this directory is in your `PATH`.

## Clone and Build

1. **Clone the repository:**

   ```bash
   git clone https://github.com/Sriram-PR/doc-scraper.git
   cd doc-scraper
   ```

2. **Download Dependencies:**

   ```bash
   go mod download
   ```

3. **Build the Binary:**

   ```bash
   make build
   # or: go build -o doc-scraper ./cmd/doc-scraper
   ```

   This creates an executable named `doc-scraper` in the project root.

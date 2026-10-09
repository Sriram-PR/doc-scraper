---
title: Quick start
description: Crawl a small docs site into Markdown in under a minute.
sidebar:
  order: 2
---

Create a minimal `config.yaml` in the project root:

```yaml
output_base_dir: "./crawled_docs"
state_dir: "./crawler_state"
enable_jsonl_output: true
sites:
  rust_cli_book:
    start_urls:
      - "https://rust-cli.github.io/book/index.html"
    allowed_domain: "rust-cli.github.io"
    allowed_path_prefix: "/book/"
    content_selector: "#content, main"
    max_depth: 2          # seed plus one level; set 0 for the whole book
```

Run the crawl:

```bash
./doc-scraper crawl -site rust_cli_book -loglevel info
```

The Markdown, plus `pages.jsonl`, `llms.txt`, and `llms-full.txt`, lands under `./crawled_docs/rust_cli_book/` (output is organized by site key). A small book like this finishes in a few seconds; large sites can take minutes, so start with a low `max_depth` to gauge size before removing the bound.

## Next steps

- [Add your own site](/doc-scraper/getting-started/add-a-site/) with `doc-scraper add <url>`, which detects the framework and drafts the config entry for you.
- See every option in the [configuration reference](/doc-scraper/reference/configuration/).

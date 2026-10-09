---
title: mdBook
description: "Crawl an mdBook book with doc-scraper: detection signals, a real add run, and scope without a sitemap."
sidebar:
  order: 7
---

## How doc-scraper recognizes it

doc-scraper has no generator-tag signature for mdBook, so detection is structural: `#mdbook-content`, `nav#mdbook-sidebar`, or `#mdbook-page-wrapper`. A structural match is reported as medium confidence, and the selector is still validated against the page before it is used.

Content selector: `#mdbook-content main, main`.

The [quick start](/doc-scraper/getting-started/quick-start/) crawls an mdBook (the Rust CLI book).

## Add a site

```text
$ doc-scraper add -config config.yaml -dry-run https://rust-lang.github.io/mdBook/
Probing https://rust-lang.github.io/mdBook/ ...
Detected: mdbook via dom, confidence medium

Drafted entry:

  rust_lang_docs:
    start_urls:
      - https://rust-lang.github.io/mdBook/
    allowed_domain: rust-lang.github.io
    allowed_path_prefix: /mdBook/
    content_selector: '#mdbook-content main, main'
    max_depth: 5

  # content_selector: mdbook detected via dom (medium confidence), validated on the fetched page
  # allowed_path_prefix: /mdBook/ from the URL path (no sitemap to verify against)

Preview of the fetched page:
  1905 chars of markdown, 44% of page text, code blocks 0/0, 3 headings

  | # [Introduction](\#introduction)
  | 
  | Version: 0.5.4
  ...

Dry run: nothing written.
```

Output from doc-scraper 2.10.1 (built from `main`) against <https://rust-lang.github.io/mdBook/>, 2026-10-10. Exit code 2.

## Crawl it

```yaml
sites:
  rust_lang_docs:
    start_urls:
      - https://rust-lang.github.io/mdBook/
    allowed_domain: rust-lang.github.io
    allowed_path_prefix: /mdBook/
    content_selector: '#mdbook-content main, main'
    max_depth: 5
```

```bash
doc-scraper crawl -site rust_lang_docs
```

## Caveats

- **No sitemap.** mdBook does not generate one (this site returns 404 for `/mdBook/sitemap.xml`), so `add` takes the prefix from the URL path and the drafted `max_depth: 5` is a default, not measured. Books are small and flat, so `max_depth: 0` (unlimited) inside the prefix is usually fine.
- The site key is derived from the host, so a book on `rust-lang.github.io` becomes `rust_lang_docs`. Pass `-site mdbook` to choose your own.

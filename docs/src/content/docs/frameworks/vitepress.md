---
title: VitePress
description: "Crawl a VitePress site with doc-scraper: detection signals, a real add run, and widening the scope."
sidebar:
  order: 5
---

## How doc-scraper recognizes it

VitePress sets `<meta name="generator" content="VitePress vX">`, which alone is enough for a high-confidence match. Without it, the `.vp-doc` class or the `#VPContent` id match on structure.

Content selector: `.vp-doc, main.main, #VPContent`.

## Add a site

```text
$ doc-scraper add -config config.yaml -dry-run https://vitepress.dev/guide/what-is-vitepress
Probing https://vitepress.dev/guide/what-is-vitepress ...
Detected: vitepress (vitepress v2.0.0-alpha.20) via generator, confidence high
Corpus:   ~16 pages (sitemap)

Drafted entry:

  vitepress_docs:
    start_urls:
      - https://vitepress.dev/guide/what-is-vitepress
    allowed_domain: vitepress.dev
    allowed_path_prefix: /guide/
    content_selector: '.vp-doc, main.main, #VPContent'
    max_depth: 3

  # content_selector: vitepress detected via generator (high confidence), validated on the fetched page
  # allowed_path_prefix: /guide/ covers 16 of 272 sitemap URLs
  # max_depth: 3 from sitemap path depth under the prefix

Preview of the fetched page:
  5497 chars of markdown, 87% of page text, code blocks 0/0, 5 headings

  | # What is VitePress? [​](\#what-is-vitepress)
  | 
  | VitePress is a [Static Site Generator](https://en.wikipedia.org/wiki/Static_site_generator) (SSG) designed for building fast, content-centric websites. In a nutshell, VitePress takes your source content written in [Markdown](https://en.wikipedia.org/wiki/Markdown), applies a theme to it, and generates static HTML pages that can be easily deployed anywhere.
  ...

Dry run: nothing written.
```

Output from doc-scraper 2.10.1 (built from `main`) against <https://vitepress.dev/guide/what-is-vitepress>, 2026-10-10. Exit code 2.

## Crawl it

```yaml
sites:
  vitepress_docs:
    start_urls:
      - https://vitepress.dev/guide/what-is-vitepress
    allowed_domain: vitepress.dev
    allowed_path_prefix: /guide/
    content_selector: '.vp-doc, main.main, #VPContent'
    max_depth: 3
```

```bash
doc-scraper crawl -site vitepress_docs
```

## Caveats

- **The drafted scope is the section you started in.** Starting from `/guide/` drafts `allowed_path_prefix: /guide/`, which covers 16 of the 272 sitemap URLs and leaves out `/reference/` (17 pages). To crawl both, set the prefix to `/` and exclude the seven translation directories (`/es/`, `/ja/`, `/zh/`, ...), which hold 238 of the sitemap URLs:

  ```yaml
  allowed_path_prefix: /
  disallowed_path_patterns:
    - '^/[a-z]{2}(-[a-z]{2})?/'
  ```

  That pattern is matched against each URL's path and leaves the 34 English pages.

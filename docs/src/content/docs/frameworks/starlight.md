---
title: Starlight
description: "Crawl an Astro Starlight site with doc-scraper: detection signals, a real add run, and locale scope."
sidebar:
  order: 3
---

## How doc-scraper recognizes it

Starlight sets `<meta name="generator" content="Starlight vX">` (alongside Astro's own generator tag), which alone is enough for a high-confidence match. Without it, the `.sl-markdown-content` class, the `<starlight-toc>` element, or `#starlight__sidebar` match on structure; the generic `astro` generator and `/_astro/` asset paths add weak support.

Content selector: `main[data-pagefind-body] .sl-markdown-content, .sl-markdown-content, main[data-pagefind-body]`.

This documentation site is built with Starlight, and CI checks on every change that `doc-scraper add` detects it as Starlight with high confidence.

## Add a site

```text
$ doc-scraper add -config config.yaml -dry-run https://starlight.astro.build/getting-started/
Probing https://starlight.astro.build/getting-started/ ...
Detected: starlight (starlight v0.42.6) via generator, confidence high
Corpus:   ~612 pages (sitemap)

Drafted entry:

  starlight_docs:
    start_urls:
      - https://starlight.astro.build/getting-started/
    allowed_domain: starlight.astro.build
    allowed_path_prefix: /
    content_selector: main[data-pagefind-body] .sl-markdown-content, .sl-markdown-content, main[data-pagefind-body]
    max_depth: 4

  # content_selector: starlight detected via generator (high confidence), validated on the fetched page
  # allowed_path_prefix: / covers 612 of 612 sitemap URLs
  # max_depth: 4 from sitemap path depth under the prefix

Preview of the fetched page:
  4609 chars of markdown, 54% of page text, code blocks 9/9, 7 headings
  ...

Dry run: nothing written.
```

Output from doc-scraper 2.10.1 (built from `main`) against <https://starlight.astro.build/getting-started/>, 2026-10-10. Exit code 2.

## Crawl it

```yaml
sites:
  starlight_docs:
    start_urls:
      - https://starlight.astro.build/getting-started/
    allowed_domain: starlight.astro.build
    allowed_path_prefix: /
    content_selector: main[data-pagefind-body] .sl-markdown-content, .sl-markdown-content, main[data-pagefind-body]
    max_depth: 4
    disallowed_path_patterns:
      - '^/[a-z]{2}(-[a-z]{2})?/'
```

```bash
doc-scraper crawl -site starlight_docs
```

## Caveats

- **Translations sit inside the drafted prefix.** On starlight.astro.build, 576 of the 612 sitemap URLs are translations under 16 locale directories (`/de/`, `/pt-br/`, `/zh-cn/`, ...), and the drafted prefix `/` includes them all. `add` does not propose excluding locale trees nested inside the prefix, so add a pattern yourself, as in the entry above; it leaves the 36 English pages.
- Starlight sites hosted on GitHub project pages (`user.github.io/project/`) write their sitemap under the project path; `add` looks for the sitemap at the host root, so it derives the scope from the URL path there instead.

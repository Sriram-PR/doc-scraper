---
title: Docusaurus
description: "Crawl a Docusaurus site with doc-scraper: detection signals, a real add run, and versioned-docs scope."
sidebar:
  order: 2
---

## How doc-scraper recognizes it

Docusaurus sets `<meta name="generator" content="Docusaurus vX">`, which alone is enough for a high-confidence match. Without it, the `[data-docusaurus]` attribute or the `.theme-doc-markdown` class match on structure, and the `/assets/js/runtime~main.` bundle adds weak support.

Content selector: `article .theme-doc-markdown, .theme-doc-markdown, main article`.

## Add a site

```text
$ doc-scraper add -config config.yaml -dry-run https://docusaurus.io/docs
Probing https://docusaurus.io/docs ...
Detected: docusaurus (docusaurus v4.0.0) via generator, confidence high
Corpus:   ~1168 pages (sitemap)

Drafted entry:

  docusaurus_docs:
    start_urls:
      - https://docusaurus.io/docs
    allowed_domain: docusaurus.io
    allowed_path_prefix: /docs/
    content_selector: article .theme-doc-markdown, .theme-doc-markdown, main article
    max_depth: 7

  # content_selector: docusaurus detected via generator (high confidence), validated on the fetched page
  # allowed_path_prefix: /docs/ covers 1168 of 1366 sitemap URLs
  # max_depth: 7 from sitemap path depth under the prefix

Preview of the fetched page:
  13070 chars of markdown, 86% of page text, code blocks 2/2, 17 headings

  | # Introduction
  | 
  | ⚡️ Docusaurus will help you ship a **beautiful documentation site in no time**.
  ...

Dry run: nothing written.
```

Output from doc-scraper 2.10.1 (built from `main`) against <https://docusaurus.io/docs>, 2026-10-10. Exit code 2.

## Crawl it

```yaml
sites:
  docusaurus_docs:
    start_urls:
      - https://docusaurus.io/docs
    allowed_domain: docusaurus.io
    allowed_path_prefix: /docs/
    content_selector: article .theme-doc-markdown, .theme-doc-markdown, main article
    max_depth: 7
    disallowed_path_patterns:
      - '^/docs/([0-9]|next/)'
```

```bash
doc-scraper crawl -site docusaurus_docs
```

## Caveats

- **Versioned docs sit inside `/docs/`.** On docusaurus.io, 980 of the 1168 sitemap URLs in the drafted scope belong to older versions (`/docs/2.x/` through `/docs/3.9.2/`) and 94 to the unreleased `/docs/next/`; only 94 are the current docs. `add` does not propose excluding version trees that are nested inside the drafted prefix, so add the pattern yourself, as in the entry above. It is matched against each URL's path.
- The drafted `max_depth: 7` comes from the deepest sitemap path under the prefix, including the versioned trees.

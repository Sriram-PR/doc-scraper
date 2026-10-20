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
Corpus:   ~105 pages (sitemap)

Drafted entry:

  docusaurus_docs:
    start_urls:
      - https://docusaurus.io/docs
    allowed_domain: docusaurus.io
    allowed_path_prefix: /docs/
    content_selector: article .theme-doc-markdown, .theme-doc-markdown, main article
    disallowed_path_patterns:
      - ^/docs/v?\d+(?:\.\d+)*(?:\.x)?/
      - ^/docs/next/
    max_depth: 6

  # content_selector: docusaurus detected via generator (high confidence), validated on the fetched page
  # allowed_path_prefix: /docs/ covers 105 of 1361 sitemap URLs, not counting 1058 in excluded version/locale trees
  # max_depth: 6 from sitemap path depth under the prefix
  # disallowed_path_patterns: sibling version/locale trees observed in the sitemap
  # disallowed_path_patterns: 11 version trees under /docs/ generalized; the pattern matches none of the 303 kept sitemap URLs

Preview of the fetched page:
  13003 chars of markdown, 86% of page text, code blocks 2/2, 17 headings

  | # Introduction
  | 
  | ⚡️ Docusaurus will help you ship a **beautiful documentation site in no time**.
  ...

WARN: other doc versions share this scope (/docs/2.x/, /docs/3.0.1/, /docs/3.1.1/, /docs/3.2.1/, /docs/3.3.2/, /docs/3.4.0/, /docs/3.5.2/, /docs/3.6.3/, /docs/3.7.0/, /docs/3.8.1/, /docs/3.9.2/, /docs/next/); drafted disallowed_path_patterns exclude them

Dry run: nothing written.
```

Output from doc-scraper 2.20.0 against <https://docusaurus.io/docs>, 2026-10-20. Exit code 2.

## Crawl it

```yaml
sites:
  docusaurus_docs:
    start_urls:
      - https://docusaurus.io/docs
    allowed_domain: docusaurus.io
    allowed_path_prefix: /docs/
    content_selector: article .theme-doc-markdown, .theme-doc-markdown, main article
    disallowed_path_patterns:
      - ^/docs/v?\d+(?:\.\d+)*(?:\.x)?/
      - ^/docs/next/
    max_depth: 6
```

```bash
doc-scraper crawl -site docusaurus_docs
```

## Caveats

- **Versioned docs sit inside `/docs/`.** On docusaurus.io, 1058 of the 1163 sitemap URLs under `/docs/` belong to older versions (`/docs/2.x/` through `/docs/3.9.2/`) and the unreleased `/docs/next/`. `add` excludes the version trees whose pages mirror the current docs, which leaves 105 pages. The numbered versions share one pattern, `^/docs/v?\d+(?:\.\d+)*(?:\.x)?/`, so a later release is excluded too; `/docs/next/` gets its own line. Patterns are matched against each URL's path.

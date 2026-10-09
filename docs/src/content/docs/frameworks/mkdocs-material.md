---
title: Material for MkDocs
description: "Crawl a Material for MkDocs site with doc-scraper: detection signals, a real add run, and GitHub Pages scope."
sidebar:
  order: 6
---

## How doc-scraper recognizes it

Material for MkDocs sets a generator tag naming `mkdocs-material` (or `zensical`), which alone is enough for a high-confidence match. Without it, `[data-md-component]`, `[data-md-color-scheme]`, or the `.md-content` class match on structure, and a generic `mkdocs` generator adds weak support. Plain MkDocs and its ReadTheDocs theme are detected separately as `mkdocs` and `mkdocs-rtd`; see the [overview](/doc-scraper/frameworks/overview/#detected-frameworks).

Content selector: `article.md-content__inner, .md-content article, .md-content`.

## Add a site

```text
$ doc-scraper add -config config.yaml -dry-run https://squidfunk.github.io/mkdocs-material/getting-started/
Probing https://squidfunk.github.io/mkdocs-material/getting-started/ ...
Detected: mkdocs-material (mkdocs-1.6.1, mkdocs-material-9.7.0+insiders-4.53.18) via generator, confidence high

Drafted entry:

  squidfunk_docs:
    start_urls:
      - https://squidfunk.github.io/mkdocs-material/getting-started/
    allowed_domain: squidfunk.github.io
    allowed_path_prefix: /mkdocs-material/
    content_selector: article.md-content__inner, .md-content article, .md-content
    max_depth: 5

  # content_selector: mkdocs-material detected via generator (high confidence), validated on the fetched page
  # allowed_path_prefix: /mkdocs-material/ from the URL path (no sitemap to verify against)

Preview of the fetched page:
  6349 chars of markdown, 52% of page text, code blocks 10/10, 5 headings
  ...

Dry run: nothing written.
```

Output from doc-scraper 2.10.1 (built from `main`) against <https://squidfunk.github.io/mkdocs-material/getting-started/>, 2026-10-10. Exit code 2.

## Crawl it

```yaml
sites:
  squidfunk_docs:
    start_urls:
      - https://squidfunk.github.io/mkdocs-material/getting-started/
    allowed_domain: squidfunk.github.io
    allowed_path_prefix: /mkdocs-material/
    content_selector: article.md-content__inner, .md-content article, .md-content
    max_depth: 5
```

```bash
doc-scraper crawl -site squidfunk_docs
```

## Caveats

- **GitHub project pages: no sitemap found.** This site is served from `squidfunk.github.io/mkdocs-material/`, and its sitemap lives at `/mkdocs-material/sitemap.xml`. `add` looks for the sitemap at the host root, so it reports "no sitemap to verify against" and takes the prefix from the URL path. The drafted `max_depth: 5` is a default, not measured.
- The site key is derived from the host, so this one is `squidfunk_docs`. Pass `-site mkdocs_material` to choose your own.

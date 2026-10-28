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
Detected: starlight (starlight v0.42.7) via generator, confidence high
Corpus:   ~36 pages (sitemap)

Drafted entry:

  starlight_docs:
    start_urls:
      - https://starlight.astro.build/getting-started/
    allowed_domain: starlight.astro.build
    allowed_path_prefix: /
    content_selector: main[data-pagefind-body] .sl-markdown-content, .sl-markdown-content, main[data-pagefind-body]
    disallowed_path_patterns:
      - ^/(?:ar|bn|cs|da|de|el|en|es|fa|fi|fr|he|hi|hu|id|it|ja|ko|nl|no|pl|pt|ro|ru|sk|sv|th|tr|uk|vi|zh)(?:[-_][a-zA-Z]{2,4})?/
    max_depth: 3

  # content_selector: starlight detected via generator (high confidence), validated on the fetched page
  # allowed_path_prefix: / covers 36 of 612 sitemap URLs, not counting 576 in excluded version/locale trees
  # max_depth: 3 from sitemap path depth under the prefix
  # disallowed_path_patterns: sibling version/locale trees observed in the sitemap
  # disallowed_path_patterns: 16 locale trees under / generalized; the pattern matches none of the 36 kept sitemap URLs

Preview of the fetched page:
  4618 chars of markdown, 54% of page text, code blocks 9/9, 7 headings
  ...

Dry run: nothing written.
```

Output from doc-scraper 2.24.0 against <https://starlight.astro.build/getting-started/>, 2026-10-26. Exit code 2.

## Crawl it

```yaml
sites:
  starlight_docs:
    start_urls:
      - https://starlight.astro.build/getting-started/
    allowed_domain: starlight.astro.build
    allowed_path_prefix: /
    content_selector: main[data-pagefind-body] .sl-markdown-content, .sl-markdown-content, main[data-pagefind-body]
    disallowed_path_patterns:
      - ^/(?:ar|bn|cs|da|de|el|en|es|fa|fi|fr|he|hi|hu|id|it|ja|ko|nl|no|pl|pt|ro|ru|sk|sv|th|tr|uk|vi|zh)(?:[-_][a-zA-Z]{2,4})?/
    max_depth: 3
```

```bash
doc-scraper crawl -site starlight_docs
```

## Caveats

- **Translations sit inside the drafted prefix.** On starlight.astro.build, 576 of the 612 sitemap URLs are translations under 16 locale directories (`/de/`, `/pt-br/`, `/zh-cn/`, ...). `add` excludes the locale trees whose pages mirror the English pages, leaving 36. Because there are several, it drafts one pattern covering every locale code it recognizes rather than one line per tree, so a language added later is excluded too.

---
title: Add a site
description: Point doc-scraper add at any docs page and get a validated config entry without writing selectors by hand.
sidebar:
  order: 3
---

`doc-scraper add <url>` turns a docs URL into a site entry in `config.yaml`. It detects the docs framework, picks a content selector, proposes a crawl scope, and shows you one extracted page before anything is written.

```bash
doc-scraper add https://vitepress.dev/guide/what-is-vitepress
```

## What it probes

`add` makes a handful of polite requests: the page itself, `robots.txt`, `llms.txt`, and the sitemap. From those it reports:

- the detected framework and content selector, validated against the fetched page (see [how detection works](/doc-scraper/frameworks/overview/));
- a crawl scope clustered from the sitemap, with page counts as evidence;
- sibling version or locale trees, proposed as exclusions;
- a Markdown preview of the extracted page with code-block fidelity numbers.

Sites whose robots.txt disallows crawling the given path are refused, and robots rules that restrict AI crawlers are surfaced as a warning.

## Example

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
  | VitePress is a [Static Site Generator](https://en.wikipedia.org/wiki/Static_site_generator) (SSG) designed for building fast, content-centric websites. ...
  ...
  | ... (31 more lines)

Dry run: nothing written.
```

Output from doc-scraper 2.10.1 (built from `main`), 2026-10-09. Exit code 2.

## Confirming and writing

Without flags, `add` asks `Add site "<key>" to <config>? [y/N]` and appends the entry only if you answer yes. The rest of the file is preserved byte-for-byte, comments included. If the config file does not exist, it is created.

| Flag | Effect |
|------|--------|
| `-dry-run` | Draft only, never write |
| `-yes` | Write without prompting |
| `-json` | Print the draft as JSON on stdout; human-readable text goes to stderr |
| `-site <key>` | Use this site key instead of the derived one |
| `-selector <css>` | Use this content selector and skip auto-detection |
| `-depth <n>` | Override the proposed `max_depth` |

The full flag list is in the [CLI reference](/doc-scraper/reference/cli/#add).

## Exit codes

| Code | Meaning |
|------|---------|
| `0` | Entry written |
| `1` | Error |
| `2` | Drafted but not written (dry run, or the prompt was declined) |

## From agents and scripts

`add` never waits on a prompt when no terminal is attached. Inspect first, then commit:

```bash
doc-scraper add -dry-run -json https://docs.example.com   # exit 2, draft on stdout
doc-scraper add -yes https://docs.example.com             # exit 0, entry written
```

Without a terminal, plain `add` (no `-yes` or `-dry-run`) exits 1 with an error asking for one of them, and `add -json` prints the draft and exits 2 without writing.

After the entry is written, crawl it with `doc-scraper crawl -site <key>`.

`add` writes only the site entry. To search the crawled site (with `doc-scraper search` or the `search_docs` MCP tool), also set `enable_jsonl_output: true` at the top of `config.yaml`; the search index is built from the JSONL output, which is off by default.

---
title: Output
description: Output layout, Markdown frontmatter, llms.txt, and the JSONL schema.
---

## Layout

Crawled content is saved under the `output_base_dir` defined in the config, organized by site key and preserving the site structure. Keying by site key (rather than domain) keeps two site configs that target the same domain in separate trees:

```
<output_base_dir>/
└── <sanitized_site_key>/            # e.g., flask_docs
    ├── images/                       # Always created; only populated when skip_images: false
    │   ├── image1.png
    │   └── image2.jpg
    ├── index.md                      # Markdown for the root path
    ├── <jsonl_output_filename>       # Unless enable_jsonl_output: false
    ├── llms.txt                      # Manifest of pages (generated from the JSONL)
    ├── llms-full.txt                 # Full content concatenated (generated from the JSONL)
    ├── topic_one/
    │   ├── index.md
    │   └── subtopic_a.md
    └── topic_two.md
```

A fresh (non-`--resume`) crawl never touches the existing corpus while it runs. It builds into `<sanitized_site_key>.staging` under `output_base_dir` and a `<sanitized_site_key>_visited_db.staging` state directory, and swaps them over the live copies only when the crawl completes. If the crawl is interrupted, cancelled, or fails, the previous output, visited DB, crawl history, and search index stay as they were, and `crawl` exits `130` on SIGINT/SIGTERM. `--resume` (and `-incremental`, which implies it) then continues the staged crawl and swaps it in when done; a new fresh crawl discards leftover staging. Resuming a site with no staging works in place on the live output. Crawl history (`get_freshness`/`diff_crawl`) records completed runs only. Staging needs room for a second copy of the site until the swap.

## llms.txt and llms-full.txt

When JSONL output is enabled, the crawler also emits `llms.txt` and `llms-full.txt` following the [llmstxt.org](https://llmstxt.org/) convention. `llms.txt` is a markdown manifest (H1 + summary blockquote + `## Pages` list of every crawled page with title and URL). `llms-full.txt` concatenates the full markdown content of every page, with section separators. Both files are regenerated on every crawl from the JSONL source of truth, so resumed crawls produce a complete updated manifest.

## Markdown files

Each generated Markdown file begins with a YAML frontmatter block carrying page metadata, followed by the converted content:

- **YAML frontmatter** (delimited by `---`) with `title`, `url` (source URL), `crawled_at` (RFC3339 timestamp), `content_hash` (SHA-256 of the content, matching the JSONL record), and `depth`
- Clean content converted from HTML to GitHub-Flavored Markdown, preserving tables
- Relative links to other pages (when within the allowed domain)
- Local image references (if images are enabled)

Example:

```markdown
---
title: 'Authentication'
url: https://docs.example.com/api/auth
crawled_at: "2026-08-09T12:00:00Z"
content_hash: 9f2b...c1a4
depth: 2
---

# Authentication

...page content as Markdown...
```

## JSONL output

The crawler writes one JSON object per line to a JSONL file. This format is designed for ingestion into RAG pipelines and downstream indexers, and it is the stored corpus that search, crawl history, `llms.txt`, and the MCP page tools read. It is on by default:

```yaml
enable_jsonl_output: true             # default; false disables search and history for the site
jsonl_output_filename: "pages.jsonl"  # default
```

The file mixes two record kinds, distinguished by the `record_type` field:

- **`page`** records, one per crawled page.
- A single **`crawl_meta`** record as the final line, holding the crawl-level summary. Resuming rewrites the file to drop any leftover `crawl_meta` record before appending a fresh one at close, so a closed file always contains exactly one `crawl_meta` record.

**`page` record fields** (from `PageJSONL`):

| Field | Description |
|-------|-------------|
| `record_type` | Always `"page"` |
| `url` | Final absolute URL of the page |
| `title` | Page title |
| `content` | Full markdown content |
| `headings` | Array of headings extracted from the page |
| `links` | Array of links found in the content |
| `images` | Array of image URLs found in the content |
| `content_hash` | SHA-256 hash of the content (used for incremental crawling) |
| `crawled_at` | Timestamp of when the page was crawled |
| `depth` | Crawl depth from the start URL |

**`crawl_meta` record fields** (from `CrawlMetaJSONL`):

| Field | Description |
|-------|-------------|
| `record_type` | Always `"crawl_meta"` |
| `site_key` | Site key from the config |
| `allowed_domain` | The crawled domain |
| `crawl_started_at` | Crawl start timestamp |
| `crawl_ended_at` | Crawl end timestamp |
| `total_pages` | Number of pages recorded in this crawl |

The output file is written to each site's output directory. Both the enable flag and filename can be overridden per site.

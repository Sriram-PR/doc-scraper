---
title: Detection benchmark
description: How accurately doc-scraper recognizes docs frameworks across 223 labeled live sites, and how to rerun the benchmark.
---

## What it measures

`tools/detect-bench` runs the same detection that `content_selector: "auto"` and `doc-scraper add` use against a battery of 223 live documentation sites, each labeled with the framework it is built on. 44 of them are labeled `unknown`: sites built with no supported framework, which must **not** be claimed by any signature. The benchmark measures two things: how often a labeled site's framework family is recognized, and whether any unknown site is wrongly claimed (a false positive, which would apply the wrong content selector to every page of a crawl).

Scoring is by family: any Sphinx theme counts as Sphinx, any MkDocs theme as MkDocs, and any JavaScript-shell verdict on a JavaScript-shell site is a hit. A `js-shell` verdict on an unknown-labeled site also counts as correct.

## Results

```text
fetched 222/223 | HIT 222 (100.0%) | UNDER 0 | OVER 0 | CONFUSE 0 | SHELL 0 | fetch-fail 1
```

| Family | Hits | Fetched |
|--------|------|---------|
| `antora` | 4 | 4 |
| `docsy` | 6 | 6 |
| `docusaurus` | 54 | 54 |
| `doxygen` | 4 | 4 |
| `fern` | 3 | 3 |
| `fumadocs` | 2 | 2 |
| `gitbook` | 2 | 2 |
| `godoc` | 1 | 1 |
| `javadoc` | 1 | 1 |
| `just-the-docs` | 1 | 1 |
| `mdbook` | 5 | 5 |
| `mintlify` | 4 | 4 |
| `mkdocs` | 8 | 8 |
| `nextra` | 3 | 3 |
| `readme` | 3 | 3 |
| `rustdoc` | 1 | 1 |
| `shell:docsify` | 1 | 1 |
| `shell:swagger-ui` | 1 | 1 |
| `sphinx` | 24 | 24 |
| `starlight` | 37 | 37 |
| `typedoc` | 1 | 1 |
| `unknown` | 43 | 43 |
| `vitepress` | 11 | 11 |
| `vuepress` | 2 | 2 |

Run on 2026-10-11 with the detector as of doc-scraper commit `9932b12`.

- **HIT**: the detected family matches the label.
- **UNDER**: a labeled framework was not recognized (detection fell back to Readability).
- **OVER**: an unknown-labeled site was claimed by a framework signature (false positive).
- **CONFUSE**: the site was recognized as the wrong framework.
- **SHELL**: a labeled framework site was reported as JavaScript-rendered.
- **fetch-fail**: the site could not be fetched (DNS failure, TLS error, or a non-200 status), so it is not scored.

Every fetched site was recognized correctly, with no false positives. The one fetch failure is ruby-doc.org, which is online but slow enough (about 30 seconds per request) to hit the TLS handshake timeout; it is not scored.

## Why it matters

Recognizing the framework is only half of it. A recognized framework's content selector must still capture at least 200 characters of visible text on the fetched page before it is trusted; otherwise extraction falls back to Readability. So a recognized framework whose selector does not fit the page falls back to Readability instead of producing an empty crawl. See [how detection works](/doc-scraper/frameworks/overview/).

## Run it yourself

From a checkout of the repository:

```bash
go run ./tools/detect-bench
```

It fetches each site once (about 230 requests) and prints every disagreement, the summary line, and the per-family table.

| Flag | Default | Effect |
|------|---------|--------|
| `-v` | `false` | Print every site, not just disagreements |
| `-concurrency` | `8` | Concurrent fetches |
| `-sites` | `tools/detect-bench/sites.json` | Path to the labeled site battery |

Labels drift as sites migrate. When a miss shows a generator tag or markup that contradicts its label, the fix belongs in `sites.json`, not the detector.

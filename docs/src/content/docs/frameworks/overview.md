---
title: How detection works
description: How doc-scraper recognizes a docs framework, validates its content selector, and what it does when nothing matches.
sidebar:
  order: 1
---

Set `content_selector: "auto"` for a site, or run [`doc-scraper add <url>`](/doc-scraper/getting-started/add-a-site/), and doc-scraper recognizes the documentation framework from the page itself and uses that framework's content selector. Navigation, sidebars, and footers stay out of the Markdown without you writing a selector.

```yaml
sites:
  pytorch_docs:
    start_urls:
      - "https://pytorch.org/docs/stable/"
    allowed_domain: "pytorch.org"
    allowed_path_prefix: "/docs/stable/"
    content_selector: "auto"  # Auto-detect framework
    max_depth: 0
```

## Signals

Each framework has a signature of signals, checked in three tiers of decreasing trust:

| Signal | Points | Notes |
|--------|--------|-------|
| `<meta name="generator">` names the framework | 4 | Machine-set, trusted on its own |
| Generic generator (`astro`, `hugo`, `jekyll`, `mkdocs`, `next.js`) | 1 | Adds confidence, never confirms alone |
| Structural DOM marker (attribute, id, class) | 2 | |
| Asset path pattern (script or stylesheet URL) | 1 | Weak corroboration |

A framework needs at least 2 points to match, so an asset path alone never identifies one. The highest score wins; themed variants are listed before their family (Furo before plain Sphinx, Material before plain MkDocs), so a tie resolves to the more specific theme. Confidence is **high** when the generator tag matched or the score is 6 or more, and **medium** otherwise.

## Validation

A recognized framework is not trusted blindly. Its selector must pick an element with at least 200 characters of visible text on the fetched page. If it does not, the result is reported as unvalidated and extraction falls back to Readability instead of producing empty pages.

## Pages that need JavaScript

Client-rendered shells (Docsify, Swagger UI, Redoc, Scalar, Document360, and generic empty-body single-page apps) are recognized and reported as needing JavaScript rendering, rather than silently producing an empty crawl. The generic empty-body check runs only when no framework signature matched, so a nearly empty landing page from a recognized generator is not mistaken for a shell.

## Fallback

If no framework matches, or the matched selector fails validation, doc-scraper uses Mozilla's Readability algorithm to find the main content. This works well on classic server-rendered docs but can drop code blocks on some modern sites, which is why `add` reports code-block fidelity in its preview before you commit a config.

How well this works across real sites is measured by the detection benchmark.

## Detected frameworks

Rows link to a guide where one exists. Selectors are tried left to right; the first that matches is used.

| ID | Framework | Detected by | Content selector |
|----|-----------|-------------|------------------|
| `docusaurus` | [Docusaurus](/doc-scraper/frameworks/docusaurus/) | generator contains `docusaurus`; DOM `[data-docusaurus]`, `.theme-doc-markdown`; assets `/assets/js/runtime~main.` | `article .theme-doc-markdown, .theme-doc-markdown, main article` |
| `vitepress` | [VitePress](/doc-scraper/frameworks/vitepress/) | generator contains `vitepress`; DOM `.vp-doc`, `#VPContent` | `.vp-doc, main.main, #VPContent` |
| `vuepress` | VuePress | generator contains `vuepress`; DOM `[vp-content]` | `[vp-content] #content, .theme-hope-content, [vp-content]` |
| `starlight` | [Starlight (Astro)](/doc-scraper/frameworks/starlight/) | generator contains `starlight`; generic generator `astro`; DOM `.sl-markdown-content`, `starlight-toc`, `#starlight__sidebar`; assets `/_astro/` | `main[data-pagefind-body] .sl-markdown-content, .sl-markdown-content, main[data-pagefind-body]` |
| `nextra` | Nextra | generic generator `next.js`; DOM `.nextra-toc`, `.nextra-sidebar`, `.nextra-breadcrumb`, `.nextra-navbar` | `main[data-pagefind-body="true"], article` |
| `fumadocs` | Fumadocs | DOM `#nd-page`, `#nd-docs-layout` | `article#nd-page .prose, article#nd-page, main[data-layout-main]` |
| `mintlify` | Mintlify | generator contains `mintlify`; DOM `#content-area`; assets `mintcdn.com` | `#content-area .mdx-content, .mdx-content, #content-area` |
| `fern` | Fern | generator contains `buildwithfern.com`; DOM `#fern-docs`, `#fern-header`, `#fern-sidebar`, `main.fern-main` | `.fern-prose, main.fern-main article` |
| `gitbook` | GitBook | generator contains `gitbook (`; DOM `[data-gb-sections]`, `[data-gb-table-of-contents]`, `[data-gb-site-header]` | `main div.contents, main` |
| `mkdocs-material` | [Material for MkDocs](/doc-scraper/frameworks/mkdocs-material/) | generator contains `mkdocs-material`, `material for mkdocs`, `zensical`; generic generator `mkdocs`; DOM `[data-md-component]`, `[data-md-color-scheme]`, `.md-content` | `article.md-content__inner, .md-content article, .md-content` |
| `mkdocs-rtd` | MkDocs, ReadTheDocs theme | generic generator `mkdocs`; DOM `body.wy-body-for-nav`; assets `css/theme_extra.css` | `div[role='main'].document, div.rst-content, .wy-nav-content` |
| `mkdocs` | MkDocs | generator contains `mkdocs-`; DOM `#mkdocs-search-query`, `#mkdocs_search_modal`, `div.col-md-9[role='main']` | `div.col-md-9[role='main'], div[role='main']` |
| `sphinx-furo` | Sphinx, Furo theme | DOM `article#furo-main-content`; assets `furo.css`, `furo.js` | `article#furo-main-content` |
| `sphinx-book` | Sphinx, Book theme | DOM `.sbt-scroll-pixel-helper`; assets `sphinx-book-theme.js`, `sphinx-book-theme.css` | `article.bd-article, main.bd-main` |
| `sphinx-pydata` | Sphinx, PyData theme | DOM `article.bd-article`; assets `pydata-sphinx-theme.js`, `pydata-sphinx-theme.css` | `article.bd-article, main#main-content` |
| `sphinx-rtd` | Sphinx, Read the Docs theme | DOM `body.wy-body-for-nav`, `.rst-content`; assets `_static/` | `.rst-content div[itemprop='articleBody'], .rst-content, div[role='main']` |
| `antora` | Antora | generator contains `antora`; DOM `article.doc`; assets `_/css/site.css`, `_/js/site.js` | `article.doc` |
| `docsy` | Docsy (Hugo) | generic generator `hugo`; DOM `.td-content`, `.td-sidebar` | `div.td-content` |
| `hugo-book` | hugo-book | generic generator `hugo`; DOM `article.book-article`, `.book-menu` | `article.book-article` |
| `geekdoc` | Geekdoc (Hugo) | generic generator `hugo`; DOM `article.gdoc-markdown`, `.gdoc-page` | `article.gdoc-markdown` |
| `just-the-docs` | Just the Docs (Jekyll) | generic generator `jekyll`; DOM `div#main-content.main-content`, `.side-bar`; assets `just-the-docs` | `#main-content main, #main-content` |
| `mdbook` | [mdBook](/doc-scraper/frameworks/mdbook/) | DOM `#mdbook-content`, `nav#mdbook-sidebar`, `#mdbook-page-wrapper` | `#mdbook-content main, main` |
| `rustdoc` | rustdoc | generator contains `rustdoc`; DOM `[data-rustdoc-version]`, `section#main-content.content` | `#main-content` |
| `godoc` | pkg.go.dev | DOM `.Documentation-content`, `[data-test-id="UnitDetails-content"]` | `.Documentation-content, [data-test-id="UnitDetails-content"], article.go-Main-article` |
| `javadoc` | Javadoc | generator contains `javadoc/` | `main[role='main'], main` |
| `doxygen` | Doxygen | generator contains `doxygen`; DOM `div#nav-path.navpath`, `div#titlearea` | `div.contents` |
| `typedoc` | TypeDoc | DOM `.tsd-generator`, `.col-content .tsd-panel` | `.col-content, .tsd-panel` |
| `writerside` | Writerside | DOM `article.article[data-template="article"]`; assets `/writerside/apidoc/` | `article.article` |
| `readme` | ReadMe | DOM `meta[name="readme-deploy"]`, `[data-testid="RDMD"]`, `article.rm-Article` | `[data-testid="RDMD"], div.rm-Markdown.markdown-body, article.rm-Article` |
| `intercom` | Intercom | DOM `div.article.intercom-force-break` | `div.article_body, div.article.intercom-force-break` |
| `docus` | Docus | DOM `.docus-sub-header` | `[data-content-id]` |
| `sphinx` | [Sphinx](/doc-scraper/frameworks/sphinx/) | DOM `div.body`, `.sphinxsidebar`, `div.document`, `a.headerlink`; assets `_static/documentation_options`, `_static/doctools`, `searchindex.js` | `div.body section[id], div.body, article.bd-article, div[role='main'], div.document` |
| `docsify` | Docsify | assets `docsify.min.js` or `docsify@`, or `$docsify` in an inline script | needs JavaScript rendering, reported instead of crawled |
| `swagger-ui` | Swagger UI | `#swagger-ui` plus a Swagger UI asset | needs JavaScript rendering, reported instead of crawled |
| `redoc` | Redoc | a `<redoc>` element or `redoc.standalone.js` | needs JavaScript rendering, reported instead of crawled |
| `scalar` | Scalar | asset `@scalar/api-reference` | needs JavaScript rendering, reported instead of crawled |
| `document360` | Document360 | an empty `d360-article-content` element | needs JavaScript rendering, reported instead of crawled |
| `js-shell` | Any client-rendered page | no signature matched, under 200 characters of body text, and a SPA mount point, `<noscript>` fallback, or scripts on a near-empty body | needs JavaScript rendering, reported instead of crawled |

# Privacy Policy

doc-scraper is a self-hosted tool. It runs entirely on your machine and is designed to keep your data there.

## What doc-scraper collects

Nothing. There is no telemetry, no analytics, no crash reporting, no account system, and no usage tracking of any kind.

## Where your data lives

Everything doc-scraper produces stays on your machine: crawled Markdown and JSONL output in the output directory you configure, and crawl state and the search index in the state directory you configure. Nothing is uploaded anywhere.

## Network requests it makes

The only network requests doc-scraper makes are the ones you ask for:

- Fetching pages, robots.txt files, and sitemaps from the documentation sites listed in your configuration when you run a crawl.
- Fetching a single URL when you (or your AI assistant) invoke the `get_page` tool.
- Downloading images from crawled pages, only if you enable image downloading in your configuration.

Reading, searching, and diffing crawled content (`read_page`, `search_docs`, `list_pages`, `diff_crawl`, `get_freshness`) uses only local files and makes no network requests at all.

## Third parties

doc-scraper sends no data to the project's authors or to any third-party service. The sites you crawl will see ordinary HTTP requests from your machine, identified by doc-scraper's user agent, exactly as if you visited them with a browser.

## Changes

If this policy ever changes, the change will appear in this file's git history in the project repository.

## Contact

Questions: open an issue at https://github.com/Sriram-PR/doc-scraper/issues

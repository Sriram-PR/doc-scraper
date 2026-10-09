---
title: Incremental crawls and watch mode
description: Re-crawl only what changed, and keep sites fresh on a schedule.
sidebar:
  order: 1
---

## Incremental crawling

`crawl -incremental` (which implies `--resume`, and is also what `watch` mode uses) re-fetches every previously-crawled page and re-checks it for changes:

- Change detection is **content-scoped**: it hashes the extracted content-selector region, not the raw page. Churn in the page shell (navigation, analytics, build timestamps, CSRF tokens) outside the content selector does **not** count as a change.
- Pages whose content region is **unchanged** are skipped without re-converting, re-downloading images, or rewriting output.
- Pages whose content region **changed** are fully reprocessed and their output is rewritten.
- A page that now returns an error (e.g. 404) on re-crawl leaves its previously-crawled output **as-is**; nothing is pruned.

Because there is no conditional-request support yet, incremental mode still performs the HTTP fetch for each known page; the savings come from skipping the downstream processing of unchanged pages.

## Watch mode

Watch mode enables scheduled periodic re-crawling of documentation sites. The scheduler tracks the last run time for each site and automatically triggers crawls when the configured interval has elapsed.

### Usage

```bash
# Watch a single site with 24-hour interval
./doc-scraper watch -site pytorch_docs -interval 24h

# Watch multiple sites
./doc-scraper watch -sites pytorch_docs,tensorflow_docs -interval 12h

# Watch all configured sites weekly
./doc-scraper watch --all-sites -interval 7d
```

### Interval Format

The interval supports standard Go duration format plus day units:
- `30m` - 30 minutes
- `1h` - 1 hour
- `24h` - 24 hours
- `7d` - 7 days
- `1d12h` - 1 day and 12 hours

### State Persistence

Watch mode persists state to `<state_dir>/watch_state.json`, tracking:
- Last run time for each site
- Success/failure status
- Pages processed
- Error messages (if any)

This allows the scheduler to resume correctly after restarts, only running sites when their interval has elapsed.

### Example Output

First run of `watch -site rust_cli_book -interval 24h` (timestamps and the `component=watch` attribute trimmed from each line):

```
level=INFO msg="Starting watch mode for 1 sites with interval 24h0m0s"
level=INFO msg="Watch schedule:"
level=INFO msg="  rust_cli_book: never run, will run immediately"
level=INFO msg="Running crawl for 1 due sites: [rust_cli_book]"
...
level=INFO msg="  rust_cli_book: SUCCESS - 18 pages in 843.519503ms"
...
level=INFO msg="Next crawl: rust_cli_book in 24h0m0s (at 16:26:11)"
```

After a restart, the schedule picks up from the saved state:

```
level=INFO msg="Starting watch mode for 1 sites with interval 24h0m0s"
level=INFO msg="Watch schedule:"
level=INFO msg="  rust_cli_book: last run 2026-10-09T16:26:11+11:00 (success, 18 pages), next run 2026-10-10T16:26:11+11:00"
level=INFO msg="Next crawl: rust_cli_book in 23h59m56s (at 16:26:11)"
```

Output from doc-scraper 2.10.1 (built from `main`), 2026-10-09.

### Graceful Shutdown

Watch mode handles SIGINT/SIGTERM gracefully: it stops the scheduler and cancels any in-progress crawl, letting the crawler flush its BadgerDB state and partial output first, so the interrupted crawl resumes cleanly on the next run. Watch exits `0` on a signal; scheduled re-crawls always resume, so an interrupted one is continued on the next run, and only completed re-crawls are recorded in crawl history.

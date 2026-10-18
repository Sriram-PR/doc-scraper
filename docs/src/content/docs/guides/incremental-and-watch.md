---
title: Incremental crawls and watch mode
description: Re-crawl only what changed, and keep sites fresh on a schedule.
sidebar:
  order: 1
---

## Incremental crawling

`crawl -incremental` (which implies `--resume`, and is also what `watch` mode uses) re-fetches every previously-crawled page and re-checks it for changes:

- Change detection is **content-scoped**: it hashes the extracted content-selector region, not the raw page. Churn in the page shell (navigation, analytics, build timestamps, CSRF tokens) outside the content selector does **not** count as a change.
- Pages whose content region is **unchanged** are skipped without re-converting, re-downloading images, or rewriting output. An unchanged page whose Markdown file is missing from the output is saved again.
- Pages whose content region **changed** are fully reprocessed and their output is rewritten.
- After an upgrade that changes the Markdown conversion, the first incremental crawl reprocesses every page once, since the output for the same HTML has changed.
- Unchanged pages still have their links followed, so a page newly linked from the navigation is found.
- A page that now returns 404 or 410 is removed: its Markdown file, its JSONL record and its search entries go, and `diff_crawl` lists it as removed. Any other error (a 403 or a server error, for example) leaves its previously-crawled output **as-is**. A run that fails leaves everything in place.
- If every page fails and none checks out unchanged (the site is down, for example), the run fails and exits `1`. The existing output is kept, and the run is not recorded in crawl history, so `get_freshness` still reports the last good crawl.

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

Days must be whole (`1.5d` is rejected; write `1d12h`), and the interval must be positive: `0`, `0s`, and negative values exit with an error. The same rules apply to `interval` in `run` task specs.

### State Persistence

Watch mode persists state to `<state_dir>/watch_state.json`, tracking:
- Last run time for each site
- Success/failure status
- Pages saved, re-checked but unchanged, and failed in the last run
- Error messages (if any)

This allows the scheduler to resume correctly after restarts, only running sites when their interval has elapsed. A failed run (the site was unreachable, or every page failed) is retried after 15 minutes instead of a full interval, or after the interval if that is shorter.

### Example Output

First run of `watch -site rust_cli_book -interval 24h` (timestamps and the `component=watch` attribute trimmed from each line):

```
level=INFO msg="Starting watch mode for 1 sites with interval 24h0m0s"
level=INFO msg="Watch schedule:"
level=INFO msg="  rust_cli_book: never run, will run immediately"
level=INFO msg="Running crawl for 1 due sites: [rust_cli_book]"
...
level=INFO msg="  rust_cli_book: SUCCESS - 17 pages saved, 0 unchanged, 0 failed, in 3.374338083s"
...
level=INFO msg="Next crawl: rust_cli_book in 24h0m0s (at 14:54:01)"
```

After a restart, the schedule picks up from the saved state:

```
level=INFO msg="Starting watch mode for 1 sites with interval 24h0m0s"
level=INFO msg="Watch schedule:"
level=INFO msg="  rust_cli_book: last run 2026-10-15T14:54:01+11:00 (success, 17 pages saved, 0 unchanged, 0 failed), next run 2026-10-16T14:54:01+11:00"
level=INFO msg="Next crawl: rust_cli_book in 23h59m59s (at 14:54:01)"
```

A later run with nothing changed on the site re-checks every page and reprocesses none:

```
level=INFO msg="  rust_cli_book: SUCCESS - 0 pages saved, 17 unchanged, 0 failed, in 168.335008ms"
```

Output from doc-scraper 2.13.0, 2026-10-15.

### Graceful Shutdown

Watch mode handles SIGINT/SIGTERM gracefully: it stops the scheduler and cancels any in-progress crawl, letting the crawler flush its BadgerDB state and partial output first, so the interrupted crawl resumes cleanly on the next run. Watch exits `0` on a signal. A crawl interrupted this way is not recorded in the watch state, so the site is due as soon as watch starts again, and the crawl continues where it stopped. Only completed re-crawls are recorded in crawl history.

# Contributing to doc-scraper

Thanks for helping. Issues labeled [`good first issue`](https://github.com/Sriram-PR/doc-scraper/labels/good%20first%20issue) are small and self-contained; [`help wanted`](https://github.com/Sriram-PR/doc-scraper/labels/help%20wanted) issues are larger and usually need a short design discussion on the issue first.

Everyone taking part follows the [Code of Conduct](CODE_OF_CONDUCT.md). Security problems go through [SECURITY.md](SECURITY.md), not public issues.

## Before you start

- **Claim the issue.** Comment on it before you start so two people don't do the same work. Don't open a PR for an issue someone else has claimed.
- **Untracked work needs an issue first.** Open one and agree on the approach before writing code. PRs that aren't tied to an issue, including unsolicited cosmetic changes, are closed.
- **One open PR at a time** until your first PR is merged.

## AI-assisted contributions

AI tools and coding agents are welcome. The one thing we ask is that a person reviewed the work before it reaches us.

- **Review and test what you submit.** You should understand the change well enough to answer questions about it and to fix it when review asks for changes. Run `make check` yourself before opening the PR.
- **Mention it in the PR.** Note whether you used AI tools and roughly how (the PR template has a field for this). It helps reviewers know where to look; it doesn't count against you.
- **Keep a human in the loop.** Please don't open PRs straight from an agent, or paste a model's output into issues and review replies without reading and editing it first. Using AI to polish your wording is fine.

PRs that clearly weren't reviewed (code that doesn't build, calls to functions that don't exist, changes unrelated to the issue) will be closed without a detailed review. Repeatedly submitting unreviewed work will get you blocked from the repository.

## Setup

You need Go 1.26 or later and [golangci-lint](https://golangci-lint.run/) v2.12.2 or later.

```bash
git clone https://github.com/Sriram-PR/doc-scraper.git
cd doc-scraper
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2
make check
```

Install golangci-lint through the `/v2` module path as shown. `.../golangci-lint/cmd/golangci-lint@latest` (without `/v2`) resolves to the old v1 line, and `make check` then fails with `unknown flag: --diff`.

`make check` runs the format check, lint, and tests. CI additionally runs the tests with `-race` on Linux, macOS, and Windows, checks that `go.mod` is tidy, and runs `govulncheck`. Other targets: `make test`, `make lint`, `make fmt`, `make build`, `make test-cover`.

## Project layout

| Path | What lives there |
| --- | --- |
| `cmd/doc-scraper` | CLI entry point and subcommands (`crawl`, `watch`, `run`, `config`, `mcp-server`, `search`, `add`) |
| `pkg/crawler` | Crawl loop, workers, staging and swap of fresh crawls, JSONL / `llms.txt` output, search-chunk indexing |
| `pkg/fetch` | HTTP client, retries, rate limiting, per-host concurrency, robots.txt, SSRF-safe dialing |
| `pkg/process` | HTML to Markdown, link rewriting, image downloads, heading extraction |
| `pkg/detect` | Docs-framework detection and content-selector choice |
| `pkg/discover` | Site probing behind `add`: sitemap, robots.txt, llms.txt, scope inference |
| `pkg/sitemap` | Sitemap fetching and URL enqueueing during a crawl |
| `pkg/mcp` | MCP server, tool handlers, background crawl jobs |
| `pkg/storage` | BadgerDB visited-page store; `pkg/storage/index` is the SQLite crawl history and FTS5 search index |
| `pkg/chunk` | Splits page Markdown into heading-anchored chunks for search |
| `pkg/config` | Config types, defaults and validation, unknown-key warnings, and the config writer used by `add` |
| `pkg/orchestrate`, `pkg/watch` | Parallel multi-site crawls; scheduled re-crawls |
| `pkg/parse`, `pkg/queue`, `pkg/models`, `pkg/utils`, `pkg/log`, `pkg/taskspec`, `pkg/version` | Shared helpers and types |
| `tools/detect-bench` | Live benchmark for `pkg/detect` against labeled real sites |

## Tests

Every change needs tests, and a bug fix needs a test that fails without the fix. Write that test first and watch it fail; it proves the test reproduces the bug.

- **No network.** Tests must not reach the internet. Serve fixtures from `httptest.NewServer` and write files under `t.TempDir()`. CI runs the Linux tests in a network namespace with only loopback, so a test that dials out fails there even if it passes on your machine.
- **testify for new tests.** Use `require` for preconditions that make the rest of the test meaningless, `assert` for the checks themselves. Some older files still use plain `t.Errorf`; convert a file only when you are already changing it.
- **No sleeping to synchronize.** Wait on a channel, a `sync.WaitGroup`, or a deadline-bounded poll instead of `time.Sleep` followed by an assertion. Sleeps that pass locally fail on a loaded CI runner.
- **Race-clean on every OS.** CI runs `go test -race ./...` on Linux, macOS, and Windows. Windows differs most: it refuses to rename over an open file, and its coarser clock makes timestamps tie.
- **Smoke test.** `cmd/doc-scraper/smoke_test.go` builds the real binary and checks the MCP stdio handshake. If you add, rename, or remove an MCP tool, update the tool list in `mcp-server -h` (`cmd/doc-scraper/mcp.go`) and the tool table in `README.md`; the smoke test fails when the three disagree. `go test -short` skips it.
- **Fuzzing.** Parsers of untrusted input have fuzz targets (`Fuzz*` in `pkg/chunk`, `pkg/config`, `pkg/detect`, `pkg/discover`). CI fuzzes each one weekly; to fuzz locally, `go test ./pkg/discover -run='^$' -fuzz='^FuzzParseRobotsLines$' -fuzztime=30s`.

### Framework detection changes

`pkg/detect` changes need a fixture-based unit test, and should also be checked against real sites:

```bash
go run ./tools/detect-bench
```

It fetches about 230 labeled documentation sites and prints a per-family scorecard plus every disagreement (`-v` prints every site). Expect no family to regress and no false positives. When a "miss" shows a generator meta tag that contradicts the label, the site migrated platforms: fix `tools/detect-bench/sites.json`, not the detector.

## Pull requests

- One issue per PR, linked in the description (`Fixes #123`).
- Run `make check` before pushing. CI must pass on all three operating systems.
- PRs are squash-merged, so your branch's commit history does not matter. Write the PR title as the final commit message: one line describing the user-visible change, for example `Reject a -sites list with no site keys instead of panicking`.
- Update `README.md` when you change a flag, config key, MCP tool, or output format.

## License

doc-scraper is licensed under [Apache-2.0](LICENSE). Under section 5 of the license, contributions you submit are licensed under the same terms.

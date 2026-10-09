---
title: Task specs
description: Drive crawl and watch from a JSON task spec on stdin.
sidebar:
  order: 3
---

The `run` command reads a single JSON object from stdin and dispatches the equivalent `crawl` or `watch`. It is meant for orchestration agents that would rather build a JSON payload than assemble shell flags. Unknown fields are rejected so typos surface immediately; logs go to stderr and the exit code matches the equivalent flag-driven subcommand. Validation is stricter than the flags: a spec that sets more than one of `site`, `sites`, and `all_sites` is rejected with exit code 1, whereas `crawl -site a -sites b` only warns and uses `-sites`.

```json
{
  "command":     "crawl" | "watch",   // required
  "config":      "config.yaml",        // optional, defaults to config.yaml
  "site":        "site_key",           // exactly one of site | sites | all_sites
  "sites":       ["a", "b"],
  "all_sites":   true,
  "resume":      false,                // crawl only
  "incremental": false,                // crawl only (implies resume)
  "full":        false,                // crawl only (mutually exclusive with incremental)
  "interval":    "24h",                // watch only, defaults to 24h
  "loglevel":    "info",               // defaults to info
  "json_logs":   false,                // emit slog records as JSON on stderr
  "pprof":       ""                    // crawl only, e.g. localhost:6060
}
```

Examples:

```bash
echo '{"command":"crawl","site":"pytorch_docs"}' | doc-scraper run
echo '{"command":"crawl","all_sites":true,"incremental":true,"json_logs":true}' | doc-scraper run
echo '{"command":"watch","sites":["pytorch_docs","tensorflow_docs"],"interval":"6h"}' | doc-scraper run
```

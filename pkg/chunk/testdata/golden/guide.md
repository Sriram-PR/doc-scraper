Retry Guide for the HTTP client. This page explains how the client retries failed requests, how backoff is computed, and which settings control it. Read it before you change maxRetries or the backoff ceiling in production.

# Retries

The client retries idempotent requests that fail with a connection error or a 5xx status. Each attempt waits longer than the last, and the wait is capped so a long outage doesn't stall a worker forever. Set `maxRetries` to 0 to turn retries off entirely.

## Concepts

### Attempts

An attempt is one request on the wire. The first attempt is not a retry, so maxRetries of 3 means up to four attempts in total. Attempts share one deadline: if the caller's context expires, no further attempt is made even when the retry budget isn't spent.

### Backoff

Backoff doubles after each failed attempt, starting at `baseDelay` and stopping at `maxDelay`. Full jitter is applied on top, so concurrent clients that failed together don't retry in lockstep. getHTTPResponse returns the last error once the budget is spent.

## Policies

A policy bundles the retry settings for one kind of caller. The client ships two of them, described below, and you can register your own at startup. Policies are looked up by name on each request.

### Default policy

Used when a request names no policy. It retries three times with full jitter and a thirty second ceiling, which suits most server-to-server traffic.

##	Tabbed heading

A tab after the hashes still makes a heading.

####### Seven hashes is plain text, not a heading

###### Level six is the deepest heading

## Configuration

Settings live under the `retry` key. The XMLParser and JSONDecoder share the same settings, and snake_case keys such as max_retries are accepted as aliases for the camelCase names.

| Key | Default | Meaning |
| --- | --- | --- |
| maxRetries | 3 | Retries after the first attempt |
| baseDelay | 100ms | First wait |
| maxDelay | 30s | Upper bound on any wait |

## Example

A client that gives up quickly, for interactive tools where a fast failure beats a slow success. Use it from a command-line program or an editor plugin.

## Example

A client for batch jobs that can afford to wait out a short outage. Keep the deadline on the context so the job still finishes in bounded time.

## Usage ##

Call `client.Do(req)` as usual; retries are transparent. Responses carry the attempt count in `resp.Attempts` for logging and metrics dashboards.

## Version 2.x: what's new?

Retry-After headers are honored, and the iPhone and macOS clients now share the same defaults as Linux.

## foo##

A heading whose trailing hashes aren't preceded by a space keeps them.

#### Deep heading after a level jump

Short.

#

An empty heading line.

## Résumé et caractères spéciaux

Unicode headings slug to lowercase letters and digits; punctuation is dropped.

## See also

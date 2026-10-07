# Security Policy

## Supported versions

Only the [latest release](https://github.com/Sriram-PR/doc-scraper/releases/latest) receives security fixes. Upgrade before reporting, and check that the problem still happens there.

## Reporting a vulnerability

Report privately through GitHub: open the [**Security and quality** tab](https://github.com/Sriram-PR/doc-scraper/security), click **Report a vulnerability**, and fill in the form. Please don't open a public issue, PR, or discussion for a vulnerability.

Include the doc-scraper version, how to reproduce it (a config, a command, and for crawl-time issues a minimal page or sitemap that triggers it), and what an attacker gains.

doc-scraper is maintained by one person. I aim to acknowledge a report within 14 days and will keep you updated in the advisory until it is fixed or declined. With your agreement, you will be credited in the published advisory.

Reports written or found with AI tools are fine if you have verified them yourself. A report needs a reproduction you have actually run against doc-scraper; reports without one are closed.

## Scope

doc-scraper fetches pages from sites it is pointed at, so content served by those sites is untrusted. If you think you have found a vulnerability, report it even if it doesn't fit the list below. These are the areas we care about most:

- **SSRF guard bypass.** With `allow_private_networks` unset or `false` (the default), any way to make doc-scraper connect to a private, loopback, link-local, CGNAT, or multicast address, including through redirects or DNS.
- **Crafted site content.** A page, sitemap, `robots.txt`, or `llms.txt` that crashes doc-scraper, hangs it, or exhausts memory or disk well beyond the size of the content.
- **Writes outside the configured directories.** A URL or page content that makes doc-scraper create or overwrite files outside `output_base_dir` or `state_dir`.
- **MCP tool arguments** that read or write files outside those directories, or fetch past the SSRF guard. The MCP server only speaks over stdio and opens no network port.

Not treated as vulnerabilities:

- Anything that requires control of `config.yaml` or the process environment. The config chooses what to crawl and where to write, and proxy settings (`HTTP_PROXY`, `HTTPS_PROXY`) are honored as given.
- Setting `allow_private_networks: true`, which turns the SSRF guard off by design.
- The pprof debug server. It only exists in builds made with `-tags pprof`, and only listens when you give it an address.
- Vulnerabilities in dependencies with no reachable path in doc-scraper. CI runs `govulncheck` on every change and weekly; report those upstream.

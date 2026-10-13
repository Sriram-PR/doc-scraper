#!/usr/bin/env bash
# Usage: docs-dogfood.sh <doc-scraper binary> <built site dir> [page path under the base]
set -euo pipefail

bin=$1
dist=$2
page=${3-getting-started/quick-start/}
port=18950
root=$(mktemp -d)
server=""
trap '[ -n "$server" ] && kill "$server" 2>/dev/null; rm -rf "$root"' EXIT

# GitHub Pages serves the project site under /doc-scraper/, so mirror that path.
mkdir -p "$root/www"
cp -r "$dist" "$root/www/doc-scraper"
python3 -m http.server "$port" --bind 127.0.0.1 --directory "$root/www" >/dev/null 2>&1 &
server=$!
for _ in $(seq 50); do
	curl -fs -o /dev/null "http://127.0.0.1:$port/doc-scraper/" && break
	sleep 0.1
done

printf 'http_client_settings:\n  allow_private_networks: true\nsites: {}\n' >"$root/config.yaml"
set +e
"$bin" add -config "$root/config.yaml" -dry-run -json "http://127.0.0.1:$port/doc-scraper/$page" >"$root/out.json"
code=$?
set -e
if [ "$code" -ne 2 ]; then
	echo "::error::doc-scraper add exited $code, want 2 (drafted, not written)"
	exit 1
fi

python3 - "$root/out.json" <<'EOF'
import json
import sys

d = json.load(open(sys.argv[1]))
p = d["preview"]
problems = []
if d["framework"] != "starlight":
    problems.append(f"framework={d['framework']!r}, want 'starlight'")
if d["confidence"] != "high":
    problems.append(f"confidence={d['confidence']!r}, want 'high'")
if p.get("error"):
    problems.append(f"preview error: {p['error']}")
if p["nav_leak"]:
    problems.append("preview leaked navigation into the content")
if p["chars"] < 200:
    problems.append(f"preview has {p['chars']} chars, want at least 200")
if problems:
    print("::error::docs dogfood check failed: " + "; ".join(problems))
    sys.exit(1)
print(f"dogfood ok: {d['framework']} ({d['confidence']}, {d['signal_source']}), "
      f"{p['chars']} chars, selector {d['config']['content_selector']!r}")
EOF

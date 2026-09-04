#!/usr/bin/env bash
# Assembles doc-scraper.mcpb from goreleaser's dist/ output: a universal
# macOS binary plus linux/windows amd64, packed with the official mcpb CLI.
# Usage: build-mcpb.sh <version> [dist-dir] [out-dir]
set -euo pipefail

VERSION="$1"
DIST="${2:-dist}"
OUT="${3:-dist/mcpb}"

find_bin() {
	local pattern="$1" name="$2" hit
	hit=$(find "$DIST" -type f -path "*${pattern}*" -name "$name" | head -1)
	if [ -z "$hit" ]; then
		echo "no $name matching $pattern under $DIST" >&2
		exit 1
	fi
	echo "$hit"
}

# Resolve every binary up front: a failed command substitution inside an
# assignment stops the script with find_bin's message, instead of handing
# empty paths to makefat/cp mid-assembly.
DARWIN_AMD64="$(find_bin darwin_amd64 doc-scraper)"
DARWIN_ARM64="$(find_bin darwin_arm64 doc-scraper)"
LINUX_AMD64="$(find_bin linux_amd64 doc-scraper)"
WINDOWS_AMD64="$(find_bin windows_amd64 doc-scraper.exe)"

rm -rf "$OUT"
mkdir -p "$OUT/bundle/server"

go run github.com/randall77/makefat@v0.0.0-20260406194835-1b91746796b7 \
	"$OUT/bundle/server/doc-scraper-macos" "$DARWIN_AMD64" "$DARWIN_ARM64"
cp "$LINUX_AMD64" "$OUT/bundle/server/doc-scraper-linux"
cp "$WINDOWS_AMD64" "$OUT/bundle/server/doc-scraper.exe"
chmod +x "$OUT/bundle/server/doc-scraper-macos" "$OUT/bundle/server/doc-scraper-linux"

jq --arg v "$VERSION" '.version = $v' mcpb/manifest.json > "$OUT/bundle/manifest.json"

npx --yes @anthropic-ai/mcpb@2.1.2 pack "$OUT/bundle" "$OUT/doc-scraper.mcpb"

sha256sum "$OUT/doc-scraper.mcpb" | awk '{print $1}' > "$OUT/doc-scraper.mcpb.sha256"
echo "built $OUT/doc-scraper.mcpb ($(du -h "$OUT/doc-scraper.mcpb" | cut -f1)) sha256=$(cat "$OUT/doc-scraper.mcpb.sha256")"

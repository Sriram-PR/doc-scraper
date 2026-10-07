#!/usr/bin/env bash
# Fails unless the release version matches the version.go default (what
# go install builds report) and mcpb/manifest.json.
# Usage: check-version.sh <version>
set -euo pipefail

VERSION="$1"
CODE="$(sed -nE 's/^var Version = "([^"]+)"$/\1/p' pkg/version/version.go)"
MANIFEST="$(jq -r .version mcpb/manifest.json)"

status=0
if [ "$CODE" != "$VERSION" ]; then
	echo "pkg/version/version.go has \"$CODE\", expected \"$VERSION\"" >&2
	status=1
fi
if [ "$MANIFEST" != "$VERSION" ]; then
	echo "mcpb/manifest.json has \"$MANIFEST\", expected \"$VERSION\"" >&2
	status=1
fi
[ "$status" -eq 0 ] && echo "version $VERSION matches pkg/version and mcpb/manifest.json"
exit "$status"

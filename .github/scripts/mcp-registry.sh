#!/usr/bin/env bash
# Stamps server.json with the release version and the mcpb bundle's URL and
# sha256, then validates it against the MCP registry or publishes it.
# Usage: mcp-registry.sh <validate|publish> <version>
# Expects dist/mcpb/doc-scraper.mcpb.sha256 from build-mcpb.sh.
set -euo pipefail

ACTION="$1"
VERSION="$2"

PUBLISHER_VERSION="v1.8.1"
PUBLISHER_SHA256="a06c9096dcb9727c13555b6be26c7effa707b01f06a4c561ba7a3635443cf2cc"

case "$ACTION" in
validate | publish) ;;
*)
	echo "usage: $0 <validate|publish> <version>" >&2
	exit 2
	;;
esac

curl -sSfL -o mcp-publisher.tar.gz \
	"https://github.com/modelcontextprotocol/registry/releases/download/${PUBLISHER_VERSION}/mcp-publisher_linux_amd64.tar.gz"
echo "${PUBLISHER_SHA256}  mcp-publisher.tar.gz" | sha256sum -c -
tar xzf mcp-publisher.tar.gz mcp-publisher

SHA256="$(cat dist/mcpb/doc-scraper.mcpb.sha256)"
jq --arg v "$VERSION" --arg sha "$SHA256" \
	--arg url "https://github.com/Sriram-PR/doc-scraper/releases/download/v${VERSION}/doc-scraper.mcpb" \
	'.version = $v | .packages[0].identifier = $url | .packages[0].fileSha256 = $sha' \
	server.json > server.publish.json
mv server.publish.json server.json

if [ "$ACTION" = publish ]; then
	./mcp-publisher login github-oidc
	./mcp-publisher publish
else
	./mcp-publisher validate
fi

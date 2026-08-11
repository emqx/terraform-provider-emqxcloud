#!/bin/sh

set -eu

version=${1:-0.1.0}
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

for tool in grep jq; do
  command -v "$tool" >/dev/null 2>&1 || {
    echo "required tool not found: $tool" >&2
    exit 1
  }
done

printf '%s\n' "$version" | grep -qE '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$'
grep -qF "## [$version] - " CHANGELOG.md
grep -qF "version = \"~> $version\"" README.md
test "$(find examples -name main.tf -exec grep -lF "version = \"~> $version\"" {} + | wc -l | tr -d ' ')" = 5
grep -qF 'version="${1:-0.1.0}"' scripts/build-release.sh

jq -e '.version == 1 and .metadata.protocol_versions == ["6.0"]' \
  terraform-registry-manifest.json >/dev/null

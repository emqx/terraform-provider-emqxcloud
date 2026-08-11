#!/bin/sh

set -eu

release_dir=${1:-dist/release}
checksums=$(find "$release_dir" -maxdepth 1 -type f -name '*_SHA256SUMS' -print)
test "$(printf '%s\n' "$checksums" | sed '/^$/d' | wc -l | tr -d ' ')" = 1

manifest_name=$(awk '$2 ~ /_manifest.json$/ { print $2 }' "$checksums")
test "$(printf '%s\n' "$manifest_name" | sed '/^$/d' | wc -l | tr -d ' ')" = 1
cp terraform-registry-manifest.json "$release_dir/$manifest_name"

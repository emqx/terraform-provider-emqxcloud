#!/bin/sh

set -eu

release_dir=${1:-dist/release}
mode=${2:-snapshot}
release_dir=$(CDPATH= cd -- "$release_dir" && pwd)

# A missing tool must fail loudly rather than silently pass a check that runs inside `if`.
for tool in grep jq unzip file find; do
  command -v "$tool" >/dev/null 2>&1 || {
    echo "required tool not found: $tool" >&2
    exit 1
  }
done

case "$mode" in
  snapshot|release) ;;
  *) echo "mode must be snapshot or release" >&2; exit 1 ;;
esac

manifest=$(find "$release_dir" -maxdepth 1 -type f -name '*_manifest.json' -print)
checksums=$(find "$release_dir" -maxdepth 1 -type f -name '*_SHA256SUMS' -print)
test "$(printf '%s\n' "$manifest" | sed '/^$/d' | wc -l | tr -d ' ')" = 1
test "$(printf '%s\n' "$checksums" | sed '/^$/d' | wc -l | tr -d ' ')" = 1
jq -e '.version == 1 and .metadata.protocol_versions == ["6.0"]' "$manifest" >/dev/null

for platform in darwin_amd64 darwin_arm64 linux_amd64 linux_arm64 windows_amd64 windows_arm64; do
  test "$(find "$release_dir" -maxdepth 1 -type f -name "terraform-provider-emqxcloud_*_${platform}.zip" | wc -l | tr -d ' ')" = 1
done
test "$(find "$release_dir" -maxdepth 1 -type f -name '*.zip' | wc -l | tr -d ' ')" = 6

for archive in "$release_dir"/*.zip; do
  entries=$(unzip -Z1 "$archive")
  test "$(printf '%s\n' "$entries" | wc -l | tr -d ' ')" = 5
  for entry in $entries; do
    case "$entry" in
      CHANGELOG.md|LICENSE|NOTICE|README.md|terraform-provider-emqxcloud_v*) ;;
      *) echo "unexpected archive content in $archive: $entry" >&2; exit 1 ;;
    esac
  done
done

(
  cd "$release_dir"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum -c "$(basename "$checksums")"
  else
    shasum -a 256 -c "$(basename "$checksums")"
  fi
)

signature=$(find "$release_dir" -maxdepth 1 -type f -name '*_SHA256SUMS.sig' -print)
if test "$mode" = release; then
  test "$(printf '%s\n' "$signature" | sed '/^$/d' | wc -l | tr -d ' ')" = 1
else
  test -z "$signature"
fi

temporary=$(mktemp -d "${TMPDIR:-/tmp}/emqxcloud-release.XXXXXX")
trap 'rm -rf "$temporary"' EXIT HUP INT TERM
linux_archive=$(find "$release_dir" -maxdepth 1 -type f -name 'terraform-provider-emqxcloud_*_linux_amd64.zip')
unzip -qq "$linux_archive" -d "$temporary"
file "$temporary"/terraform-provider-emqxcloud_v* | grep -q 'statically linked'

for artifact in $(find "$release_dir" -maxdepth 1 -type f -print); do
  case "$(basename "$artifact")" in
    artifacts.json|config.yaml|metadata.json|*.zip|*_manifest.json|*_SHA256SUMS|*_SHA256SUMS.sig) ;;
    *) echo "unexpected release artifact: $artifact" >&2; exit 1 ;;
  esac
done

for artifact in $(find "$release_dir" -mindepth 2 -type f -print); do
  case "$(basename "$artifact")" in
    terraform-provider-emqxcloud_v*) ;;
    *) echo "unexpected build output: $artifact" >&2; exit 1 ;;
  esac
done

if find "$release_dir" -type f \( -name '*.tfstate*' -o -name '*.pem' -o -name '*.key' -o -name '*.log' \) | grep -q .; then
  echo "release output contains a forbidden file" >&2
  exit 1
fi

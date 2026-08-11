#!/bin/sh

set -eu

version="${1:-0.1.0}"
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
provider_dir=$(CDPATH= cd -- "$script_dir/.." && pwd)
"$provider_dir/scripts/check-version.sh" "$version"

mirror_root="$provider_dir/dist/mirror/registry.terraform.io/emqx/emqxcloud/$version"
checksums="$provider_dir/dist/SHA256SUMS"

mkdir -p "$mirror_root"
: > "$checksums"

for target in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64; do
  operating_system=${target%/*}
  architecture=${target#*/}
  target_dir="$mirror_root/${operating_system}_${architecture}"
  binary="$target_dir/terraform-provider-emqxcloud_v$version"

  mkdir -p "$target_dir"
  GOOS="$operating_system" GOARCH="$architecture" CGO_ENABLED=0 \
    go build -ldflags "-X main.version=$version" -o "$binary" "$provider_dir"

  relative_binary=${binary#"$provider_dir/dist/"}
  if command -v sha256sum >/dev/null 2>&1; then
    (
      cd "$provider_dir/dist"
      sha256sum "$relative_binary"
    ) >> "$checksums"
  else
    (
      cd "$provider_dir/dist"
      shasum -a 256 "$relative_binary"
    ) >> "$checksums"
  fi
done

echo "Built filesystem mirror under $provider_dir/dist/mirror"
echo "Checksums written to $checksums"

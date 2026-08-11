#!/bin/sh

set -eu

terraform_bin=${1:-terraform}
version=${2:-0.1.0}
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
workspace=$(mktemp -d "${TMPDIR:-/tmp}/emqxcloud-examples.XXXXXX")
trap 'rm -rf "$workspace"' EXIT HUP INT TERM
chmod 700 "$workspace"

"$root/scripts/build-release.sh" "$version"

cli_config="$workspace/terraformrc"
printf 'provider_installation {\n  filesystem_mirror {\n    path = %s\n    include = ["emqx/emqxcloud"]\n  }\n  direct {\n    exclude = ["emqx/emqxcloud"]\n  }\n}\n' \
  "\"$root/dist/mirror\"" >"$cli_config"
chmod 600 "$cli_config"

find "$root/examples" -name main.tf -print | while IFS= read -r main; do
  example=$(dirname "$main")
  destination="$workspace/${example#"$root/examples/"}"
  mkdir -p "$destination"
  cp "$example"/*.tf "$destination/"
  (
    cd "$destination"
    TF_CLI_CONFIG_FILE="$cli_config" "$terraform_bin" init -backend=false -input=false -no-color
    TF_CLI_CONFIG_FILE="$cli_config" "$terraform_bin" validate -no-color
  )
done

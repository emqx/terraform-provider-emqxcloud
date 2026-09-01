#!/bin/sh

set -eu

terraform_bin=${1:-terraform}
version=${2:-0.2.0}
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

workspace=$(mktemp -d "${TMPDIR:-/tmp}/emqxcloud-examples.XXXXXX")
trap 'rm -rf "$workspace"' EXIT HUP INT TERM
chmod 700 "$workspace"

"$root/scripts/build-release.sh" "$version"

cli_config="$workspace/terraformrc"
printf 'provider_installation {\n  filesystem_mirror {\n    path = %s\n    include = ["emqx/emqxcloud"]\n  }\n  direct {\n    exclude = ["emqx/emqxcloud"]\n  }\n}\n' \
  "\"$root/dist/mirror\"" >"$cli_config"
chmod 600 "$cli_config"

validate_example() {
  example=$1
  destination="$workspace/${example#"$root/examples/"}"
  mkdir -p "$destination"
  cp "$example"/*.tf "$destination/"
  (
    cd "$destination"
    TF_CLI_CONFIG_FILE="$cli_config" "$terraform_bin" init -backend=false -input=false -no-color
    TF_CLI_CONFIG_FILE="$cli_config" "$terraform_bin" validate -no-color
  )
}

find "$root/examples" -name main.tf -print | while IFS= read -r main; do
  validate_example "$(dirname "$main")"
done

terraform_version=$("$terraform_bin" version -json)
terraform_major=$(printf '%s' "$terraform_version" | jq -r '.terraform_version | split(".")[0]')
terraform_minor=$(printf '%s' "$terraform_version" | jq -r '.terraform_version | split(".")[1]')
if test "$terraform_major" -gt 1 || { test "$terraform_major" -eq 1 && test "$terraform_minor" -ge 14; }; then
  validate_example "$root/examples/actions/emqxcloud_reset_authorization_cache"
fi

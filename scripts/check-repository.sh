#!/bin/sh

set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

# A missing tool must fail loudly. Both guards below run inside `if`, where a command that is
# not installed reports failure and would silently pass the check.
for tool in go git grep; do
  command -v "$tool" >/dev/null 2>&1 || {
    echo "required tool not found: $tool" >&2
    exit 1
  }
done

module=$(go list -m)
test "$module" = "github.com/emqx/terraform-provider-emqxcloud" || {
  echo "unexpected Go module: $module" >&2
  exit 1
}

old_repository='github.com/emqx/emqx-cloud-'terraform-provider
if git grep -nF "$old_repository"; then
  echo "old canonical repository address found" >&2
  exit 1
fi

expected_docs='docs/data-sources/deployment.md
docs/data-sources/deployments.md
docs/data-sources/projects.md
docs/index.md
docs/resources/action.md
docs/resources/authentication_user.md
docs/resources/authorization_client.md
docs/resources/authorization_user.md
docs/resources/banned.md
docs/resources/connector.md
docs/resources/deployment.md
docs/resources/deployment_tls.md
docs/resources/rule.md'
actual_docs=$(find docs -type f -print | LC_ALL=C sort)
test "$actual_docs" = "$expected_docs" || {
  echo "Registry documentation file set does not match registered objects" >&2
  printf '%s\n' "$actual_docs" >&2
  exit 1
}

if grep -rnE 'uses: [^ ]+@(main|master|v[0-9])' .github/workflows; then
  echo "GitHub Actions must use immutable commit SHAs" >&2
  exit 1
fi

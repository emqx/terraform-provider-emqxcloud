#!/bin/sh

set -eu

cd "$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
exec go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs@v0.25.0 \
  validate --provider-name emqxcloud --tf-version 1.15.8

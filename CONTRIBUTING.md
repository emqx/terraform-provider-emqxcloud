# Contributing

Thank you for contributing to the EMQX Cloud Terraform Provider. Participation in this project is governed by our
[Code of Conduct](.github/CODE_OF_CONDUCT.md).

## Before opening a change

1. Open an issue for new resources, externally visible behavior, large refactors, or security policy changes.
2. Keep changes within this repository's Provider, tests, examples, and release tooling. Platform API
   implementation changes belong in the EMQX Cloud Console repository.
3. Never include credentials, Terraform state, certificates, private keys, customer data, or live API output.

## Development

Use Go 1.25 and Terraform 1.0 or later. Run the relevant focused tests while editing, then run:

```shell
test -z "$(gofmt -l .)"
terraform fmt -check -recursive examples/
go test ./...
go vet ./...
go build ./...
./scripts/generate-docs.sh
./scripts/validate-docs.sh
git diff --check
```

Generated Registry documentation under `docs/` must be committed with schema or template changes.

Live tests require explicit authorization and a dedicated non-production target. A pull request must not depend on
live credentials to pass.

## Releases

Maintainers release from a default-branch commit that passed all required checks. Release tags use `v`-prefixed
Semantic Versioning, such as `v0.1.0`. Published versions are immutable; fixes require a new version.

By submitting a contribution, you agree that it is licensed under the Apache License 2.0. Parts of this repository
are derived from MPL-2.0 licensed sources; see [NOTICE](NOTICE).

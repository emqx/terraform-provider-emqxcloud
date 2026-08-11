# AGENTS.md

Repository instructions for Codex and other coding agents working in this Provider repository.

## Rule Loading

Load only the rules that match the task and changed paths.

- Go Provider, client, schema, state, lifecycle, examples, build scripts, or CI:
  read `.codex/rules/provider-development.md`.
- Unit, protocol, acceptance, live API, Terraform, MQTT, or TLS testing:
  also read `.codex/rules/testing.md`.
- Review-only requests are read-only. Review the requested diff first and report concrete findings before
  proposing changes.

If instructions conflict, follow the stricter rule; the more local `AGENTS.md` wins.

## Repository Boundary and Sources of Truth

- This repository owns the Go Provider, tests, examples, and release build.
- The EMQX Cloud Console owns the Platform API implementation and its OpenAPI definitions. When Platform behavior
  must change, make that change in the Console repository under its own instructions; do not reproduce Console
  server-side implementation here.
- API contract snapshots are not part of this repository. Maintainers may keep them locally under the untracked
  `spec/` directory; they are review references only, never runtime inputs or code-generation sources.
- Deployment API resources call the configured product API directly. For EMQX v5, combine the published Dedicated
  API documentation with verified runtime behavior; do not infer undocumented payloads.

## Development Workflow

- New resources, externally visible behavior, large refactors, and security-policy changes require a confirmed
  development document before implementation. Use the matching Console release document when the Platform API is
  involved; otherwise keep a concise Provider document under `dev/release/<version>/`, which is untracked.
- Before drafting, inspect the current resource flow, its API contract, tests, and examples; restate the scope and
  ask only questions that materially affect behavior or risk.
- Require separate confirmation of the complete document, its `Notes and Limitations` section, and the request to
  implement. Keep `Notes and Limitations` to at most five one-sentence, real boundaries.
- Small, local, low-risk fixes may proceed after identifying the failing path, scope, and impact. If the work grows,
  stop and document it.
- Implement confirmed points in order. For each point, review the local diff and run the smallest relevant checks.
- Do not commit, push, publish, create a release, or run live tests unless the user asks for that action.

## Execution Constraints

- State material assumptions and observable success criteria before changing behavior.
- Read each target file, its callers or registrations, related tests, and the relevant API contract before editing.
- Make the smallest change that satisfies the confirmed contract. Reuse the existing client, resource patterns,
  standard library, and installed dependencies.
- Preserve the Platform/Deployment terminology. Terraform fields use `snake_case`; API request and response structs
  use the API's exact JSON field names.
- Fix lifecycle and state bugs at the shared path when possible. Never trade away input validation, secret handling,
  remote-resource recoverability, or destructive-operation safeguards.
- Tests must protect observable Provider and remote-resource behavior, not mirror implementation details.
- When a schema or payload changes, synchronize its tests, examples, README, and the applicable upstream API
  contract.
- Report what changed, exactly what was verified, and anything still unverified.

# Provider Development Rules

Apply these rules to Go Provider code, clients, schemas, state handling, Terraform examples, build scripts, and CI.

## Architecture and Naming

- Keep one Provider with two independent credential groups:
  - Platform API manages projects, deployments, and deployment TLS.
  - Deployment API manages resources inside one product deployment, currently EMQX v5.
- Never fall back between the two credential groups. Endpoint, API key, and API secret must be configured together.
- Use `Platform` and `Deployment` in Go types, diagnostics, documentation, and examples. Do not introduce the old
  `Cloud`/`EMQX` configuration terminology.
- Terraform schema names are `snake_case`; JSON tags and payload fields match the remote API exactly, including its
  casing. Keep the conversion at typed request/response boundaries.
- Use Terraform Plugin Framework Protocol 6 and the existing `internal/client` package. Do not add OpenAPI code
  generation, a second HTTP stack, or Terraform Plugin SDK abstractions.

## HTTP and Secret Safety

- Use `context.Context`, the shared client, Basic Auth, bounded response reads, and `client.EscapePathSegment` for
  remote identities in paths.
- Retry only GET requests for the existing transient statuses. Do not automatically retry POST, PUT, or DELETE.
- API diagnostics may include the HTTP status and a validated short error code, but never response bodies, API
  secrets, Authorization headers, passwords, certificates, private keys, or sensitive JSON.
- Mark secrets and opaque configuration containing possible secrets as `Sensitive`. Remember that this hides CLI
  output but does not encrypt Terraform state.

## Terraform State and Lifecycle

- After a successful remote create, persist enough identity and planned state before polling. A timeout or failed
  read must leave the remote object manageable instead of orphaning it.
- A confirmed Read 404 removes the resource from state. If an API uses an ambiguous empty response or DELETE 404,
  verify absence with the documented read path before dropping state.
- Preserve configured write-only or masked values when Read omits them or returns `******`; refresh only stable,
  readable remote fields.
- Identity changes use the existing replace semantics unless the resource has a stricter lifecycle boundary.
- `emqxcloud_deployment` is the exception: it creates only `dedicatedFlex` in `running`, supports only start/stop
  updates, rejects immutable-field changes, and never sends a remote delete. Do not weaken this boundary.
- TLS delete affects only deployment TLS. A DELETE 404 is not success while the TLS read path still reports a
  configuration.
- Connector and Action identities are `type:name`; Rule uses `rule_id`. Action updates omit immutable `type` and
  `name`; Rule may use `actions: []`.
- Authentication passwords remain write-only. Username/clientid authorization resources own only that identity's
  complete rule set. Banned resources own one exact `as`/`who` entry and never use collection delete endpoints.
- Keep dependency cleanup explicit: Rule before Action, Action before Connector. Do not add cascade deletion.

## Change Shape

- Prefer typed models for stable Platform fields. Retain generic normalized JSON only where Connector, Action, and
  Rule product schemas are intentionally open-ended.
- Validate trust-boundary inputs before requests: non-empty identities, allowed enum values, JSON object/array
  shape, duplicated identity fields, endpoint shape, and TLS mode requirements.
- Use the Go standard library and existing dependencies first. Explain and justify any new direct dependency.
- Register a new data source or resource in `provider.go`, add the smallest behavior-focused test, and update the
  relevant example and README entry.
- Change request or response handling only after confirming the upstream API contract with its owner; do not infer
  it from a stale local snapshot.

## Local Completion Checks

Run the checks relevant to the diff; before handoff of Provider code, run the full set from the repository root:

```shell
test -z "$(gofmt -l .)"
terraform fmt -check -recursive examples/
go test ./...
go vet ./...
go build ./...
git diff --check
```

Do not claim skipped, partial, or failed checks passed.

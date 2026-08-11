# Testing and Live Acceptance Rules

Apply these rules to unit, protocol, acceptance, live API, Terraform, MQTT, and TLS tests.

## Automated Tests

- Use `httptest` and Terraform Protocol 6 tests for deterministic behavior. Assert the HTTP method, exact path,
  payload, provider alias, state transition, empty follow-up plan, and cleanup behavior that form the contract.
- For bugs, add the smallest regression case that fails before the fix. Include the failure path when it protects
  remote identity, state retention, secret redaction, or destructive-operation safety.
- Keep tests hermetic by default. Live credentials and network access must never be required by `go test ./...`.

## Live-Test Authorization and Isolation

- Run live tests only when the user explicitly requests them and supplies or identifies a dedicated non-production
  target. Confirm the exact project, deployment, APIs, and destructive scope before applying changes.
- Never echo credentials or place them in tracked files, command history, plans, logs, screenshots, or reports. Use
  environment variables or silent input; redact secrets from tool output.
- Use unique resource names and a separate Terraform working directory. Protect it with directory mode `0700` and
  state/config/plan files with mode `0600` when they contain credentials, passwords, certificates, or private keys.
- Inspect `terraform plan` before apply. Run create/read, update or replacement, a second plan expecting `No changes`,
  and destroy only for resources whose delete behavior is in scope. Verify remote absence afterward.
- Record every created remote identity before the next step. If a test fails, stop expansion and clean up known
  resources in dependency order.

## Resource-Specific Safety

- Platform deployment creation may incur charges and is rate-limited. Create at most the explicitly approved
  deployment and do not parallelize long-running deployment operations.
- Always place `lifecycle.prevent_destroy = true` on live `emqxcloud_deployment` resources. The Provider must not
  delete deployments; `terraform state rm` only transfers management and is not remote cleanup.
- Do not destroy or remove the only Terraform state for a live deployment unless the user explicitly requests the
  transfer and the persistent replacement state or manual owner is identified.
- Clean Deployment API resources and TLS before stopping a deployment. Delete Rule, then Action, then Connector;
  delete only the exact authentication, authorization, and banned identities created by the test.
- TLS tests require a target without unowned TLS configuration. Verify certificate hostname and expiry. For two-way
  TLS, test MQTT access both without and with the client certificate; do not treat a raw TLS handshake alone as
  proof of MQTT client-certificate enforcement.
- Terraform state can retain private keys and old sensitive values after destroy. Once remote cleanup and empty
  state are verified, securely remove disposable test workspaces; preserve persistent deployment lifecycle state.
- When MQTT behavior is in scope, verify the intended authentication/ACL or publish path and read the related Rule
  and Action metrics instead of treating resource creation alone as success.

## Handoff

- Report the tested endpoint type without repeating credentials, exact resources created/updated/deleted, final
  remote status, final Terraform plan result, cleanup gaps, and the path of any state that must be preserved.
- A live test is incomplete while an unexpected resource remains, a deployment cleanup owner is unknown, or a
  sensitive temporary workspace is left behind without an explicit reason.

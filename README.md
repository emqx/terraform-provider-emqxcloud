# EMQX Cloud Terraform Provider

Manage EMQX Cloud Platform resources and resources inside EMQX v5 deployments with Terraform.

## Features

The Provider uses two independent API credential groups. Configure only the group needed by your configuration;
credentials are never shared or used as fallback between them.

| API | Terraform objects |
| --- | --- |
| Platform API | Projects and deployments data sources, Dedicated Flex deployment lifecycle, deployment API key issuance, deployment TLS |
| Deployment API | Connector, Action, Source, Rule, authorization-cache reset, authentication user, username/clientid authorization, banned entry |

The Provider uses Terraform Plugin Protocol 6 and requires Terraform 1.0 or later. Key issuance and the complete
workflow require Terraform 1.3 or later so deletion and replacement can be blocked during planning. Invoking
`emqxcloud_reset_authorization_cache` requires Terraform 1.14 or later.

## Registry installation

Add the Provider to your Terraform configuration:

```terraform
terraform {
  required_providers {
    emqxcloud = {
      source  = "emqx/emqxcloud"
      version = "~> 0.3.0"
    }
  }
}
```

Then run:

```shell
terraform init
```

## Configuration

Platform and Deployment API credentials can be set in Provider configuration or environment variables.
Each group must supply its endpoint, API key, and API secret entirely in Provider configuration or entirely
through environment variables. Setting any group member in configuration selects configuration for the whole
group; empty or missing members are errors and never use environment values to fill gaps. Only an entirely
unset/null group uses environment variables.
Endpoints must use HTTPS and must not contain URL user information, a query string, or a fragment.

```terraform
provider "emqxcloud" {
  alias = "platform"

  platform_endpoint   = "https://example.com/public_api/v1"
  platform_api_key    = var.platform_api_key
  platform_api_secret = var.platform_api_secret
}

provider "emqxcloud" {
  alias = "deployment"

  deployment_endpoint   = "https://deployment.example.com:8443/api/v5"
  deployment_api_key    = var.deployment_api_key
  deployment_api_secret = var.deployment_api_secret
}
```

The equivalent environment variables are:

```text
EMQXCLOUD_PLATFORM_ENDPOINT
EMQXCLOUD_PLATFORM_API_KEY
EMQXCLOUD_PLATFORM_API_SECRET
EMQXCLOUD_DEPLOYMENT_ENDPOINT
EMQXCLOUD_DEPLOYMENT_API_KEY
EMQXCLOUD_DEPLOYMENT_API_SECRET
```

Use Provider aliases when one configuration manages Platform resources and one or more deployments. The
[`platform/complete` example](examples/platform/complete/main.tf) creates a Dedicated Flex deployment, issues
`emqxcloud_deployment_api_key` through the Platform API, and uses its outputs to create an MQTT user in the same apply:

```terraform
provider "emqxcloud" {
  alias                 = "deployment"
  deployment_endpoint   = emqxcloud_deployment.main.connection_info.api_endpoint
  deployment_api_key    = emqxcloud_deployment_api_key.main.app_id
  deployment_api_secret = emqxcloud_deployment_api_key.main.app_secret
}
```

Supply the initial Platform credentials through `EMQXCLOUD_PLATFORM_*`. The Platform key must grant deployment
management and the separate `Deployment_API_Key` service for the selected project. Declare the Deployment key's
`whitelist` explicitly before issuance; no permissions are silently added. Configuration values that are not yet
known wait for their resource dependencies and never use stale environment credentials instead.

`connection_info` contains `address`, `api_endpoint` and `ports` with `protocol`, integer `port` and `status`.
Only ports with `status = "running"` are available. The object is null when the API omits connection information,
including stopped deployments and older Platform API versions. The deployment list does not expose these fields.
Missing or empty string fields and missing listener fields are null; an omitted listener list is null, while an
explicit empty list remains empty.

If credentials become unknown during a deployment status change, Deployment resources retain their prior state
and report a refresh warning without making API calls. This allows a normal apply to restart a deployment that
was stopped externally. Run a full plan after recovery to check remote changes skipped during that refresh;
resource changes and Actions still require known credentials. Unknown Platform credentials produce an explicit
dependency diagnostic when the Platform API is needed.

Before deliberately stopping a deployment, remove its managed Deployment resources and apply while it is still
running, then set the deployment status to `stopped`. Keep its deployment and key records. If a dependency cannot
be resolved by a full apply, start the deployment through the Platform API or use a targeted deployment apply,
then run a full plan.

## Examples

Runnable examples are under [`examples/`](examples/):

| Directory | Contents |
| --- | --- |
| `platform/` | Project and deployment data sources, deployment lifecycle, TLS, and the complete deployment → key → MQTT user workflow |
| `deployment/` | Authentication, authorization (ACL), banned entries, data integration, and an MQTT Source |
| `actions/` | Explicit Terraform 1.14+ authorization-cache reset invocation |

Start with the read-only `platform/data-sources` example. The other examples can change remote resources or incur
charges and must only be run against a dedicated non-production target.

## Lifecycle and state safety

- `emqxcloud_deployment` creates only Dedicated Flex deployments and can start or stop them. It never deletes a
  deployment. Removing management requires an explicit state transfer, followed by manual remote ownership and
  cleanup.
- Terraform import is not supported.
- `emqxcloud_deployment_api_key` retains a local issuance record. Refresh never creates another key and cannot
  detect remote revocation or permission drift. Its inputs cannot change; import, rotation, remote deletion and
  replacement are unsupported. Revoke it in Console before explicitly removing its record from state.
- Key issuance requires a running Dedicated or Dedicated Flex deployment, permits at most five keys per
  deployment, and is limited to two successful creations per hour per Platform key. Serverless and BYOC issuance
  are not supported. A failed or uncertain write is not retried; an incomplete receipt blocks reissuance for that
  resource until the outcome has been resolved in Console.
- Key creation is serialized within each Provider process, including independent resources sharing that process.
  Coordinate writes to the same deployment from other Provider processes, Terraform runs, Console or API clients;
  this local safeguard does not make backend key updates concurrency-safe.
- Connector, Action, and Source names plus Rule IDs are generated once by the Provider as `c-`, `a-`, `s-`, or
  `r-` followed by six lowercase letters or digits. Put human-readable descriptions and functional settings in
  `config_json` and use resource references for dependencies.
- Delete dependent data-integration resources in Rule, then Action or Source, then Connector order.
- API secrets, TLS certificates, private keys, passwords, and opaque JSON can remain in Terraform state. `Sensitive` suppresses
  normal CLI display but does not encrypt state.
- Use an encrypted remote backend with access controls. Never commit state, credentials, certificates, or keys.

Keep both the deployment and its key protected with `prevent_destroy`. Clean deployment resources while their
Provider alias and key still work, then handle Key revocation and deployment ownership explicitly. The Provider
does not return a lost SK again or automatically replace a revoked key.

A failed key creation may retain a tainted issuance record, including an unexpected HTTP 200 response with
complete credentials. After confirming in Console that this exact key is usable and both AK/SK are retained,
use `terraform untaint emqxcloud_deployment_api_key.main` to keep it. Do not discard the state and issue another
key. A missing SK cannot be recovered by untainting; resolve that issuance in Console first.

See the generated [Registry documentation](docs/index.md) for every Provider, data source, resource, and Action.

### Upgrading from v0.1.0

Before planning with v0.2.0, remove Connector and Action `name`, Rule `rule_id`, and Rule `config_json.name` from
configuration. Existing identities remain unchanged in state and are not replaced; only newly created objects use
the generated eight-character identities.

## Local development installation

Local development uses a filesystem mirror and is separate from the signed Registry release build.

Requirements:

- Go 1.26.10 or later
- Terraform 1.0 or later
- Git

Build the v0.3.0 mirror:

```shell
git clone https://github.com/emqx/terraform-provider-emqxcloud.git
cd terraform-provider-emqxcloud
./scripts/build-release.sh 0.3.0
```

The script creates binaries under `dist/mirror/registry.terraform.io/emqx/emqxcloud/0.3.0/` for Darwin and Linux
on AMD64 and ARM64. Create a Terraform CLI configuration file with the absolute mirror path:

```hcl
provider_installation {
  filesystem_mirror {
    path    = "/absolute/path/to/terraform-provider-emqxcloud/dist/mirror"
    include = ["emqx/emqxcloud"]
  }

  direct {
    exclude = ["emqx/emqxcloud"]
  }
}
```

Set `TF_CLI_CONFIG_FILE` to that file before running `terraform init` in an example directory.

## Development checks

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

See [CONTRIBUTING.md](CONTRIBUTING.md) for the contribution workflow and [SECURITY.md](SECURITY.md) for private
vulnerability reporting.

## License

Licensed under the [Apache License 2.0](LICENSE). See [NOTICE](NOTICE) for copyright and third-party attribution.

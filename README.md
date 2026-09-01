# EMQX Cloud Terraform Provider

Manage EMQX Cloud Platform resources and resources inside EMQX v5 deployments with Terraform.

## Features

The Provider uses two independent API credential groups. Configure only the group needed by your configuration;
credentials are never shared or used as fallback between them.

| API | Terraform objects |
| --- | --- |
| Platform API | Projects and deployments data sources, Dedicated Flex deployment lifecycle, deployment TLS |
| Deployment API | Connector, Action, Source, Rule, authorization-cache reset, authentication user, username/clientid authorization, banned entry |

The Provider uses Terraform Plugin Protocol 6 and requires Terraform 1.0 or later. Invoking
`emqxcloud_reset_authorization_cache` requires Terraform 1.14 or later.

## Registry installation

Add the Provider to your Terraform configuration:

```terraform
terraform {
  required_providers {
    emqxcloud = {
      source  = "emqx/emqxcloud"
      version = "~> 0.2.0"
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
Each configured group requires its endpoint, API key, and API secret together.
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

Use Provider aliases when one configuration manages Platform resources and one or more deployments. A Platform
deployment does not expose or create Deployment API credentials; prepare those credentials separately in the
target deployment.

## Examples

Runnable examples are under [`examples/`](examples/):

| Directory | Contents |
| --- | --- |
| `platform/` | Project and deployment data sources, deployment lifecycle, and TLS |
| `deployment/` | Authentication, authorization (ACL), banned entries, data integration, and an MQTT Source |
| `actions/` | Explicit Terraform 1.14+ authorization-cache reset invocation |

Start with the read-only `platform/data-sources` example. The other examples can change remote resources or incur
charges and must only be run against a dedicated non-production target.

## Lifecycle and state safety

- `emqxcloud_deployment` creates only Dedicated Flex deployments and can start or stop them. It never deletes a
  deployment. Removing management requires an explicit state transfer, followed by manual remote ownership and
  cleanup.
- v0.2.0 does not support Terraform import.
- Connector, Action, and Source names plus Rule IDs are generated once by the Provider as `c-`, `a-`, `s-`, or
  `r-` followed by six lowercase letters or digits. Put human-readable descriptions and functional settings in
  `config_json` and use resource references for dependencies.
- Delete dependent data-integration resources in Rule, then Action or Source, then Connector order.
- TLS certificates, private keys, passwords, and opaque JSON can remain in Terraform state. `Sensitive` suppresses
  normal CLI display but does not encrypt state.
- Use an encrypted remote backend with access controls. Never commit state, credentials, certificates, or keys.

See the generated [Registry documentation](docs/index.md) for every Provider, data source, resource, and Action.

### Upgrading from v0.1.0

Before planning with v0.2.0, remove Connector and Action `name`, Rule `rule_id`, and Rule `config_json.name` from
configuration. Existing identities remain unchanged in state and are not replaced; only newly created objects use
the generated eight-character identities.

## Local development installation

Local development uses a filesystem mirror and is separate from the signed Registry release build.

Requirements:

- Go 1.25
- Terraform 1.0 or later
- Git

Build the v0.2.0 mirror:

```shell
git clone https://github.com/emqx/terraform-provider-emqxcloud.git
cd terraform-provider-emqxcloud
./scripts/build-release.sh 0.2.0
```

The script creates binaries under `dist/mirror/registry.terraform.io/emqx/emqxcloud/0.2.0/` for Darwin and Linux
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

# Changelog

All notable changes to this project will be documented in this file.

## [0.3.0] - Unreleased

- Added `connection_info` to the deployment resource and detail data source, including MQTT listeners and the Deployment API endpoint.
- Added `emqxcloud_deployment_api_key` to issue one key with explicit permissions and retain its once-only secret in Sensitive state.
- Serialized key creation within each Provider process to prevent independent resources from overwriting keys through concurrent backend updates; other processes and clients still require coordination.
- Supported resource-derived Provider credentials so one apply can create a deployment, issue its key, and manage deployment resources.
- Prevented mixed configuration/environment credential groups and blocked uncertain changes to existing key inputs during planning.
- Preserved Deployment resource state with an explicit warning while credentials are pending, allowing stopped deployments to recover without stale credential fallback.
- Key issuance requires Terraform 1.3 or later to guard create-before-destroy replacement during planning; existing resources retain Terraform 1.0 compatibility.
- Added a complete workflow example; key refresh never reissues credentials, and updates, deletion and replacement are blocked pending manual revocation.
- Updated the build toolchain to Go 1.26.10 and refreshed Go dependencies, including gRPC, HCL, cryptography and networking libraries.

## [0.2.0] - 2026-08-31

- Added the EMQX v5 Source resource and MQTT Source example.
- Added the Terraform 1.14+ authorization-cache reset Action.
- Provider-generated eight-character `name` values for Connector, Action, and Source, and `rule_id` values for Rule.
- Existing v0.1 identities remain unchanged; configurations must remove Connector/Action `name`, Rule `rule_id`,
  and Rule `config_json.name` when upgrading.

## [0.1.0] - 2026-08-11

- Initial EMQX Cloud Terraform Provider release.
- HTTPS-only API endpoints and same-origin redirect protection for Basic Auth credentials.
- Three Platform data sources.
- Dedicated Flex deployment lifecycle and deployment TLS resources.
- EMQX v5 Connector, Action, Rule, authentication, authorization, and banned resources.

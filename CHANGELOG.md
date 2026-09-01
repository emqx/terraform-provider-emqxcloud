# Changelog

All notable changes to this project will be documented in this file.

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

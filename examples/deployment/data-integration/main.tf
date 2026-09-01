terraform {
  required_version = ">= 1.0"

  required_providers {
    emqxcloud = {
      source  = "emqx/emqxcloud"
      version = "~> 0.2.0"
    }
  }
}

provider "emqxcloud" {
  alias = "deployment"
}

variable "http_endpoint" {
  type = string
}

resource "emqxcloud_connector" "http" {
  provider = emqxcloud.deployment

  type = "http"
  config_json = jsonencode({
    enable          = true
    connect_timeout = "5s"
    pool_size       = 1
    headers = {
      content-type = "application/json"
    }
    url = var.http_endpoint
  })
}

resource "emqxcloud_action" "http" {
  provider = emqxcloud.deployment

  type = "http"
  config_json = jsonencode({
    connector = emqxcloud_connector.http.name
    enable    = true
    parameters = {
      headers = {}
      method  = "get"
      path    = "/ping"
    }
  })
}

resource "emqxcloud_rule" "http" {
  provider = emqxcloud.deployment

  config_json = jsonencode({
    sql         = "SELECT * FROM \"terraform/preview\""
    actions     = ["${emqxcloud_action.http.type}:${emqxcloud_action.http.name}"]
    enable      = true
    description = "Terraform Provider v0.2 preview"
  })
}

output "data_integration_identities" {
  value = {
    connector_name = emqxcloud_connector.http.name
    action_name    = emqxcloud_action.http.name
    rule_id        = emqxcloud_rule.http.rule_id
  }
}

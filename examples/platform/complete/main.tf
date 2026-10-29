terraform {
  required_version = ">= 1.3"

  required_providers {
    emqxcloud = {
      source  = "emqx/emqxcloud"
      version = "~> 0.3.0"
    }
  }
}

provider "emqxcloud" {
  alias = "platform"
}

variable "project_id" {
  type = string
}

variable "platform" {
  type = string
}

variable "region" {
  type = string
}

variable "deployment_version" {
  type    = string
  default = "v5"
}

variable "connections" {
  type    = number
  default = 1000
}

variable "tps" {
  type    = number
  default = 1000
}

variable "mqtt_user_id" {
  type = string
}

variable "mqtt_password" {
  type      = string
  sensitive = true
}

resource "emqxcloud_deployment" "main" {
  provider           = emqxcloud.platform
  project_id         = var.project_id
  platform           = var.platform
  region             = var.region
  connections        = var.connections
  tps                = var.tps
  deployment_version = var.deployment_version
  status             = "running"

  lifecycle {
    prevent_destroy = true
  }
}

resource "emqxcloud_deployment_api_key" "main" {
  provider      = emqxcloud.platform
  deployment_id = emqxcloud_deployment.main.deployment_id
  description   = "Terraform deployment management"
  whitelist = {
    "/api/v5/authentication/password_based:built_in_database/users" = ["GET", "POST", "PUT", "DELETE"]
  }

  lifecycle {
    prevent_destroy = true
  }
}

provider "emqxcloud" {
  alias                 = "deployment"
  deployment_endpoint   = emqxcloud_deployment.main.connection_info.api_endpoint
  deployment_api_key    = emqxcloud_deployment_api_key.main.app_id
  deployment_api_secret = emqxcloud_deployment_api_key.main.app_secret
}

resource "emqxcloud_authentication_user" "example" {
  provider     = emqxcloud.deployment
  user_id      = var.mqtt_user_id
  password     = var.mqtt_password
  is_superuser = false
}

output "deployment_id" {
  value = emqxcloud_deployment.main.deployment_id
}

output "connection_info" {
  value = emqxcloud_deployment.main.connection_info
}

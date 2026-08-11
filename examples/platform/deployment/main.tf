terraform {
  required_version = ">= 1.0"

  required_providers {
    emqxcloud = {
      source  = "emqx/emqxcloud"
      version = "~> 0.1.0"
    }
  }
}

provider "emqxcloud" {}

variable "project_id" {
  type = string
}

variable "platform" {
  type = string
}

variable "region" {
  type = string
}

variable "connections" {
  type = number
}

variable "tps" {
  type = number
}

variable "deployment_name" {
  type = string
}

variable "deployment_version" {
  type    = string
  default = "v5"
}

variable "status" {
  type    = string
  default = "running"
}

resource "emqxcloud_deployment" "current" {
  project_id         = var.project_id
  platform           = var.platform
  region             = var.region
  connections        = var.connections
  tps                = var.tps
  deployment_name    = var.deployment_name
  deployment_version = var.deployment_version
  status             = var.status

  lifecycle {
    prevent_destroy = true
  }
}

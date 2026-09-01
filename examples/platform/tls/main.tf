terraform {
  required_version = ">= 1.0"

  required_providers {
    emqxcloud = {
      source  = "emqx/emqxcloud"
      version = "~> 0.2.0"
    }
  }
}

provider "emqxcloud" {}

variable "deployment_id" {
  type = string
}

variable "tls_type" {
  type    = string
  default = "one-way"
}

variable "certificate_path" {
  type = string
}

variable "private_key_path" {
  type = string
}

variable "ca_certificate_path" {
  type    = string
  default = null
}

resource "emqxcloud_deployment_tls" "current" {
  deployment_id   = var.deployment_id
  tls_type        = var.tls_type
  certificate_pem = file(var.certificate_path)
  private_key_pem = file(var.private_key_path)
  ca_certificate_pem = (
    var.ca_certificate_path == null ? null : file(var.ca_certificate_path)
  )
}

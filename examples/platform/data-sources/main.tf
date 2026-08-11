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

data "emqxcloud_projects" "current" {}

data "emqxcloud_deployments" "current" {}

variable "deployment_id" {
  description = "Optional deployment ID to read in detail."
  type        = string
  default     = null
}

data "emqxcloud_deployment" "selected" {
  count = var.deployment_id == null ? 0 : 1

  deployment_id = var.deployment_id
}

output "projects" {
  value = data.emqxcloud_projects.current.projects
}

output "deployments" {
  value = data.emqxcloud_deployments.current.deployments
}

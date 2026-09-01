terraform {
  required_version = ">= 1.14.0"

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

action "emqxcloud_reset_authorization_cache" "deployment" {
  provider = emqxcloud.deployment
}

# This action has no ordinary apply trigger. Invoke it explicitly:
# terraform plan -invoke=action.emqxcloud_reset_authorization_cache.deployment
# terraform apply -invoke=action.emqxcloud_reset_authorization_cache.deployment

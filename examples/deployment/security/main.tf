terraform {
  required_version = ">= 1.0"

  required_providers {
    emqxcloud = {
      source  = "emqx/emqxcloud"
      version = "~> 0.1.0"
    }
  }
}

provider "emqxcloud" {
  alias = "deployment"
}

variable "authentication_user_id" {
  type = string
}

variable "authentication_password" {
  type      = string
  sensitive = true
}

variable "authorization_username" {
  type = string
}

variable "authorization_client_id" {
  type = string
}

variable "banned_client_id" {
  type = string
}

resource "emqxcloud_authentication_user" "current" {
  provider = emqxcloud.deployment

  user_id      = var.authentication_user_id
  password     = var.authentication_password
  is_superuser = false
}

resource "emqxcloud_authorization_user" "current" {
  provider = emqxcloud.deployment

  username = var.authorization_username
  rules_json = jsonencode([{
    permission = "allow"
    action     = "publish"
    topic      = "terraform/user/#"
  }])
}

resource "emqxcloud_authorization_client" "current" {
  provider = emqxcloud.deployment

  client_id = var.authorization_client_id
  rules_json = jsonencode([{
    permission = "allow"
    action     = "subscribe"
    topic      = "terraform/client/#"
  }])
}

resource "emqxcloud_banned" "current" {
  provider = emqxcloud.deployment

  as  = "clientid"
  who = var.banned_client_id
  config_json = jsonencode({
    reason = "Terraform Provider v0.1 preview"
  })
}

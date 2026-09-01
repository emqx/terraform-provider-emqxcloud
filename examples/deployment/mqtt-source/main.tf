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

resource "emqxcloud_connector" "mqtt" {
  provider = emqxcloud.deployment

  type = "mqtt"
  config_json = jsonencode({
    description = "Public MQTT broker for the Source example"
    enable      = true
    server      = "broker.emqx.io:1883"
  })
}

resource "emqxcloud_source" "mqtt" {
  provider = emqxcloud.deployment

  type = "mqtt"
  config_json = jsonencode({
    connector   = emqxcloud_connector.mqtt.name
    description = "Subscribe to EMQX ESP32 messages"
    enable      = true
    parameters = {
      qos   = 1
      topic = "emqx/esp32/#"
    }
  })
}

resource "emqxcloud_action" "mqtt" {
  provider = emqxcloud.deployment

  type = "mqtt"
  config_json = jsonencode({
    connector   = emqxcloud_connector.mqtt.name
    description = "Forward Source payload outside the subscribed topic tree"
    enable      = true
    parameters = {
      payload = "$${.payload}"
      qos     = 1
      retain  = false
      topic   = "terraform/emqxcloud/$${topic}"
    }
  })
}

resource "emqxcloud_rule" "mqtt" {
  provider = emqxcloud.deployment

  config_json = jsonencode({
    actions     = ["${emqxcloud_action.mqtt.type}:${emqxcloud_action.mqtt.name}"]
    description = "Forward MQTT Source messages through the MQTT Action"
    enable      = true
    sql         = "SELECT * FROM \"$bridges/mqtt:${emqxcloud_source.mqtt.name}\""
  })
}

output "mqtt_pipeline_identities" {
  value = {
    connector_name = emqxcloud_connector.mqtt.name
    source_name    = emqxcloud_source.mqtt.name
    action_name    = emqxcloud_action.mqtt.name
    rule_id        = emqxcloud_rule.mqtt.rule_id
  }
}

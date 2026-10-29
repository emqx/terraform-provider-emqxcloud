package provider

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func TestDeploymentConnectionRefresh(t *testing.T) {
	for _, body := range []string{
		`{"connection":{"address":"mqtt.example","apiEndpoint":"https://api.example/api/v5","ports":[{"protocol":"wss","port":8084,"status":"stopped"},{"protocol":"mqtts","port":8883,"status":"running"}]}}`,
		`{"connection":{"address":"mqtt.example","apiEndpoint":"https://api.example/api/v5","ports":[{"protocol":"mqtts","port":8883,"status":"running"},{"protocol":"wss","port":8084,"status":"stopped"}]}}`,
	} {
		var deployment deploymentAPIModel
		if err := json.Unmarshal([]byte(body), &deployment); err != nil {
			t.Fatal(err)
		}
		detail := terraformDeploymentDetail(deployment)
		managed := managedDeploymentModel{}
		updateManagedDeployment(&managed, deployment)
		if !detail.Connection.Equal(managed.Connection) {
			t.Fatal("resource and data source connection differ")
		}
		attributes := detail.Connection.Attributes()
		if !attributes["api_endpoint"].Equal(types.StringValue("https://api.example/api/v5")) {
			t.Fatal("API endpoint was not preserved")
		}
		ports := attributes["ports"].(types.List).Elements()
		first := ports[0].(types.Object).Attributes()
		second := ports[1].(types.Object).Attributes()
		if !first["port"].Equal(types.Int64Value(8883)) || !second["status"].Equal(types.StringValue("stopped")) {
			t.Fatal("port ordering or stopped listener was lost")
		}
		for _, absent := range []string{`{"connection":null}`, `{}`} {
			var refreshed deploymentAPIModel
			if err := json.Unmarshal([]byte(absent), &refreshed); err != nil {
				t.Fatal(err)
			}
			updateManagedDeployment(&managed, refreshed)
			if !managed.Connection.IsNull() || !terraformDeploymentDetail(refreshed).Connection.IsNull() {
				t.Fatal("refresh retained an unavailable connection")
			}
		}
	}
	if _, ok := deploymentAttributes(false, true)["connection_info"]; ok {
		t.Fatal("list data source must not expose connection")
	}
}

func TestDeploymentConnectionMissingFieldsAreNull(t *testing.T) {
	for _, body := range []string{`{"connection":{}}`, `{"connection":{"apiEndpoint":null}}`, `{"connection":{"apiEndpoint":""}}`} {
		var deployment deploymentAPIModel
		if err := json.Unmarshal([]byte(body), &deployment); err != nil {
			t.Fatal(err)
		}
		connection := terraformDeploymentConnection(deployment.Connection)
		for name, value := range connection.Attributes() {
			if !value.IsNull() {
				t.Fatalf("missing %s was not null", name)
			}
		}
	}
	var deployment deploymentAPIModel
	if err := json.Unmarshal([]byte(`{"connection":{"ports":[{}, {"protocol":"mqtts","port":8883,"status":"running"}]}}`), &deployment); err != nil {
		t.Fatal(err)
	}
	ports := terraformDeploymentConnection(deployment.Connection).Attributes()["ports"].(types.List).Elements()
	for name, value := range ports[0].(types.Object).Attributes() {
		if !value.IsNull() {
			t.Fatalf("missing listener %s was not null", name)
		}
	}
	if err := json.Unmarshal([]byte(`{"connection":{"ports":[]}}`), &deployment); err != nil {
		t.Fatal(err)
	}
	if ports := terraformDeploymentConnection(deployment.Connection).Attributes()["ports"].(types.List); ports.IsNull() || len(ports.Elements()) != 0 {
		t.Fatal("an explicit empty listener list must remain empty")
	}
}

func TestDeploymentStateWithoutConnectionUpgrades(t *testing.T) {
	server, err := testProviderFactories["emqxcloud"]()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	schema, err := server.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	upgraded, err := server.UpgradeResourceState(ctx, &tfprotov6.UpgradeResourceStateRequest{
		TypeName: "emqxcloud_deployment", Version: 0,
		RawState: &tfprotov6.RawState{JSON: []byte(`{"project_id":"project-1","platform":"aws","region":"us-east-1","connections":1000,"tps":1000,"deployment_name":"legacy","deployment_version":"v5","status":"running","deployment_id":"legacy-id","deployment_type":"dedicatedFlex"}`)},
	})
	if err != nil || upgraded.UpgradedState == nil {
		t.Fatalf("upgrade old deployment state: %v, %v", err, upgraded.Diagnostics)
	}
	value, err := upgraded.UpgradedState.Unmarshal(schema.ResourceSchemas["emqxcloud_deployment"].ValueType())
	if err != nil {
		t.Fatal(err)
	}
	var attributes map[string]tftypes.Value
	if err := value.As(&attributes); err != nil {
		t.Fatal(err)
	}
	var identity string
	if err := attributes["deployment_id"].As(&identity); err != nil || identity != "legacy-id" || !attributes["connection_info"].IsNull() {
		t.Fatal("old deployment identity or new nullable attribute was not preserved")
	}
}

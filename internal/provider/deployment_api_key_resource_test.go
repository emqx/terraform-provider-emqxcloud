package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func keyTestModel() deploymentAPIKeyModel {
	return deploymentAPIKeyModel{
		DeploymentID: types.StringValue("deployment/one"),
		Description:  types.StringValue("explicit permissions"),
		Whitelist: types.MapValueMust(types.SetType{ElemType: types.StringType}, map[string]attr.Value{
			"/api/v5/clients": types.SetValueMust(types.StringType, []attr.Value{types.StringValue("GET")}),
		}),
		AppID: types.StringUnknown(), AppSecret: types.StringUnknown(),
	}
}

func keyTestState(t *testing.T, model deploymentAPIKeyModel) tfsdk.State {
	t.Helper()
	var schema resource.SchemaResponse
	(&deploymentAPIKeyResource{}).Schema(context.Background(), resource.SchemaRequest{}, &schema)
	if !schema.Schema.Attributes["app_secret"].IsSensitive() {
		t.Fatal("issued secrets must be Sensitive")
	}
	state := tfsdk.State{Schema: schema.Schema}
	if diagnostics := state.Set(context.Background(), &model); diagnostics.HasError() {
		t.Fatal(diagnostics)
	}
	return state
}

func TestDeploymentAPIKeyCreateAndRetain(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		retain bool
	}{
		{"success", 201, `{"appID":"issued-ak","appSecret":"issued-sk"}`, true},
		{"forbidden", 403, `{"appSecret":"never-log-this"}`, false},
		{"rate limit", 429, `{"appSecret":"never-log-this"}`, false},
		{"server error", 500, `{"appSecret":"never-log-this"}`, true},
		{"missing secret", 201, `{"appID":"issued-ak"}`, true},
		{"missing identity", 201, `{"appSecret":"never-log-this"}`, true},
		{"masked secret", 201, `{"appID":"issued-ak","appSecret":"******"}`, true},
		{"wrong response type", 201, `{"appID":"issued-ak","appSecret":42}`, true},
		{"unexpected status", 200, `{"appID":"issued-ak","appSecret":"never-log-this"}`, true},
		{"lost response", 0, "", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					if r.URL.EscapedPath() != "/deployments/deployment%2Fone" {
						t.Errorf("unescaped deployment identity: %s", r.URL.EscapedPath())
					}
					writeJSON(w, 200, map[string]any{"deploymentID": "deployment/one", "deploymentType": "dedicatedFlex", "status": "running"})
					return
				}
				posts.Add(1)
				if r.Method != http.MethodPost || r.URL.EscapedPath() != "/deployments/deployment%2Fone/applications" {
					t.Errorf("unexpected write: %s %s", r.Method, r.URL.EscapedPath())
				}
				var payload createDeploymentAPIKeyRequest
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				if payload.Description != "explicit permissions" || !reflect.DeepEqual(payload.Whitelist, map[string][]string{"/api/v5/clients": {"GET"}}) {
					t.Error("explicit permission payload was not preserved")
				}
				if test.status == 0 {
					connection, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = connection.Close()
					return
				}
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			r := &deploymentAPIKeyResource{platform: testAPIClient(t, server.URL)}
			state := keyTestState(t, keyTestModel())
			response := resource.CreateResponse{State: tfsdk.State{Schema: state.Schema}}
			r.Create(context.Background(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: state.Schema, Raw: state.Raw}}, &response)
			if (test.name == "success") == response.Diagnostics.HasError() {
				t.Fatalf("unexpected create diagnostics: %v", response.Diagnostics)
			}
			for _, diagnostic := range response.Diagnostics {
				message := diagnostic.Summary() + diagnostic.Detail()
				if strings.Contains(message, "never-log-this") || strings.Contains(message, "issued-sk") {
					t.Fatal("create diagnostics disclosed credentials")
				}
			}
			if test.name == "unexpected status" && !strings.Contains(response.Diagnostics[0].Detail(), "terraform untaint") {
				t.Fatal("complete issuance receipt has no taint recovery guidance")
			}
			if test.name == "missing secret" && strings.Contains(response.Diagnostics[0].Detail(), "terraform untaint") {
				t.Fatal("an incomplete issuance must not suggest untaint as recovery")
			}
			if posts.Load() != 1 {
				t.Fatalf("POST attempts: %d", posts.Load())
			}
			if !test.retain {
				if !response.State.Raw.IsNull() {
					t.Fatal("a confirmed rejection must not be recorded as an issuance")
				}
				return
			}
			var retained deploymentAPIKeyModel
			if diagnostics := response.State.Get(context.Background(), &retained); diagnostics.HasError() {
				t.Fatal(diagnostics)
			}
			if retained.DeploymentID.ValueString() != "deployment/one" || retained.AppID.IsUnknown() || retained.AppSecret.IsUnknown() {
				t.Fatal("issuance receipt was lost or contains unknown state")
			}
			read := resource.ReadResponse{State: response.State}
			r.Read(context.Background(), resource.ReadRequest{State: response.State}, &read)
			if (test.name == "success" || test.name == "unexpected status") == read.Diagnostics.HasError() {
				t.Fatalf("unexpected refresh diagnostics: %v", read.Diagnostics)
			}
			if !response.State.Raw.Equal(read.State.Raw) || posts.Load() != 1 {
				t.Fatal("refresh changed credentials or reissued a key")
			}
		})
	}
}

func TestDeploymentAPIKeyConcurrentCreatesRetainBothKeys(t *testing.T) {
	var mu sync.Mutex
	persisted := []string{"existing-key"}
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, map[string]any{"deploymentID": "deployment/one", "deploymentType": "dedicatedFlex", "status": "running"})
			return
		}
		if request.Method != http.MethodPost || request.URL.EscapedPath() != "/deployments/deployment%2Fone/applications" {
			t.Errorf("unexpected write: %s %s", request.Method, request.URL.EscapedPath())
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var payload createDeploymentAPIKeyRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		// Model the backend's read/modify/write snapshots without serializing whole requests.
		mu.Lock()
		snapshot := append([]string(nil), persisted...)
		mu.Unlock()
		entered <- struct{}{}
		select {
		case <-release:
		case <-request.Context().Done():
			return
		}
		mu.Lock()
		persisted = append(snapshot, payload.Description)
		mu.Unlock()
		writeJSON(w, http.StatusCreated, deploymentAPIKeyResponse{AppID: payload.Description, AppSecret: "secret-" + payload.Description})
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	responses := make(chan resource.CreateResponse, 2)
	create := func(id string) {
		model := keyTestModel()
		model.Description = types.StringValue(id)
		state := keyTestState(t, model)
		// Independent resources and clients also cover aliases sharing one Provider process.
		r := &deploymentAPIKeyResource{platform: testAPIClient(t, server.URL)}
		go func() {
			response := resource.CreateResponse{State: tfsdk.State{Schema: state.Schema}}
			r.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: state.Schema, Raw: state.Raw}}, &response)
			responses <- response
		}()
	}
	create("key-a")
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("first issuance did not reach the API")
	}
	create("key-b")
	select {
	case <-entered:
	case <-time.After(200 * time.Millisecond):
	case <-ctx.Done():
		t.Fatal("timed out waiting for concurrent issuance")
	}
	close(release)
	for range 2 {
		select {
		case response := <-responses:
			if response.Diagnostics.HasError() {
				t.Fatal(response.Diagnostics)
			}
			var state deploymentAPIKeyModel
			if diagnostics := response.State.Get(ctx, &state); diagnostics.HasError() {
				t.Fatal(diagnostics)
			}
			if state.AppID.ValueString() != state.Description.ValueString() || state.AppSecret.ValueString() != "secret-"+state.AppID.ValueString() {
				t.Fatal("issued credentials were not retained in their resource state")
			}
		case <-ctx.Done():
			t.Fatal("concurrent creates did not complete")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(persisted, []string{"existing-key", "key-a", "key-b"}) {
		t.Fatalf("successful issuance overwrote remote keys: %v", persisted)
	}
}

func TestDeploymentAPIKeyWaitingCreateCanCancel(t *testing.T) {
	var requests atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, map[string]any{"deploymentID": "deployment/one", "deploymentType": "dedicatedFlex", "status": "running"})
			return
		}
		close(entered)
		select {
		case <-release:
		case <-request.Context().Done():
			return
		}
		writeJSON(w, http.StatusCreated, deploymentAPIKeyResponse{AppID: "issued-ak", AppSecret: "issued-sk"})
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	state := keyTestState(t, keyTestModel())
	plan := resource.CreateRequest{Plan: tfsdk.Plan{Schema: state.Schema, Raw: state.Raw}}
	first := &deploymentAPIKeyResource{platform: testAPIClient(t, server.URL)}
	done := make(chan resource.CreateResponse, 1)
	go func() {
		response := resource.CreateResponse{State: tfsdk.State{Schema: state.Schema}}
		first.Create(ctx, plan, &response)
		done <- response
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("first issuance did not reach the API")
	}
	waitCtx, cancelWait := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancelWait()
	second := &deploymentAPIKeyResource{platform: testAPIClient(t, server.URL)}
	response := resource.CreateResponse{State: tfsdk.State{Schema: state.Schema}}
	second.Create(waitCtx, plan, &response)
	if !response.Diagnostics.HasError() || !response.State.Raw.IsNull() || requests.Load() != 2 {
		t.Fatal("a cancelled waiting create sent a request or recorded an issuance")
	}
	close(release)
	select {
	case firstResponse := <-done:
		if firstResponse.Diagnostics.HasError() {
			t.Fatal(firstResponse.Diagnostics)
		}
	case <-ctx.Done():
		t.Fatal("cancelling a waiting create blocked the active issuance")
	}
}

func TestDeploymentAPIKeyRejectsUnreadyDeployment(t *testing.T) {
	for _, body := range []string{
		`{"deploymentID":"other","deploymentType":"dedicatedFlex","status":"running"}`,
		`{"deploymentID":"deployment/one","deploymentType":"serverless","status":"running"}`,
		`{"deploymentID":"deployment/one","deploymentType":"dedicatedFlex","status":"stopped"}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Error("unready deployment caused a write")
			}
			_, _ = w.Write([]byte(body))
		}))
		state := keyTestState(t, keyTestModel())
		r := &deploymentAPIKeyResource{platform: testAPIClient(t, server.URL)}
		response := resource.CreateResponse{State: tfsdk.State{Schema: state.Schema}}
		r.Create(context.Background(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: state.Schema, Raw: state.Raw}}, &response)
		server.Close()
		if !response.Diagnostics.HasError() || !response.State.Raw.IsNull() {
			t.Fatal("unready deployment was accepted")
		}
	}
}

func TestDeploymentAPIKeyValidation(t *testing.T) {
	for _, invalid := range []types.Map{
		types.MapNull(types.SetType{ElemType: types.StringType}),
		types.MapValueMust(types.SetType{ElemType: types.StringType}, map[string]attr.Value{}),
		types.MapValueMust(types.SetType{ElemType: types.StringType}, map[string]attr.Value{"": types.SetValueMust(types.StringType, []attr.Value{types.StringValue("GET")})}),
		types.MapValueMust(types.SetType{ElemType: types.StringType}, map[string]attr.Value{"/api/v5/clients": types.SetNull(types.StringType)}),
		types.MapValueMust(types.SetType{ElemType: types.StringType}, map[string]attr.Value{"/api/v5/clients": types.SetValueMust(types.StringType, []attr.Value{})}),
		types.MapValueMust(types.SetType{ElemType: types.StringType}, map[string]attr.Value{"/api/v5/clients": types.SetValueMust(types.StringType, []attr.Value{types.StringNull()})}),
		types.MapValueMust(types.SetType{ElemType: types.StringType}, map[string]attr.Value{"/api/v5/clients": types.SetValueMust(types.StringType, []attr.Value{types.StringValue("get")})}),
	} {
		model := keyTestModel()
		model.Whitelist = invalid
		var diagnostics diag.Diagnostics
		if model.payload(&diagnostics) != nil || !diagnostics.HasError() {
			t.Fatal("invalid permissions could fall back to an unrestricted issuance")
		}
	}
	model := keyTestModel()
	model.Whitelist = types.MapUnknown(types.SetType{ElemType: types.StringType})
	var diagnostics diag.Diagnostics
	if model.payload(&diagnostics) != nil || diagnostics.HasError() {
		t.Fatal("unknown permissions must wait for apply")
	}
}

func TestDeploymentAPIKeyInvalidConfigMakesNoRequests(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()
	for _, field := range []string{"permissions", "identity", "description"} {
		model := keyTestModel()
		switch field {
		case "permissions":
			model.Whitelist = types.MapNull(types.SetType{ElemType: types.StringType})
		case "identity":
			model.DeploymentID = types.StringValue(" ")
		case "description":
			model.Description = types.StringValue(strings.Repeat("a", 129))
		}
		state := keyTestState(t, model)
		response := resource.CreateResponse{State: tfsdk.State{Schema: state.Schema}}
		r := &deploymentAPIKeyResource{platform: testAPIClient(t, server.URL)}
		r.Create(context.Background(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: state.Schema, Raw: state.Raw}}, &response)
		if !response.Diagnostics.HasError() || requests.Load() != 0 {
			t.Fatalf("invalid %s caused an API request", field)
		}
	}
}

func TestDeploymentAPIKeyGuards(t *testing.T) {
	r := &deploymentAPIKeyResource{}
	model := keyTestModel()
	model.AppID, model.AppSecret = types.StringValue("ak"), types.StringValue("sk")
	state := keyTestState(t, model)
	for _, change := range []string{"deployment", "description", "permissions", "unknown deployment", "unknown description", "unknown permissions", "delete"} {
		changed := model
		switch change {
		case "deployment":
			changed.DeploymentID = types.StringValue("other")
		case "description":
			changed.Description = types.StringValue("other")
		case "unknown deployment":
			changed.DeploymentID = types.StringUnknown()
		case "unknown description":
			changed.Description = types.StringUnknown()
		case "unknown permissions":
			changed.Whitelist = types.MapUnknown(types.SetType{ElemType: types.StringType})
		case "permissions":
			changed.Whitelist = types.MapValueMust(types.SetType{ElemType: types.StringType}, map[string]attr.Value{
				"/api/v5/rules": types.SetValueMust(types.StringType, []attr.Value{types.StringValue("GET")}),
			})
		}
		plan := keyTestState(t, changed)
		if change == "delete" {
			plan.Raw = tftypes.NewValue(state.Raw.Type(), nil)
		}
		response := resource.ModifyPlanResponse{}
		r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{State: state, Plan: tfsdk.Plan{Schema: state.Schema, Raw: plan.Raw}}, &response)
		if !response.Diagnostics.HasError() {
			t.Fatalf("unguarded change: %s", change)
		}
	}
	var deleted resource.DeleteResponse
	r.Delete(context.Background(), resource.DeleteRequest{}, &deleted)
	var updated resource.UpdateResponse
	r.Update(context.Background(), resource.UpdateRequest{}, &updated)
	if !deleted.Diagnostics.HasError() || !updated.Diagnostics.HasError() {
		t.Fatal("update or delete bypassed the lifecycle guard")
	}
}

func TestDeploymentAPIKeyRequiresDestroyPlanning(t *testing.T) {
	for version, supported := range map[string]bool{
		"0.15.5": false, "1.0.11": false, "1.2.9": false, "unknown": false, "": false,
		"1.3.0": true, "1.15.8": true, "1.17.0": true, "2.0.0": true,
	} {
		var response resource.ConfigureResponse
		(&deploymentAPIKeyResource{}).Configure(context.Background(), resource.ConfigureRequest{
			ProviderData: &ProviderData{TerraformVersion: version},
		}, &response)
		if supported == response.Diagnostics.HasError() {
			t.Fatalf("unexpected Key version guard for %q", version)
		}
	}
}

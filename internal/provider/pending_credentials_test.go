package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	frameworkprovider "github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestProviderTracksPendingCredentials(t *testing.T) {
	for _, name := range []string{"EMQXCLOUD_PLATFORM_ENDPOINT", "EMQXCLOUD_PLATFORM_API_KEY", "EMQXCLOUD_PLATFORM_API_SECRET", "EMQXCLOUD_DEPLOYMENT_ENDPOINT", "EMQXCLOUD_DEPLOYMENT_API_KEY", "EMQXCLOUD_DEPLOYMENT_API_SECRET"} {
		t.Setenv(name, "")
	}
	p := &emqxCloudProvider{}
	var schema frameworkprovider.SchemaResponse
	p.Schema(context.Background(), frameworkprovider.SchemaRequest{}, &schema)
	state := tfsdk.State{Schema: schema.Schema}
	model := providerModel{PlatformEndpoint: types.StringUnknown(), DeploymentEndpoint: types.StringUnknown()}
	if diagnostics := state.Set(context.Background(), &model); diagnostics.HasError() {
		t.Fatal(diagnostics)
	}
	var configured frameworkprovider.ConfigureResponse
	p.Configure(context.Background(), frameworkprovider.ConfigureRequest{
		Config: tfsdk.Config{Schema: schema.Schema, Raw: state.Raw}, TerraformVersion: "1.15.8",
	}, &configured)
	if configured.Diagnostics.HasError() {
		t.Fatal(configured.Diagnostics)
	}
	data := configured.ResourceData.(*ProviderData)
	if !data.PlatformPending || !data.DeploymentPending || data.Platform != nil || data.Deployment != nil {
		t.Fatal("unknown credentials were treated as absent or configured")
	}
	d := newProjectsDataSource().(*projectsDataSource)
	var configure datasource.ConfigureResponse
	d.Configure(context.Background(), datasource.ConfigureRequest{ProviderData: data}, &configure)
	var read datasource.ReadResponse
	d.Read(context.Background(), datasource.ReadRequest{}, &read)
	if !read.Diagnostics.HasError() || read.Diagnostics[0].Summary() != "Platform API credentials are pending" {
		t.Fatalf("misleading Platform diagnostic: %v", read.Diagnostics)
	}
}

func TestDeploymentResourcesRetainStateOnlyWhileCredentialsPending(t *testing.T) {
	ctx := context.Background()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	data := &ProviderData{Deployment: testAPIClient(t, server.URL), DeploymentPending: true}
	for _, test := range []struct {
		name    string
		factory func() resource.Resource
		model   any
	}{
		{"connector", newConnectorResource, namedJSONResourceModel{Type: types.StringValue("http"), Name: types.StringValue("c-abc123"), ConfigJSON: types.StringValue(`{"url":"https://example.com"}`)}},
		{"user", newAuthenticationUserResource, authenticationUserModel{UserID: types.StringValue("user"), Password: types.StringValue("password"), IsSuperuser: types.BoolValue(false)}},
		{"authorization", newAuthorizationUserResource, authorizationUserModel{Username: types.StringValue("user"), RulesJSON: types.StringValue(`[]`)}},
		{"banned", newBannedResource, bannedModel{As: types.StringValue("username"), Who: types.StringValue("user"), ConfigJSON: types.StringValue(`{}`)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := test.factory()
			var schema resource.SchemaResponse
			r.Schema(ctx, resource.SchemaRequest{}, &schema)
			state := tfsdk.State{Schema: schema.Schema}
			if diagnostics := state.Set(ctx, test.model); diagnostics.HasError() {
				t.Fatal(diagnostics)
			}
			var configure resource.ConfigureResponse
			r.(resource.ResourceWithConfigure).Configure(ctx, resource.ConfigureRequest{ProviderData: data}, &configure)
			read := resource.ReadResponse{State: tfsdk.State{Schema: schema.Schema}}
			r.Read(ctx, resource.ReadRequest{State: state}, &read)
			if read.Diagnostics.HasError() || len(read.Diagnostics) == 0 || !read.State.Raw.Equal(state.Raw) || requests.Load() != 0 {
				t.Fatal("pending refresh did not retain exact prior state with a warning")
			}
			var create resource.CreateResponse
			r.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: schema.Schema, Raw: state.Raw}}, &create)
			if !create.Diagnostics.HasError() || !strings.Contains(create.Diagnostics[0].Summary(), "pending") || requests.Load() != 0 {
				t.Fatal("pending credentials permitted a write or produced a misleading diagnostic")
			}
		})
	}
	a := newResetAuthorizationCacheAction().(*resetAuthorizationCacheAction)
	var configure action.ConfigureResponse
	a.Configure(ctx, action.ConfigureRequest{ProviderData: data}, &configure)
	var invoke action.InvokeResponse
	a.Invoke(ctx, action.InvokeRequest{}, &invoke)
	if !invoke.Diagnostics.HasError() || !strings.Contains(invoke.Diagnostics[0].Summary(), "pending") || requests.Load() != 0 {
		t.Fatal("pending credentials permitted an Action")
	}

	r := newAuthenticationUserResource().(*authenticationUserResource)
	var schema resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schema)
	state := tfsdk.State{Schema: schema.Schema}
	if diagnostics := state.Set(ctx, &authenticationUserModel{UserID: types.StringValue("user"), Password: types.StringValue("password")}); diagnostics.HasError() {
		t.Fatal(diagnostics)
	}
	var configured resource.ConfigureResponse
	r.Configure(ctx, resource.ConfigureRequest{ProviderData: &ProviderData{Deployment: data.Deployment}}, &configured)
	read := resource.ReadResponse{State: state}
	r.Read(ctx, resource.ReadRequest{State: state}, &read)
	if !read.Diagnostics.HasError() || requests.Load() != 1 {
		t.Fatal("a real authentication failure was hidden as a deferred refresh")
	}
}

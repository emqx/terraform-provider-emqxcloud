package provider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"
)

func TestResetAuthorizationCacheActionInvokesBodylessPost(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.Method != http.MethodPost {
			t.Errorf("unexpected method %s", request.Method)
		}
		if request.URL.EscapedPath() != "/authorization/node_cache/reset" {
			t.Errorf("unexpected path %s", request.URL.EscapedPath())
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		if len(body) != 0 {
			t.Errorf("expected empty request body, got %q", body)
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	cacheAction := newResetAuthorizationCacheAction().(*resetAuthorizationCacheAction)
	var configureResponse action.ConfigureResponse
	cacheAction.Configure(context.Background(), action.ConfigureRequest{
		ProviderData: &ProviderData{Deployment: testAPIClient(t, server.URL)},
	}, &configureResponse)
	if configureResponse.Diagnostics.HasError() {
		t.Fatalf("configure action: %v", configureResponse.Diagnostics.Errors())
	}

	var invokeResponse action.InvokeResponse
	cacheAction.Invoke(context.Background(), action.InvokeRequest{}, &invokeResponse)
	if invokeResponse.Diagnostics.HasError() {
		t.Fatalf("invoke action: %v", invokeResponse.Diagnostics.Errors())
	}
	if requests.Load() != 1 {
		t.Fatalf("expected one request, got %d", requests.Load())
	}
}

func TestResetAuthorizationCacheActionRunsThroughTerraform(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		apiKey, apiSecret, ok := request.BasicAuth()
		if !ok || apiKey != "key" || apiSecret != "secret" {
			t.Error("unexpected Basic Auth credentials")
		}
		if request.Method != http.MethodPost ||
			request.URL.EscapedPath() != "/api/v5/authorization/node_cache/reset" {
			t.Errorf("unexpected request %s %s", request.Method, request.URL.EscapedPath())
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		TerraformVersionChecks: []tfversion.TerraformVersionCheck{
			tfversion.SkipBelow(tfversion.Version1_14_0),
		},
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
terraform {
  required_version = ">= 1.14"
}

provider "emqxcloud" {
  deployment_endpoint   = %q
  deployment_api_key    = "key"
  deployment_api_secret = "secret"
}

resource "terraform_data" "trigger" {
  lifecycle {
    action_trigger {
      events  = [after_create]
      actions = [action.emqxcloud_reset_authorization_cache.current]
    }
  }
}

action "emqxcloud_reset_authorization_cache" "current" {}
`, server.URL+"/api/v5"),
			PostApplyFunc: func() {
				if requests.Load() != 1 {
					t.Errorf("expected one request, got %d", requests.Load())
				}
			},
		}},
	})
}

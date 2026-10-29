package provider

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestProviderBootstrapOneApply(t *testing.T) {
	for _, failFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("recover_downstream_%t", failFirst), func(t *testing.T) {
			t.Setenv("EMQXCLOUD_DEPLOYMENT_ENDPOINT", "https://stale.example/api/v5")
			t.Setenv("EMQXCLOUD_DEPLOYMENT_API_KEY", "stale-key")
			t.Setenv("EMQXCLOUD_DEPLOYMENT_API_SECRET", "stale-secret")
			unused := newMockProviderAPI()
			unusedServer := httptest.NewServer(unused)
			defer unusedServer.Close()
			api := newMockProviderAPI()
			if failFirst {
				api.userCreateFailures = 1
			}
			server := httptest.NewServer(api)
			defer server.Close()
			api.deploymentEndpoint = server.URL + "/api/v5"
			checkIssuance := func(_ *terraform.State) error {
				api.mu.Lock()
				defer api.mu.Unlock()
				if api.deploymentCreates != 1 || api.apiKeyCreates != 1 || api.deploymentDeletes != 0 {
					return fmt.Errorf("unexpected deployment/key creation or delete counts: %d/%d/%d", api.deploymentCreates, api.apiKeyCreates, api.deploymentDeletes)
				}
				expected := map[string][]string{
					"/api/v5" + authenticationUsersPath: {"DELETE", "GET", "POST", "PUT"},
				}
				if api.apiKeyPayload == nil || !reflect.DeepEqual(api.apiKeyPayload.Whitelist, expected) {
					return fmt.Errorf("explicit permissions were not preserved")
				}
				return nil
			}
			config := bootstrapConfig(unusedServer.URL, server.URL, "password-1", true)
			updated := bootstrapConfig(unusedServer.URL, server.URL, "password-2", true)
			steps := []resource.TestStep{}
			if failFirst {
				steps = append(steps, resource.TestStep{Config: config, ExpectError: regexp.MustCompile("Unable to create authentication user")})
			}
			steps = append(steps,
				resource.TestStep{Config: config, Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("emqxcloud_deployment.current", "connection_info.api_endpoint", server.URL+"/api/v5"),
					resource.TestCheckResourceAttr("emqxcloud_deployment_api_key.current", "app_id", "new-deployment-ak"),
					resource.TestCheckResourceAttr("emqxcloud_deployment_api_key.current", "app_secret", "new-deployment-sk"),
					resource.TestCheckResourceAttr("emqxcloud_authentication_user.current", "user_id", "bootstrap-user"),
					checkIssuance,
				)},
				resource.TestStep{Config: config, PlanOnly: true, Check: checkIssuance},
				resource.TestStep{Config: updated, Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("emqxcloud_authentication_user.current", "password", "password-2"), checkIssuance,
				)},
				resource.TestStep{Config: strings.Replace(updated, `description = "bootstrap"`, `description = "changed"`, 1), ExpectError: regexp.MustCompile("Deployment API key changes are not supported")},
				resource.TestStep{Config: bootstrapConfig(unusedServer.URL, server.URL, "password-2", false), Check: func(state *terraform.State) error {
					if err := checkIssuance(state); err != nil {
						return err
					}
					api.mu.Lock()
					defer api.mu.Unlock()
					if len(api.authenticationUsers) != 0 {
						return fmt.Errorf("bootstrap user was not deleted")
					}
					return nil
				}},
				resource.TestStep{Config: deploymentConfig(unusedServer.URL, server.URL, "running", 1000), ExpectError: regexp.MustCompile("Deployment API key deletion is not supported")},
				resource.TestStep{Config: strings.Replace(bootstrapConfig(unusedServer.URL, server.URL, "password-2", false), `description = "bootstrap"`, "description = \"bootstrap\"\n  lifecycle { create_before_destroy = true }", 1), Taint: []string{"emqxcloud_deployment_api_key.current"}, ExpectError: regexp.MustCompile("Deployment API key deletion is not supported")},
				resource.TestStep{Config: bootstrapConfig(unusedServer.URL, server.URL, "password-2", false), Taint: []string{"emqxcloud_deployment_api_key.current"}, ExpectError: regexp.MustCompile("Deployment API key deletion is not supported")},
				resource.TestStep{Config: deploymentRemovedConfig(unusedServer.URL, server.URL) + `
removed {
  from = emqxcloud_deployment_api_key.current
  lifecycle { destroy = false }
}
`},
			)
			resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testProviderFactories, Steps: steps, CheckDestroy: checkIssuance})
			if err := unused.checkNoDeploymentCreates(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func bootstrapConfig(unused, target, password string, includeUser bool) string {
	config := deploymentConfig(unused, target, "running", 1000) + `
resource "emqxcloud_deployment_api_key" "current" {
  provider = emqxcloud.target
  deployment_id = emqxcloud_deployment.current.deployment_id
  description = "bootstrap"
  whitelist = {
    "/api/v5/authentication/password_based:built_in_database/users" = ["GET", "POST", "PUT", "DELETE"]
  }
}

provider "emqxcloud" {
  alias = "deployment"
  deployment_endpoint = emqxcloud_deployment.current.connection_info.api_endpoint
  deployment_api_key = emqxcloud_deployment_api_key.current.app_id
  deployment_api_secret = emqxcloud_deployment_api_key.current.app_secret
}
`
	if includeUser {
		config += fmt.Sprintf(`
resource "emqxcloud_authentication_user" "current" {
  provider = emqxcloud.deployment
  user_id = "bootstrap-user"
  password = %q
}
`, password)
	}
	return config
}

func bootstrapRemovedConfig(endpoint string) string {
	return deploymentRemovedConfig(endpoint, endpoint) + `
removed {
  from = emqxcloud_deployment_api_key.current
  lifecycle { destroy = false }
}
`
}

func TestProviderBootstrapMissingEndpointDoesNotUseEnvironment(t *testing.T) {
	var staleRequests atomic.Int32
	stale := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		staleRequests.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer stale.Close()
	t.Setenv("EMQXCLOUD_DEPLOYMENT_ENDPOINT", stale.URL+"/api/v5")
	t.Setenv("EMQXCLOUD_DEPLOYMENT_API_KEY", "stale-key")
	t.Setenv("EMQXCLOUD_DEPLOYMENT_API_SECRET", "stale-secret")
	api := newMockProviderAPI()
	api.omitDeploymentAPIEndpoint = true
	server := httptest.NewServer(api)
	defer server.Close()
	api.deploymentEndpoint = server.URL + "/api/v5"
	config := bootstrapConfig(server.URL, server.URL, "password", true)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{Config: config, ExpectError: regexp.MustCompile("must be configured together")},
			{PreConfig: func() { api.mu.Lock(); api.omitDeploymentAPIEndpoint = false; api.mu.Unlock() }, Config: config},
			{Config: bootstrapConfig(server.URL, server.URL, "password", false)},
			{Config: bootstrapRemovedConfig(server.URL)},
		},
	})
	api.mu.Lock()
	defer api.mu.Unlock()
	if staleRequests.Load() != 0 || api.deploymentCreates != 1 || api.apiKeyCreates != 1 {
		t.Fatalf("unsafe fallback or repeated issuance: stale requests=%d deployments=%d keys=%d", staleRequests.Load(), api.deploymentCreates, api.apiKeyCreates)
	}
}

func TestProviderBootstrapDeletedDeploymentBlocksBeforeCreation(t *testing.T) {
	api := newMockProviderAPI()
	server := httptest.NewServer(api)
	defer server.Close()
	api.deploymentEndpoint = server.URL + "/api/v5"
	config := bootstrapConfig(server.URL, server.URL, "unused", false)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{PreConfig: func() {
				api.mu.Lock()
				api.deployment = map[string]any{}
				api.deploymentCreateID = "deployment-recreated"
				api.mu.Unlock()
			}, Config: config, ExpectError: regexp.MustCompile("Deployment API key changes are not supported")},
			{Config: bootstrapRemovedConfig(server.URL)},
		},
	})
	api.mu.Lock()
	defer api.mu.Unlock()
	if api.deploymentCreates != 1 || api.apiKeyCreates != 1 {
		t.Fatalf("plan guard allowed another write: deployments=%d keys=%d", api.deploymentCreates, api.apiKeyCreates)
	}
}

func TestProviderBootstrapRecoversStoppedDeployment(t *testing.T) {
	for _, external := range []bool{true, false} {
		t.Run(fmt.Sprintf("external_%t", external), func(t *testing.T) {
			api := newMockProviderAPI()
			server := httptest.NewServer(api)
			defer server.Close()
			api.deploymentEndpoint = server.URL + "/api/v5"
			config := bootstrapConfig(server.URL, server.URL, "password", true)
			stop := strings.Replace(config, `status             = "running"`, `status             = "stopped"`, 1)
			change := resource.TestStep{Config: stop, PlanOnly: true, ExpectNonEmptyPlan: true}
			if external {
				change = resource.TestStep{PreConfig: func() { api.mu.Lock(); api.deployment["status"] = "stopped"; api.mu.Unlock() }, Config: config}
			}
			withoutUser := bootstrapConfig(server.URL, server.URL, "password", false)
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testProviderFactories,
				Steps: []resource.TestStep{
					{Config: config},
					change,
					{Config: config, PlanOnly: true},
					{Config: withoutUser},
					{Config: strings.Replace(withoutUser, `status             = "running"`, `status             = "stopped"`, 1)},
					{Config: withoutUser},
					{Config: bootstrapRemovedConfig(server.URL)},
				},
			})
			api.mu.Lock()
			defer api.mu.Unlock()
			if api.deploymentCreates != 1 || api.apiKeyCreates != 1 || api.deploymentDeletes != 0 {
				t.Fatal("stop recovery changed deployment or key identity")
			}
		})
	}
}

func TestProviderBootstrapUncertainKeyDoesNotReissue(t *testing.T) {
	for _, lostResponse := range []bool{false, true} {
		t.Run(fmt.Sprintf("lost_response_%t", lostResponse), func(t *testing.T) {
			api := newMockProviderAPI()
			api.omitIssuedSecret = !lostResponse
			api.dropIssuedResponse = lostResponse
			server := httptest.NewServer(api)
			defer server.Close()
			api.deploymentEndpoint = server.URL + "/api/v5"
			config := bootstrapConfig(server.URL, server.URL, "unused", false)
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testProviderFactories,
				Steps: []resource.TestStep{
					{Config: config, ExpectError: regexp.MustCompile("Unable to create Deployment API key")},
					{Config: config, ExpectError: regexp.MustCompile("Deployment API key issuance is incomplete")},
					{Config: deploymentRemovedConfig(server.URL, server.URL) + `
removed {
  from = emqxcloud_deployment_api_key.current
  lifecycle { destroy = false }
}
`},
				},
			})
			api.mu.Lock()
			defer api.mu.Unlock()
			if api.apiKeyCreates != 1 || api.deploymentCreates != 1 || api.deploymentDeletes != 0 {
				t.Fatal("uncertain issuance caused another write")
			}
		})
	}
}

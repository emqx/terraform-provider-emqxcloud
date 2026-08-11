package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

type mockProviderAPI struct {
	mu                     sync.Mutex
	deployment             map[string]any
	deploymentCreate       map[string]any
	operations             []string
	deploymentDeletes      int
	createFailure          bool
	operationFailure       bool
	deploymentReadFailures int
	ignoreDeploymentName   bool
	tls                    map[string]any
	connectors             map[string]map[string]any
	actions                map[string]map[string]any
	rules                  map[string]map[string]any
	authenticationUsers    map[string]map[string]any
	authorizationUsers     map[string]map[string]any
	authorizationClients   map[string]map[string]any
	banned                 map[string]map[string]any
	unsafeRequests         []string
	tlsReadFailures        int
	emqxReadFailures       int
}

var testProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"emqxcloud": providerserver.NewProtocol6WithError(&emqxCloudProvider{version: "test", allowHTTP: true}),
}

func TestProviderDeploymentCreateFailureDoesNotLeaveState(t *testing.T) {
	mockAPI := newMockProviderAPI()
	mockAPI.createFailure = true
	server := httptest.NewServer(mockAPI)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{{
			Config:      deploymentConfig(server.URL, server.URL, "running", 1000),
			ExpectError: regexp.MustCompile("Unable to create deployment"),
		}},
	})
}

func TestProviderDeploymentOperationFailureKeepsRemoteDeployment(t *testing.T) {
	mockAPI := newMockProviderAPI()
	server := httptest.NewServer(mockAPI)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy: func(_ *terraform.State) error {
			return mockAPI.checkDeploymentNotDeleted()
		},
		Steps: []resource.TestStep{
			{
				Config: deploymentConfig(server.URL, server.URL, "running", 1000),
			},
			{
				PreConfig: func() {
					mockAPI.mu.Lock()
					mockAPI.operationFailure = true
					mockAPI.mu.Unlock()
				},
				Config:      deploymentConfig(server.URL, server.URL, "stopped", 1000),
				ExpectError: regexp.MustCompile("Unable to change deployment status"),
			},
			{
				Config: deploymentRemovedConfig(server.URL, server.URL),
			},
		},
	})
}

func TestProviderDeploymentCreatePollingFailureKeepsState(t *testing.T) {
	mockAPI := newMockProviderAPI()
	mockAPI.deploymentReadFailures = 3
	server := httptest.NewServer(mockAPI)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy: func(_ *terraform.State) error {
			return mockAPI.checkDeploymentNotDeleted()
		},
		Steps: []resource.TestStep{
			{
				Config:      deploymentConfig(server.URL, server.URL, "running", 1000),
				ExpectError: regexp.MustCompile("Deployment creation did not complete"),
			},
			{
				Config: deploymentRemovedConfig(server.URL, server.URL),
			},
		},
	})
}

func TestProviderDeploymentLifecycleUsesSelectedPlatformAlias(t *testing.T) {
	unusedAPI := newMockProviderAPI()
	unusedServer := httptest.NewServer(unusedAPI)
	defer unusedServer.Close()
	targetAPI := newMockProviderAPI()
	targetAPI.ignoreDeploymentName = true
	targetServer := httptest.NewServer(targetAPI)
	defer targetServer.Close()

	runningConfig := deploymentConfig(unusedServer.URL, targetServer.URL, "running", 1000)
	stoppedConfig := deploymentConfig(unusedServer.URL, targetServer.URL, "stopped", 1000)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy: func(_ *terraform.State) error {
			return targetAPI.checkDeploymentDetached()
		},
		Steps: []resource.TestStep{
			{
				Config: runningConfig,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(
						"emqxcloud_deployment.current", "deployment_id", "deployment-managed",
					),
					resource.TestCheckResourceAttr(
						"emqxcloud_deployment.current", "deployment_type", "dedicatedFlex",
					),
					resource.TestCheckResourceAttr(
						"emqxcloud_deployment.current", "deployment_name", "terraform-preview",
					),
					resource.TestCheckResourceAttr(
						"emqxcloud_deployment.current", "status", "running",
					),
				),
			},
			{
				Config: stoppedConfig,
				Check: resource.TestCheckResourceAttr(
					"emqxcloud_deployment.current", "status", "stopped",
				),
			},
			{
				Config: runningConfig,
				Check: resource.TestCheckResourceAttr(
					"emqxcloud_deployment.current", "status", "running",
				),
			},
			{
				Config:      deploymentConfig(unusedServer.URL, targetServer.URL, "running", 2000),
				ExpectError: regexp.MustCompile("Deployment replacement is not supported"),
			},
			{
				Config:      deploymentProvidersConfig(unusedServer.URL, targetServer.URL),
				ExpectError: regexp.MustCompile("Deployment deletion is not supported"),
			},
			{
				Config: deploymentRemovedConfig(unusedServer.URL, targetServer.URL),
			},
		},
	})

	if err := unusedAPI.checkNoDeploymentCreates(); err != nil {
		t.Fatal(err)
	}
}

func TestProviderRejectsInitiallyStoppedDeployment(t *testing.T) {
	mockAPI := newMockProviderAPI()
	server := httptest.NewServer(mockAPI)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{{
			Config:      deploymentConfig(server.URL, server.URL, "stopped", 1000),
			ExpectError: regexp.MustCompile("A new deployment must start with status running"),
		}},
	})
}

func TestProviderRejectsInvalidDeploymentInputs(t *testing.T) {
	validConfig := deploymentConfig("https://unused.example", "https://target.example", "running", 1000)
	tests := map[string]struct {
		config string
		error  string
	}{
		"platform": {
			config: strings.Replace(validConfig, `platform           = "aws"`, `platform           = "aaa"`, 1),
			error:  "platform must be one of: aws, gcp, azure, aliyun, tencent, huawei",
		},
		"deployment name": {
			config: strings.Replace(validConfig, `deployment_name    = "terraform-preview"`, `deployment_name    = "`+strings.Repeat("a", 65)+`"`, 1),
			error:  "deployment_name must contain between 1 and 64 characters",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testProviderFactories,
				Steps: []resource.TestStep{{
					Config:      test.config,
					ExpectError: regexp.MustCompile(test.error),
				}},
			})
		})
	}
}

func TestProviderMockLifecycle(t *testing.T) {
	mockAPI := newMockProviderAPI()
	server := httptest.NewServer(mockAPI)
	defer server.Close()

	initialConfig := acceptanceConfig(server.URL, 1, true, "initial")
	updatedConfig := acceptanceConfig(server.URL, 2, false, "updated")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy: func(_ *terraform.State) error {
			return mockAPI.checkDestroyed()
		},
		Steps: []resource.TestStep{
			{
				Config: initialConfig,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(
						"data.emqxcloud_projects.current",
						"projects.#",
						"1",
					),
					resource.TestCheckResourceAttr(
						"data.emqxcloud_deployments.current",
						"deployments.#",
						"1",
					),
					resource.TestCheckResourceAttr(
						"data.emqxcloud_deployment.current",
						"name",
						"preview",
					),
					resource.TestCheckResourceAttr(
						"emqxcloud_deployment_tls.current",
						"status",
						"running",
					),
					resource.TestCheckResourceAttr(
						"emqxcloud_connector.http",
						"name",
						"terraform_preview",
					),
					resource.TestCheckResourceAttr(
						"emqxcloud_action.http",
						"name",
						"terraform_preview",
					),
					resource.TestCheckResourceAttr(
						"emqxcloud_rule.http",
						"rule_id",
						"terraform_preview",
					),
					resource.TestCheckResourceAttr(
						"emqxcloud_authentication_user.current",
						"is_superuser",
						"true",
					),
					resource.TestCheckResourceAttr(
						"emqxcloud_authorization_user.current",
						"username",
						"terraform_preview",
					),
					resource.TestCheckResourceAttr(
						"emqxcloud_authorization_client.current",
						"client_id",
						"terraform_preview",
					),
					resource.TestCheckResourceAttr(
						"emqxcloud_banned.current",
						"as",
						"clientid",
					),
				),
			},
			{
				Config: updatedConfig,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(
						"emqxcloud_connector.http",
						"config_json",
						`{"enable":false,"pool_size":2,"url":"https://example.com"}`,
					),
					resource.TestCheckResourceAttr(
						"emqxcloud_rule.http",
						"config_json",
						`{"actions":["http:terraform_preview"],`+
							`"description":"updated","enable":false,`+
							`"name":"terraform_preview",`+
							`"sql":"SELECT * FROM \"terraform/preview\""}`,
					),
					resource.TestCheckResourceAttr(
						"emqxcloud_authentication_user.current",
						"is_superuser",
						"false",
					),
					resource.TestCheckResourceAttr(
						"emqxcloud_authorization_user.current",
						"rules_json",
						`[{"action":"publish","permission":"allow",`+
							`"topic":"terraform/updated"}]`,
					),
					resource.TestCheckResourceAttr(
						"emqxcloud_authorization_client.current",
						"rules_json",
						`[{"action":"subscribe","permission":"allow",`+
							`"topic":"terraform/updated"}]`,
					),
					resource.TestCheckResourceAttr(
						"emqxcloud_banned.current",
						"config_json",
						`{"reason":"updated"}`,
					),
				),
			},
			{
				Config:   updatedConfig,
				PlanOnly: true,
			},
			{
				PreConfig: func() {
					mockAPI.mu.Lock()
					clear(mockAPI.authenticationUsers)
					clear(mockAPI.authorizationUsers)
					clear(mockAPI.authorizationClients)
					clear(mockAPI.banned)
					mockAPI.mu.Unlock()
				},
				Config: updatedConfig,
			},
			{
				Config:   updatedConfig,
				PlanOnly: true,
			},
		},
	})
}

func TestProviderPreservesTLSStateWhenCreatePollingFails(t *testing.T) {
	mockAPI := newMockProviderAPI()
	mockAPI.tlsReadFailures = 3
	server := httptest.NewServer(mockAPI)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy: func(_ *terraform.State) error {
			return mockAPI.checkDestroyed()
		},
		Steps: []resource.TestStep{
			{
				Config:      failingTLSCreateConfig(server.URL),
				ExpectError: regexp.MustCompile("creation did not complete"),
			},
		},
	})
}

func TestProviderPreservesEMQXStateWhenCreatePollingFails(t *testing.T) {
	mockAPI := newMockProviderAPI()
	mockAPI.emqxReadFailures = 3
	server := httptest.NewServer(mockAPI)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy: func(_ *terraform.State) error {
			return mockAPI.checkDestroyed()
		},
		Steps: []resource.TestStep{
			{
				Config:      failingEMQXCreateConfig(server.URL),
				ExpectError: regexp.MustCompile("creation did not complete"),
			},
		},
	})
}

func TestProviderDeploymentResourceUsesSelectedAlias(t *testing.T) {
	unusedServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	defer unusedServer.Close()
	targetAPI := newMockProviderAPI()
	targetServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		apiKey, apiSecret, ok := request.BasicAuth()
		if !ok || apiKey != "target-key" || apiSecret != "target-secret" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		request.SetBasicAuth("key", "secret")
		targetAPI.ServeHTTP(writer, request)
	}))
	defer targetServer.Close()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy: func(_ *terraform.State) error {
			return targetAPI.checkDestroyed()
		},
		Steps: []resource.TestStep{{
			Config: deploymentAliasConfig(unusedServer.URL, targetServer.URL),
		}},
	})
}

func TestProviderRejectsLegacyConfigurationNames(t *testing.T) {
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{{
			Config: `
provider "emqxcloud" {
  cloud_endpoint = "https://legacy.example"
}

data "emqxcloud_projects" "current" {}
`,
			ExpectError: regexp.MustCompile("Unsupported argument"),
		}},
	})
}

func newMockProviderAPI() *mockProviderAPI {
	return &mockProviderAPI{
		deployment:           mockDeployment(),
		connectors:           make(map[string]map[string]any),
		actions:              make(map[string]map[string]any),
		rules:                make(map[string]map[string]any),
		authenticationUsers:  make(map[string]map[string]any),
		authorizationUsers:   make(map[string]map[string]any),
		authorizationClients: make(map[string]map[string]any),
		banned:               make(map[string]map[string]any),
	}
}

func (m *mockProviderAPI) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	apiKey, apiSecret, ok := request.BasicAuth()
	if !ok || apiKey != "key" || apiSecret != "secret" {
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	writer.Header().Set("Content-Type", "application/json")
	switch {
	case request.URL.Path == "/public_api/v1/projects":
		writeJSON(writer, http.StatusOK, []map[string]any{
			{
				"projectID":   "project-1",
				"projectName": "default",
				"description": "preview",
			},
		})
	case request.URL.Path == "/public_api/v1/deployments/deployment-1/tls":
		m.serveTLS(writer, request)
	case request.URL.Path == "/public_api/v1/deployments":
		m.serveDeployments(writer, request)
	case strings.HasPrefix(request.URL.Path, "/public_api/v1/deployments/"):
		m.serveDeployment(writer, request)
	case strings.HasPrefix(request.URL.Path, "/api/v5/"):
		m.serveEMQX(writer, request)
	default:
		writer.WriteHeader(http.StatusNotFound)
	}
}

func (m *mockProviderAPI) serveDeployments(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		writeJSON(writer, http.StatusOK, []map[string]any{m.deployment})
	case http.MethodPost:
		if m.createFailure {
			writeJSON(writer, http.StatusUnprocessableEntity, map[string]any{"code": "INVALID_SPEC"})
			return
		}
		payload, err := decodeObject(request)
		if err != nil {
			writeJSON(writer, http.StatusBadRequest, map[string]any{"code": "BAD_JSON"})
			return
		}
		m.deploymentCreate = payload
		deploymentName := payload["deploymentName"]
		if m.ignoreDeploymentName {
			deploymentName = "deployment-managed"
		}
		m.deployment = map[string]any{
			"deploymentID":   "deployment-managed",
			"deploymentName": deploymentName,
			"deploymentType": payload["deploymentType"],
			"platform":       payload["platform"],
			"region":         payload["region"],
			"status":         "running",
			"connections":    payload["connections"],
			"tps":            payload["tps"],
		}
		writeJSON(writer, http.StatusCreated, m.deployment)
	default:
		writer.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (m *mockProviderAPI) serveDeployment(writer http.ResponseWriter, request *http.Request) {
	parts := strings.Split(strings.TrimPrefix(
		request.URL.Path,
		"/public_api/v1/deployments/",
	), "/")
	if len(parts) == 0 || fmt.Sprint(m.deployment["deploymentID"]) != parts[0] {
		writer.WriteHeader(http.StatusNotFound)
		return
	}
	if request.Method == http.MethodDelete {
		m.deploymentDeletes++
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if request.Method == http.MethodGet && len(parts) == 1 {
		if m.deploymentReadFailures > 0 {
			m.deploymentReadFailures--
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writeJSON(writer, http.StatusOK, m.deployment)
		return
	}
	if request.Method == http.MethodPost && len(parts) == 2 &&
		(parts[1] == "start" || parts[1] == "stop") {
		if m.operationFailure {
			writeJSON(writer, http.StatusUnprocessableEntity, map[string]any{"code": "INVALID_STATUS"})
			return
		}
		m.operations = append(m.operations, parts[1])
		m.deployment["status"] = map[string]string{
			"start": "running",
			"stop":  "stopped",
		}[parts[1]]
		writeJSON(writer, http.StatusCreated, map[string]any{
			"deploymentID":   parts[0],
			"deploymentName": m.deployment["deploymentName"],
			"operation":      parts[1] + "ing",
		})
		return
	}
	writer.WriteHeader(http.StatusMethodNotAllowed)
}

func (m *mockProviderAPI) serveTLS(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodPost, http.MethodPut:
		payload, err := decodeObject(request)
		if err != nil {
			writeJSON(writer, http.StatusBadRequest, map[string]any{"code": "BAD_JSON"})
			return
		}
		m.tls = map[string]any{
			"tlsType": payload["tlsType"],
			"status":  "running",
			"expire":  "2030-01-01T00:00:00Z",
			"cn":      "preview.example.com",
		}
		status := http.StatusOK
		if request.Method == http.MethodPost {
			status = http.StatusCreated
		}
		writeJSON(writer, status, m.tls)
	case http.MethodGet:
		if m.tlsReadFailures > 0 {
			m.tlsReadFailures--
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if m.tls == nil {
			writeJSON(writer, http.StatusOK, map[string]any{})
			return
		}
		writeJSON(writer, http.StatusOK, m.tls)
	case http.MethodDelete:
		m.tls = nil
		writer.WriteHeader(http.StatusNoContent)
	default:
		writer.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (m *mockProviderAPI) serveEMQX(writer http.ResponseWriter, request *http.Request) {
	relativePath := strings.TrimPrefix(request.URL.Path, "/api/v5/")
	if m.serveEMQXSecurity(writer, request, relativePath) {
		return
	}
	parts := strings.Split(relativePath, "/")
	collection := m.collection(parts[0])
	if collection == nil {
		writer.WriteHeader(http.StatusNotFound)
		return
	}

	if request.Method == http.MethodPost && len(parts) == 1 {
		payload, err := decodeObject(request)
		if err != nil {
			writeJSON(writer, http.StatusBadRequest, map[string]any{"code": "BAD_JSON"})
			return
		}
		id := fmt.Sprint(payload["id"])
		if parts[0] != "rules" {
			id = fmt.Sprintf("%s:%s", payload["type"], payload["name"])
		}
		collection[id] = payload
		writeJSON(writer, http.StatusCreated, payload)
		return
	}
	if len(parts) < 2 {
		writer.WriteHeader(http.StatusNotFound)
		return
	}

	id := parts[1]
	current, exists := collection[id]
	if !exists {
		writer.WriteHeader(http.StatusNotFound)
		return
	}
	if request.Method == http.MethodPut && len(parts) == 4 && parts[2] == "enable" {
		enabled, err := strconv.ParseBool(parts[3])
		if err != nil {
			writeJSON(writer, http.StatusBadRequest, map[string]any{"code": "BAD_ENABLE"})
			return
		}
		current["enable"] = enabled
		writer.WriteHeader(http.StatusNoContent)
		return
	}

	switch request.Method {
	case http.MethodGet:
		if m.emqxReadFailures > 0 {
			m.emqxReadFailures--
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writeJSON(writer, http.StatusOK, current)
	case http.MethodPut:
		payload, err := decodeObject(request)
		if err != nil {
			writeJSON(writer, http.StatusBadRequest, map[string]any{"code": "BAD_JSON"})
			return
		}
		for key, value := range payload {
			current[key] = value
		}
		writeJSON(writer, http.StatusOK, current)
	case http.MethodDelete:
		delete(collection, id)
		writer.WriteHeader(http.StatusNoContent)
	default:
		writer.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (m *mockProviderAPI) serveEMQXSecurity(
	writer http.ResponseWriter,
	request *http.Request,
	relativePath string,
) bool {
	authenticationPath := strings.TrimPrefix(authenticationUsersPath, "/")
	if relativePath == authenticationPath || strings.HasPrefix(relativePath, authenticationPath+"/") {
		m.serveAuthenticationUsers(writer, request, relativePath, authenticationPath)
		return true
	}

	for _, authorization := range []struct {
		path       string
		collection map[string]map[string]any
		identity   string
	}{
		{strings.TrimPrefix(authorizationRulesPath+"/users", "/"), m.authorizationUsers, "username"},
		{strings.TrimPrefix(authorizationRulesPath+"/clients", "/"), m.authorizationClients, "clientid"},
	} {
		if relativePath == authorization.path || strings.HasPrefix(relativePath, authorization.path+"/") {
			m.serveAuthorizationRules(
				writer,
				request,
				relativePath,
				authorization.path,
				authorization.collection,
				authorization.identity,
			)
			return true
		}
	}

	if relativePath == "banned" || strings.HasPrefix(relativePath, "banned/") {
		m.serveBanned(writer, request, relativePath)
		return true
	}
	return false
}

func (m *mockProviderAPI) serveAuthenticationUsers(
	writer http.ResponseWriter,
	request *http.Request,
	relativePath string,
	collectionPath string,
) {
	if relativePath == collectionPath {
		if request.Method == http.MethodDelete {
			m.unsafeRequests = append(m.unsafeRequests, request.Method+" "+request.URL.Path)
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		if request.Method != http.MethodPost {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		payload, err := decodeObject(request)
		if err != nil || fmt.Sprint(payload["password"]) == "" {
			writeJSON(writer, http.StatusBadRequest, map[string]any{"code": "BAD_JSON"})
			return
		}
		userID := fmt.Sprint(payload["user_id"])
		m.authenticationUsers[userID] = map[string]any{
			"user_id":      userID,
			"is_superuser": payload["is_superuser"],
		}
		writeJSON(writer, http.StatusCreated, m.authenticationUsers[userID])
		return
	}

	userID := strings.TrimPrefix(relativePath, collectionPath+"/")
	current, exists := m.authenticationUsers[userID]
	if !exists {
		writer.WriteHeader(http.StatusNotFound)
		return
	}
	switch request.Method {
	case http.MethodGet:
		writeJSON(writer, http.StatusOK, current)
	case http.MethodPut:
		payload, err := decodeObject(request)
		if err != nil || fmt.Sprint(payload["password"]) == "" {
			writeJSON(writer, http.StatusBadRequest, map[string]any{"code": "BAD_JSON"})
			return
		}
		current["is_superuser"] = payload["is_superuser"]
		writeJSON(writer, http.StatusOK, current)
	case http.MethodDelete:
		delete(m.authenticationUsers, userID)
		writer.WriteHeader(http.StatusNoContent)
	default:
		writer.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (m *mockProviderAPI) serveAuthorizationRules(
	writer http.ResponseWriter,
	request *http.Request,
	relativePath string,
	collectionPath string,
	collection map[string]map[string]any,
	identityField string,
) {
	if relativePath == collectionPath {
		if request.Method == http.MethodDelete {
			m.unsafeRequests = append(m.unsafeRequests, request.Method+" "+request.URL.Path)
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		if request.Method != http.MethodPost {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var payload []map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil || len(payload) != 1 {
			writeJSON(writer, http.StatusBadRequest, map[string]any{"code": "BAD_JSON"})
			return
		}
		identity := fmt.Sprint(payload[0][identityField])
		collection[identity] = payload[0]
		writer.WriteHeader(http.StatusNoContent)
		return
	}

	identity := strings.TrimPrefix(relativePath, collectionPath+"/")
	current, exists := collection[identity]
	if !exists {
		writer.WriteHeader(http.StatusNotFound)
		return
	}
	switch request.Method {
	case http.MethodGet:
		writeJSON(writer, http.StatusOK, current)
	case http.MethodPut:
		payload, err := decodeObject(request)
		if err != nil || fmt.Sprint(payload[identityField]) != identity {
			writeJSON(writer, http.StatusBadRequest, map[string]any{"code": "BAD_JSON"})
			return
		}
		collection[identity] = payload
		writer.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		delete(collection, identity)
		writer.WriteHeader(http.StatusNoContent)
	default:
		writer.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (m *mockProviderAPI) serveBanned(
	writer http.ResponseWriter,
	request *http.Request,
	relativePath string,
) {
	if relativePath == "banned" {
		switch request.Method {
		case http.MethodPost:
			payload, err := decodeObject(request)
			if err != nil {
				writeJSON(writer, http.StatusBadRequest, map[string]any{"code": "BAD_JSON"})
				return
			}
			key := fmt.Sprintf("%s\x00%s", payload["as"], payload["who"])
			m.banned[key] = payload
			writeJSON(writer, http.StatusOK, payload)
		case http.MethodGet:
			data := make([]map[string]any, 0, len(m.banned))
			for _, record := range m.banned {
				filter, err := bannedFilterName(fmt.Sprint(record["as"]))
				if err == nil && (filter == "" ||
					request.URL.Query().Get(filter) == fmt.Sprint(record["who"])) {
					data = append(data, record)
				}
			}
			writeJSON(writer, http.StatusOK, map[string]any{
				"data": data,
				"meta": map[string]any{"hasnext": false, "page": 1, "limit": 100},
			})
		case http.MethodDelete:
			m.unsafeRequests = append(m.unsafeRequests, request.Method+" "+request.URL.Path)
			writer.WriteHeader(http.StatusInternalServerError)
		default:
			writer.WriteHeader(http.StatusMethodNotAllowed)
		}
		return
	}

	parts := strings.Split(strings.TrimPrefix(relativePath, "banned/"), "/")
	if len(parts) != 2 || request.Method != http.MethodDelete {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	key := parts[0] + "\x00" + parts[1]
	if _, exists := m.banned[key]; !exists {
		writer.WriteHeader(http.StatusNotFound)
		return
	}
	delete(m.banned, key)
	writer.WriteHeader(http.StatusNoContent)
}

func (m *mockProviderAPI) collection(name string) map[string]map[string]any {
	switch name {
	case "connectors":
		return m.connectors
	case "actions":
		return m.actions
	case "rules":
		return m.rules
	default:
		return nil
	}
}

func (m *mockProviderAPI) checkDestroyed() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.tls != nil {
		return fmt.Errorf("TLS still exists after destroy")
	}
	if len(m.connectors) != 0 || len(m.actions) != 0 || len(m.rules) != 0 {
		return fmt.Errorf(
			"EMQX resources remain after destroy: connectors=%d actions=%d rules=%d",
			len(m.connectors),
			len(m.actions),
			len(m.rules),
		)
	}
	if len(m.authenticationUsers) != 0 || len(m.authorizationUsers) != 0 ||
		len(m.authorizationClients) != 0 || len(m.banned) != 0 {
		return fmt.Errorf(
			"EMQX security resources remain after destroy: authn=%d authz_users=%d authz_clients=%d banned=%d",
			len(m.authenticationUsers),
			len(m.authorizationUsers),
			len(m.authorizationClients),
			len(m.banned),
		)
	}
	if len(m.unsafeRequests) != 0 {
		return fmt.Errorf("unsafe collection requests: %v", m.unsafeRequests)
	}
	return nil
}

func (m *mockProviderAPI) checkNoDeploymentCreates() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.deploymentCreate != nil {
		return errors.New("deployment was created through the unused Platform alias")
	}
	return nil
}

func (m *mockProviderAPI) checkDeploymentDetached() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.deploymentCreate == nil {
		return errors.New("deployment was not created")
	}
	expected := map[string]any{
		"projectID":         "project-1",
		"platform":          "aws",
		"region":            "us-east-1",
		"connections":       float64(1000),
		"tps":               float64(1000),
		"deploymentName":    "terraform-preview",
		"deploymentType":    "dedicatedFlex",
		"deploymentVersion": "v5",
		"freeTrial":         false,
	}
	if !reflect.DeepEqual(m.deploymentCreate, expected) {
		return fmt.Errorf("unexpected deployment create payload: %#v", m.deploymentCreate)
	}
	if fmt.Sprint(m.operations) != "[stop start]" {
		return fmt.Errorf("unexpected deployment operations: %v", m.operations)
	}
	if m.deploymentDeletes != 0 {
		return fmt.Errorf("deployment DELETE requests: %d", m.deploymentDeletes)
	}
	return nil
}

func (m *mockProviderAPI) checkDeploymentNotDeleted() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.deploymentCreate == nil {
		return errors.New("deployment was not created")
	}
	if m.deploymentDeletes != 0 {
		return fmt.Errorf("deployment DELETE requests: %d", m.deploymentDeletes)
	}
	return nil
}

func mockDeployment() map[string]any {
	return map[string]any{
		"createAt":       "2026-07-29T00:00:00Z",
		"deploymentID":   "deployment-1",
		"deploymentName": "preview",
		"deploymentType": "dedicated",
		"projectName":    "default",
		"platform":       "AWS",
		"region":         "Frankfurt",
		"status":         "running",
		"version":        "v5",
		"connections":    1000,
		"tps":            1000,
	}
}

func decodeObject(request *http.Request) (map[string]any, error) {
	var payload map[string]any
	err := json.NewDecoder(request.Body).Decode(&payload)
	return payload, err
}

func writeJSON(writer http.ResponseWriter, status int, payload any) {
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(payload)
}

func acceptanceConfig(endpoint string, poolSize int, enabled bool, description string) string {
	return fmt.Sprintf(`
terraform {
  required_version = ">= 1.0"
}

provider "emqxcloud" {
  platform_endpoint      = %[1]q
  platform_api_key       = "key"
  platform_api_secret    = "secret"
  deployment_endpoint   = %[2]q
  deployment_api_key    = "key"
  deployment_api_secret = "secret"
}

data "emqxcloud_projects" "current" {}
data "emqxcloud_deployments" "current" {}
data "emqxcloud_deployment" "current" {
  deployment_id = "deployment-1"
}

resource "emqxcloud_deployment_tls" "current" {
  deployment_id  = "deployment-1"
  tls_type       = "one-way"
  certificate_pem = "certificate-%[3]s"
  private_key_pem = "private-key"
}

resource "emqxcloud_connector" "http" {
  type = "http"
  name = "terraform_preview"
  config_json = jsonencode({
    enable    = %[4]t
    pool_size = %[5]d
    url       = "https://example.com"
  })
}

resource "emqxcloud_action" "http" {
  type = "http"
  name = "terraform_preview"
  config_json = jsonencode({
    connector = emqxcloud_connector.http.name
    enable    = %[4]t
    parameters = {
      body    = "$${.}"
      headers = {}
      method  = "post"
      path    = "/ping"
    }
  })
}

resource "emqxcloud_rule" "http" {
  rule_id = "terraform_preview"
  config_json = jsonencode({
    name        = "terraform_preview"
    sql         = "SELECT * FROM \"terraform/preview\""
    actions     = ["${emqxcloud_action.http.type}:${emqxcloud_action.http.name}"]
    enable      = %[4]t
    description = %[6]q
  })
}

resource "emqxcloud_authentication_user" "current" {
  user_id      = "terraform_preview"
  password     = "password-%[6]s"
  is_superuser = %[4]t
}

resource "emqxcloud_authorization_user" "current" {
  username = "terraform_preview"
  rules_json = jsonencode([{
    permission = "allow"
    action     = "publish"
    topic      = "terraform/%[6]s"
  }])
}

resource "emqxcloud_authorization_client" "current" {
  client_id = "terraform_preview"
  rules_json = jsonencode([{
    permission = "allow"
    action     = "subscribe"
    topic      = "terraform/%[6]s"
  }])
}

resource "emqxcloud_banned" "current" {
  as   = "clientid"
  who  = "terraform_preview"
  config_json = jsonencode({
    reason = %[6]q
  })
}
`,
		endpoint+"/public_api/v1",
		endpoint+"/api/v5",
		description,
		enabled,
		poolSize,
		description,
	)
}

func failingTLSCreateConfig(endpoint string) string {
	return fmt.Sprintf(`
provider "emqxcloud" {
  platform_endpoint   = %q
  platform_api_key    = "key"
  platform_api_secret = "secret"
}

resource "emqxcloud_deployment_tls" "current" {
  deployment_id   = "deployment-1"
  tls_type        = "one-way"
  certificate_pem = "certificate"
  private_key_pem = "private-key"
}
`, endpoint+"/public_api/v1")
}

func failingEMQXCreateConfig(endpoint string) string {
	return fmt.Sprintf(`
provider "emqxcloud" {
  deployment_endpoint   = %q
  deployment_api_key    = "key"
  deployment_api_secret = "secret"
}

resource "emqxcloud_connector" "current" {
  type        = "http"
  name        = "polling_failure"
  config_json = jsonencode({"enable": true, "url": "https://example.com"})
}
`, endpoint+"/api/v5")
}

func deploymentProvidersConfig(unusedEndpoint, targetEndpoint string) string {
	return fmt.Sprintf(`
terraform {
  required_version = ">= 1.7"
}

provider "emqxcloud" {
  alias               = "unused"
  platform_endpoint   = %q
  platform_api_key    = "key"
  platform_api_secret = "secret"
}

provider "emqxcloud" {
  alias               = "target"
  platform_endpoint   = %q
  platform_api_key    = "key"
  platform_api_secret = "secret"
}
	`, unusedEndpoint+"/public_api/v1", targetEndpoint+"/public_api/v1")
}

func deploymentAliasConfig(unusedEndpoint, targetEndpoint string) string {
	return fmt.Sprintf(`
provider "emqxcloud" {
  alias                 = "unused"
  deployment_endpoint   = %q
  deployment_api_key    = "unused-key"
  deployment_api_secret = "unused-secret"
}

provider "emqxcloud" {
  alias                 = "target"
  deployment_endpoint   = %q
  deployment_api_key    = "target-key"
  deployment_api_secret = "target-secret"
}

resource "emqxcloud_connector" "current" {
  provider = emqxcloud.target

  type = "http"
  name = "alias_test"
  config_json = jsonencode({
    enable = true
    url    = "https://example.com"
  })
}
`, unusedEndpoint+"/api/v5", targetEndpoint+"/api/v5")
}

func deploymentConfig(
	unusedEndpoint string,
	targetEndpoint string,
	status string,
	connections int,
) string {
	return deploymentProvidersConfig(unusedEndpoint, targetEndpoint) + fmt.Sprintf(`
resource "emqxcloud_deployment" "current" {
  provider           = emqxcloud.target
  project_id         = "project-1"
  platform           = "aws"
  region             = "us-east-1"
  connections        = %d
  tps                = 1000
  deployment_name    = "terraform-preview"
  deployment_version = "v5"
  status             = %q
}
`, connections, status)
}

func deploymentRemovedConfig(unusedEndpoint, targetEndpoint string) string {
	return deploymentProvidersConfig(unusedEndpoint, targetEndpoint) + `
removed {
  from = emqxcloud_deployment.current

  lifecycle {
    destroy = false
  }
}
`
}

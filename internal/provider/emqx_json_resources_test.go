package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	frameworkresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestGenerateEMQXResourceName(t *testing.T) {
	t.Parallel()

	for _, prefix := range []string{"c-", "a-", "s-", "r-"} {
		name, err := generateEMQXResourceName(prefix)
		if err != nil {
			t.Fatalf("generate %s name: %v", prefix, err)
		}
		if !regexp.MustCompile("^" + regexp.QuoteMeta(prefix) + `[a-z0-9]{6}$`).MatchString(name) {
			t.Fatalf("unexpected generated name %q", name)
		}
	}
}

func TestProjectVisibleJSONPreservesMaskedAndMissingValues(t *testing.T) {
	t.Parallel()

	configJSON := `{
		"url":"https://example.com",
		"password":"secret",
		"headers":{"authorization":"secret","content-type":"application/json"},
		"items":[{"token":"secret","count":1}]
	}`
	remote := map[string]any{
		"url":      "https://changed.example.com",
		"password": maskedValue,
		"headers": map[string]any{
			"authorization": maskedValue,
		},
		"items": []any{
			map[string]any{
				"token": maskedValue,
				"count": json.Number("2"),
			},
		},
	}

	projected, matches, err := projectVisibleJSON(configJSON, remote)
	if err != nil {
		t.Fatalf("project JSON: %v", err)
	}
	if matches {
		t.Fatal("expected visible drift")
	}
	for _, expected := range []string{
		`"url":"https://changed.example.com"`,
		`"password":"secret"`,
		`"authorization":"secret"`,
		`"content-type":"application/json"`,
		`"count":2`,
		`"token":"secret"`,
	} {
		if !strings.Contains(projected, expected) {
			t.Fatalf("projected JSON missing %s: %s", expected, projected)
		}
	}
}

func TestProjectVisibleJSONKeepsOriginalFormattingWithoutDrift(t *testing.T) {
	t.Parallel()

	configJSON := "{\n  \"pool_size\": 1,\n  \"password\": \"secret\"\n}"
	remote := map[string]any{
		"pool_size": json.Number("1"),
		"password":  maskedValue,
		"status":    "connected",
	}

	projected, matches, err := projectVisibleJSON(configJSON, remote)
	if err != nil {
		t.Fatalf("project JSON: %v", err)
	}
	if !matches || projected != configJSON {
		t.Fatalf("expected original JSON without drift, got %s", projected)
	}
}

func TestJSONResourcePayloadInjectsIdentity(t *testing.T) {
	t.Parallel()

	resource := newEMQXJSONResource(emqxJSONResourceSpec{
		typeSuffix: "connector",
		collection: "/connectors",
		named:      true,
		toggle:     true,
	})
	payload, err := resource.createPayload(emqxJSONState{
		Type:       types.StringValue("http"),
		Name:       types.StringValue("example"),
		ConfigJSON: types.StringValue(`{"enable":true,"url":"https://example.com"}`),
	})
	if err != nil {
		t.Fatalf("create payload: %v", err)
	}
	if payload["type"] != "http" || payload["name"] != "example" {
		t.Fatalf("identity was not injected: %#v", payload)
	}
}

func TestJSONRuleCreateResponseIdentity(t *testing.T) {
	for _, test := range []struct {
		name        string
		returnedID  string
		invalidJSON bool
		wantError   bool
	}{
		{name: "omitted id"},
		{name: "mismatched id", returnedID: "remote-secret-id", wantError: true},
		{name: "invalid response", invalidJSON: true, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requestedID string
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				switch request.Method {
				case http.MethodPost:
					if request.URL.EscapedPath() != "/rules" {
						t.Errorf("unexpected create path %s", request.URL.EscapedPath())
					}
					var payload map[string]any
					if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
						t.Errorf("decode create payload: %v", err)
					}
					requestedID, _ = payload["id"].(string)
					writer.WriteHeader(http.StatusCreated)
					if test.invalidJSON {
						_, _ = writer.Write([]byte(`{`))
					} else if test.returnedID != "" {
						_ = json.NewEncoder(writer).Encode(map[string]string{"id": test.returnedID})
					}
				case http.MethodGet:
					if request.URL.EscapedPath() != "/rules/"+requestedID {
						t.Errorf("unexpected read path %s", request.URL.EscapedPath())
					}
					_ = json.NewEncoder(writer).Encode(map[string]string{
						"id": requestedID, "sql": "SELECT * FROM t",
					})
				default:
					t.Errorf("unexpected request %s %s", request.Method, request.URL.EscapedPath())
				}
			}))
			defer server.Close()

			rule := newRuleResource().(*emqxJSONResource)
			rule.deployment = testAPIClient(t, server.URL)
			rule.pollInterval = time.Nanosecond
			rule.timeout = time.Second
			var schemaResponse frameworkresource.SchemaResponse
			rule.Schema(context.Background(), frameworkresource.SchemaRequest{}, &schemaResponse)

			plan := tfsdk.Plan{Schema: schemaResponse.Schema}
			diagnostics := plan.Set(context.Background(), &ruleJSONResourceModel{
				RuleID:     types.StringUnknown(),
				ConfigJSON: types.StringValue(`{"sql":"SELECT * FROM t"}`),
			})
			if diagnostics.HasError() {
				t.Fatalf("set rule plan: %v", diagnostics.Errors())
			}
			response := frameworkresource.CreateResponse{State: tfsdk.State{Schema: schemaResponse.Schema}}
			rule.Create(context.Background(), frameworkresource.CreateRequest{Plan: plan}, &response)
			if response.Diagnostics.HasError() != test.wantError {
				t.Fatalf("unexpected create diagnostics: %v", response.Diagnostics.Errors())
			}
			for _, diagnostic := range response.Diagnostics.Errors() {
				if test.returnedID != "" && strings.Contains(diagnostic.Detail(), test.returnedID) {
					t.Fatalf("diagnostic leaked returned id: %s", diagnostic.Detail())
				}
			}

			var current ruleJSONResourceModel
			if diagnostics = response.State.Get(context.Background(), &current); diagnostics.HasError() {
				t.Fatalf("get rule state: %v", diagnostics.Errors())
			}
			if current.RuleID.ValueString() != requestedID {
				t.Fatalf("unexpected state rule id %q, want %q", current.RuleID.ValueString(), requestedID)
			}
		})
	}
}

func TestJSONResourceUpdateSeparatesEnable(t *testing.T) {
	t.Parallel()

	resource := newEMQXJSONResource(emqxJSONResourceSpec{
		typeSuffix: "action",
		collection: "/actions",
		named:      true,
		toggle:     true,
	})
	payload, enabled, err := resource.updatePayload(emqxJSONState{
		ConfigJSON: types.StringValue(`{"enable":false,"connector":"example"}`),
	})
	if err != nil {
		t.Fatalf("update payload: %v", err)
	}
	if enabled == nil || *enabled {
		t.Fatalf("expected disabled toggle, got %#v", enabled)
	}
	if _, exists := payload["enable"]; exists {
		t.Fatalf("enable leaked into update payload: %#v", payload)
	}
}

func TestJSONResourceWaitsForVisibleConfiguration(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		poolSize := 1
		if attempts.Add(1) == 2 {
			poolSize = 2
		}
		_, _ = writer.Write([]byte(`{"type":"http","name":"example","pool_size":` +
			strconv.Itoa(poolSize) + `}`))
	}))
	defer server.Close()

	resource := newEMQXJSONResource(emqxJSONResourceSpec{
		typeSuffix: "connector",
		collection: "/connectors",
		named:      true,
		toggle:     true,
	})
	resource.deployment = testAPIClient(t, server.URL)
	resource.pollInterval = time.Nanosecond
	resource.timeout = time.Second

	projected, err := resource.waitForRemote(context.Background(), emqxJSONState{
		Type:       types.StringValue("http"),
		Name:       types.StringValue("example"),
		ConfigJSON: types.StringValue(`{"pool_size":2}`),
	})
	if err != nil {
		t.Fatalf("wait for resource: %v", err)
	}
	if projected != `{"pool_size":2}` || attempts.Load() != 2 {
		t.Fatalf("unexpected result %s after %d attempts", projected, attempts.Load())
	}
}

func TestJSONRuleWaitAllowsOmittedID(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"sql":"SELECT * FROM t"}`))
	}))
	defer server.Close()

	resource := newRuleResource().(*emqxJSONResource)
	resource.deployment = testAPIClient(t, server.URL)
	resource.pollInterval = time.Nanosecond
	resource.timeout = time.Second

	projected, err := resource.waitForRemote(context.Background(), emqxJSONState{
		RuleID:     types.StringValue("r-abc123"),
		ConfigJSON: types.StringValue(`{"sql":"SELECT * FROM t"}`),
	})
	if err != nil {
		t.Fatalf("wait for rule: %v", err)
	}
	if projected != `{"sql":"SELECT * FROM t"}` {
		t.Fatalf("unexpected projected rule %s", projected)
	}
}

func TestJSONRuleWaitRejectsMismatchedIDWithoutLeakingIt(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"id":"remote-secret-id","sql":"SELECT * FROM t"}`))
	}))
	defer server.Close()

	resource := newRuleResource().(*emqxJSONResource)
	resource.deployment = testAPIClient(t, server.URL)
	resource.pollInterval = time.Nanosecond
	resource.timeout = time.Second

	_, err := resource.waitForRemote(context.Background(), emqxJSONState{
		RuleID:     types.StringValue("r-abc123"),
		ConfigJSON: types.StringValue(`{"sql":"SELECT * FROM t"}`),
	})
	if err == nil || !strings.Contains(err.Error(), "does not match") ||
		strings.Contains(err.Error(), "remote-secret-id") {
		t.Fatalf("expected redacted mismatched-id error, got %v", err)
	}
}

func TestJSONResourceRejectsIdentityInConfig(t *testing.T) {
	t.Parallel()

	resource := newEMQXJSONResource(emqxJSONResourceSpec{
		typeSuffix: "connector",
		collection: "/connectors",
		named:      true,
	})
	_, err := resource.createPayload(emqxJSONState{
		Type:       types.StringValue("http"),
		Name:       types.StringValue("example"),
		ConfigJSON: types.StringValue(`{"name":"duplicate"}`),
	})
	if err == nil || !strings.Contains(err.Error(), "identity field") {
		t.Fatalf("expected identity error, got %v", err)
	}
}

func TestJSONResourceTreatsNotFoundAsMissing(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	resource := newEMQXJSONResource(emqxJSONResourceSpec{
		collection: "/connectors", named: true,
	})
	resource.deployment = testAPIClient(t, server.URL)

	_, exists, err := resource.readRemote(context.Background(), emqxJSONState{
		Type: types.StringValue("http"), Name: types.StringValue("missing"),
	})
	if err != nil || exists {
		t.Fatalf("read missing resource: exists=%t err=%v", exists, err)
	}
}

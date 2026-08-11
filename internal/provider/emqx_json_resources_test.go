package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

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

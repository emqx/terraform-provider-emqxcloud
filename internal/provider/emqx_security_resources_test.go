package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestBannedFilterNamesAndItemPaths(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"clientid":     "clientid",
		"username":     "username",
		"peerhost":     "peerhost",
		"clientid_re":  "like_clientid",
		"username_re":  "like_username",
		"peerhost_net": "like_peerhost_net",
	}
	for as, expectedFilter := range tests {
		as, expectedFilter := as, expectedFilter
		t.Run(as, func(t *testing.T) {
			t.Parallel()
			filter, err := bannedFilterName(as)
			if err != nil || filter != expectedFilter {
				t.Fatalf("filter for %s: %q, %v", as, filter, err)
			}
			path := bannedItemPath(bannedModel{
				As: types.StringValue(as), Who: types.StringValue("name/with space"),
			})
			if !strings.HasSuffix(path, "/name%2Fwith%20space") {
				t.Fatalf("item path was not escaped: %s", path)
			}
		})
	}
}

func TestAuthorizationPayloadUsesDeploymentAPIIdentity(t *testing.T) {
	t.Parallel()

	resource := newAuthorizationClientResource().(*authorizationResource)
	payload, err := resource.payload(authorizationState{
		Identity:  types.StringValue("client-1"),
		RulesJSON: types.StringValue(`[{"permission":"allow","action":"publish","topic":"a/#"}]`),
	})
	if err != nil {
		t.Fatalf("authorization payload: %v", err)
	}
	if payload["clientid"] != "client-1" || !reflect.DeepEqual(
		payload["rules"],
		[]any{map[string]any{"permission": "allow", "action": "publish", "topic": "a/#"}},
	) {
		t.Fatalf("unexpected authorization payload: %#v", payload)
	}
}

func TestProjectJSONArrayIgnoresRemoteRuleDefaults(t *testing.T) {
	t.Parallel()

	configured := `[{"permission":"allow","action":"publish","topic":"a/#"}]`
	remote := []any{map[string]any{
		"permission": "allow",
		"action":     "publish",
		"topic":      "a/#",
		"qos":        []any{float64(0), float64(1), float64(2)},
		"retain":     "all",
	}}
	projected, err := projectJSONArray(configured, remote)
	if err != nil {
		t.Fatalf("project authorization rules: %v", err)
	}
	if projected != configured {
		t.Fatalf("remote defaults caused drift: %s", projected)
	}
}

func TestBannedPayloadRejectsUnsupportedAndIdentityFields(t *testing.T) {
	t.Parallel()

	for _, configJSON := range []string{`{"as":"username"}`, `{"extra":true}`} {
		_, err := bannedPayload(bannedModel{
			As: types.StringValue("username"), Who: types.StringValue("user"),
			ConfigJSON: types.StringValue(configJSON),
		})
		if err == nil {
			t.Fatalf("expected invalid config_json %s to fail", configJSON)
		}
	}
}

func TestBannedReadStopsWhenListingNeverEnds(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		// A page that never matches and always claims another page follows.
		_, _ = writer.Write([]byte(
			`{"data":[{"as":"username","who":"someone-else"}],"meta":{"hasnext":true}}`,
		))
	}))
	defer server.Close()

	resource := &bannedResource{deploymentAPIResource: deploymentAPIResource{
		deployment: testAPIClient(t, server.URL),
	}}
	_, exists, err := resource.readRemote(context.Background(), bannedModel{
		As:  types.StringValue("username"),
		Who: types.StringValue("user-1"),
	})
	if err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("expected a page limit error, got exists=%t err=%v", exists, err)
	}
	if exists {
		t.Fatal("unconfirmed absence must not report the entry as found")
	}
	if requests.Load() != bannedMaxPages {
		t.Fatalf("expected %d requests, got %d", bannedMaxPages, requests.Load())
	}
}

func TestBannedReadStopsOnEmptyPage(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"data":[],"meta":{"hasnext":true}}`))
	}))
	defer server.Close()

	resource := &bannedResource{deploymentAPIResource: deploymentAPIResource{
		deployment: testAPIClient(t, server.URL),
	}}
	_, exists, err := resource.readRemote(context.Background(), bannedModel{
		As:  types.StringValue("username"),
		Who: types.StringValue("user-1"),
	})
	if err != nil {
		t.Fatalf("read banned entry: %v", err)
	}
	if exists {
		t.Fatal("expected the banned entry to be absent")
	}
	if requests.Load() != 1 {
		t.Fatalf("expected 1 request, got %d", requests.Load())
	}
}

func TestBannedReadPreservesFormattedNumericConfiguration(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("like_clientid") != "^client-1$" {
			t.Fatalf("unexpected regex filter: %s", request.URL.RawQuery)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(
			`{"data":[{"as":"clientid_re","who":"^client-1$",` +
				`"until":"2030-01-01T00:00:00+00:00"}],` +
				`"meta":{"hasnext":false}}`,
		))
	}))
	defer server.Close()

	resource := &bannedResource{deploymentAPIResource: deploymentAPIResource{
		deployment: testAPIClient(t, server.URL),
	}}
	configJSON := "{\n  \"until\": 1893456000\n}"
	remote, exists, err := resource.readRemote(context.Background(), bannedModel{
		As:         types.StringValue("clientid_re"),
		Who:        types.StringValue("^client-1$"),
		ConfigJSON: types.StringValue(configJSON),
	})
	if err != nil || !exists {
		t.Fatalf("read banned entry: exists=%t err=%v", exists, err)
	}
	projected, _, err := projectBannedJSON(configJSON, remote)
	if err != nil {
		t.Fatalf("project banned entry: %v", err)
	}
	if projected != configJSON {
		t.Fatalf("numeric configuration changed from %q to %q", configJSON, projected)
	}
}

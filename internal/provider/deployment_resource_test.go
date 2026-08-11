package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emqx/terraform-provider-emqxcloud/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestDeploymentWaitsForTerminalStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		statuses []string
		desired  string
	}{
		{name: "create", statuses: []string{"pending", "starting", "running"}, desired: "running"},
		{name: "stop", statuses: []string{"stopping", "stopped"}, desired: "stopped"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(
				writer http.ResponseWriter,
				_ *http.Request,
			) {
				attempt := int(attempts.Add(1)) - 1
				if attempt >= len(test.statuses) {
					attempt = len(test.statuses) - 1
				}
				_, _ = writer.Write([]byte(
					`{"deploymentID":"deployment-1","status":"` + test.statuses[attempt] + `"}`,
				))
			}))
			defer server.Close()

			current, err := testDeploymentResource(t, server.URL).waitForDeployment(
				context.Background(),
				"deployment-1",
				test.desired,
			)
			if err != nil {
				t.Fatalf("wait for deployment: %v", err)
			}
			if current.Status != test.desired || int(attempts.Load()) != len(test.statuses) {
				t.Fatalf("unexpected result %#v after %d attempts", current, attempts.Load())
			}
		})
	}
}

func TestDeploymentWaitFailsImmediately(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"deploymentID":"deployment-1","status":"failed"}`))
	}))
	defer server.Close()

	_, err := testDeploymentResource(t, server.URL).waitForDeployment(
		context.Background(),
		"deployment-1",
		"running",
	)
	if err == nil || !strings.Contains(err.Error(), "failed status") {
		t.Fatalf("expected failed status error, got %v", err)
	}
}

func TestDeploymentWaitTimesOut(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"deploymentID":"deployment-1","status":"pending"}`))
	}))
	defer server.Close()

	managedResource := testDeploymentResource(t, server.URL)
	managedResource.pollInterval = time.Millisecond
	managedResource.timeout = time.Millisecond
	_, err := managedResource.waitForDeployment(context.Background(), "deployment-1", "running")
	if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("expected timeout, got %v", err)
	}
}

func TestDeploymentReadTreats404AsAbsent(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	_, exists, err := testDeploymentResource(t, server.URL).readDeployment(
		context.Background(),
		"deployment-1",
	)
	if err != nil {
		t.Fatalf("read deployment: %v", err)
	}
	if exists {
		t.Fatal("expected deployment to be absent")
	}
}

func TestDeploymentRefreshPreservesMissingCreationFields(t *testing.T) {
	t.Parallel()

	model := managedDeploymentModel{
		ProjectID:    types.StringValue("project-1"),
		Connections:  types.Int64Value(1000),
		Transactions: types.Int64Value(1000),
		Name:         types.StringValue("preview"),
		Type:         types.StringValue("dedicatedFlex"),
		Version:      types.StringValue("v5"),
		Status:       types.StringValue("pending"),
	}
	updateManagedDeployment(&model, deploymentAPIModel{Status: "running"})

	if model.Name.ValueString() != "preview" || model.Type.ValueString() != "dedicatedFlex" ||
		model.Version.ValueString() != "v5" || model.Status.ValueString() != "running" ||
		model.Connections.ValueInt64() != 1000 || model.Transactions.ValueInt64() != 1000 {
		t.Fatalf("unexpected refreshed model: %#v", model)
	}
}

func TestDeploymentRefreshResolvesUnknownComputedFields(t *testing.T) {
	t.Parallel()

	// deployment_name and deployment_type are computed and stay unknown on create until the
	// Platform API reports them. Terraform rejects a state that still holds an unknown value.
	model := managedDeploymentModel{
		Name: types.StringUnknown(),
		Type: types.StringUnknown(),
	}
	updateManagedDeployment(&model, deploymentAPIModel{ID: "deployment-1", Status: "running"})

	if model.Name.IsUnknown() || model.Type.IsUnknown() {
		t.Fatalf("computed attributes stayed unknown: %#v", model)
	}
	if !model.Name.IsNull() || !model.Type.IsNull() {
		t.Fatalf("expected unreported computed attributes to be null: %#v", model)
	}
}

func TestDeploymentWaitForStableIgnoresTransientStatus(t *testing.T) {
	t.Parallel()

	// A deployment that is still starting cannot accept stop, and it settles on running.
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		status := "starting"
		if attempts.Add(1) >= 2 {
			status = "running"
		}
		_, _ = writer.Write([]byte(
			`{"deploymentID":"deployment-1","status":"` + status + `"}`,
		))
	}))
	defer server.Close()

	current, err := testDeploymentResource(t, server.URL).waitForStableDeployment(
		context.Background(),
		"deployment-1",
	)
	if err != nil {
		t.Fatalf("wait for stable deployment: %v", err)
	}
	if current.Status != "running" || attempts.Load() != 2 {
		t.Fatalf("unexpected result %#v after %d attempts", current, attempts.Load())
	}
}

func TestDeploymentDeleteIsRejected(t *testing.T) {
	t.Parallel()

	var response resource.DeleteResponse
	(&deploymentResource{}).Delete(
		context.Background(),
		resource.DeleteRequest{},
		&response,
	)

	if !response.Diagnostics.HasError() {
		t.Fatal("expected deployment deletion to be rejected")
	}
	if !strings.Contains(response.Diagnostics.Errors()[0].Summary(), "not supported") {
		t.Fatalf("unexpected diagnostic: %s", response.Diagnostics.Errors()[0].Summary())
	}
}

func testDeploymentResource(t *testing.T, endpoint string) *deploymentResource {
	t.Helper()
	return &deploymentResource{
		platform:     testAPIClient(t, endpoint),
		pollInterval: time.Nanosecond,
		timeout:      time.Second,
	}
}

func testAPIClient(t *testing.T, endpoint string) *client.Client {
	t.Helper()
	apiClient, err := client.New(client.Options{
		Endpoint: endpoint, APIKey: "key", APISecret: "secret", RetryWait: time.Nanosecond, AllowHTTP: true,
	})
	if err != nil {
		t.Fatalf("create API client: %v", err)
	}
	return apiClient
}

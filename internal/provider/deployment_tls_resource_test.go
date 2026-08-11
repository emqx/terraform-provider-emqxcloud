package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestDeploymentTLSWaitsForRunning(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		status := "pending"
		if attempts.Add(1) == 2 {
			status = "running"
		}
		_, _ = writer.Write([]byte(
			`{"tlsType":"one-way","status":"` + status + `","expire":"2030-01-01","cn":"test"}`,
		))
	}))
	defer server.Close()

	resource := testTLSResource(t, server.URL)
	current, err := resource.waitForTLS(context.Background(), "deployment-1", false)
	if err != nil {
		t.Fatalf("wait for TLS: %v", err)
	}
	if current.Status != "running" || attempts.Load() != 2 {
		t.Fatalf("unexpected TLS result: %#v after %d attempts", current, attempts.Load())
	}
}

func TestDeploymentTLSDeleteWaitsForEmptyResponse(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			_, _ = writer.Write([]byte(`{"tlsType":"one-way","status":"terminated"}`))
			return
		}
		_, _ = writer.Write([]byte(`{}`))
	}))
	defer server.Close()

	resource := testTLSResource(t, server.URL)
	_, err := resource.waitForTLS(context.Background(), "deployment-1", true)
	if err != nil {
		t.Fatalf("wait for TLS deletion: %v", err)
	}
	if attempts.Load() != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts.Load())
	}
}

func TestDeploymentTLSFailsImmediately(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		_, _ = writer.Write([]byte(`{"tlsType":"one-way","status":"failed"}`))
	}))
	defer server.Close()

	resource := testTLSResource(t, server.URL)
	_, err := resource.waitForTLS(context.Background(), "deployment-1", false)
	if err == nil || !strings.Contains(err.Error(), "failed status") {
		t.Fatalf("expected failed status error, got %v", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("expected 1 attempt, got %d", attempts.Load())
	}
}

func TestDeploymentTLSWaitTimesOut(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"tlsType":"one-way","status":"pending"}`))
	}))
	defer server.Close()

	resource := testTLSResource(t, server.URL)
	resource.pollInterval = time.Millisecond
	resource.timeout = time.Millisecond
	_, err := resource.waitForTLS(context.Background(), "deployment-1", false)
	if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("expected timeout, got %v", err)
	}
}

func TestDeploymentTLSReadTreats404AsAbsent(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	resource := testTLSResource(t, server.URL)
	_, exists, err := resource.readTLS(context.Background(), "deployment-1")
	if err != nil {
		t.Fatalf("read TLS: %v", err)
	}
	if exists {
		t.Fatal("expected TLS to be absent")
	}
}

func TestDeploymentTLSDelete404VerifiesRemoteAbsence(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.Method == http.MethodDelete {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = writer.Write([]byte(`{"tlsType":"one-way","status":"running"}`))
	}))
	defer server.Close()

	resource := testTLSResource(t, server.URL)
	err := resource.deleteTLS(context.Background(), "deployment-1")
	if err == nil || !strings.Contains(err.Error(), "still exists") {
		t.Fatalf("expected retained TLS error, got %v", err)
	}
}

func TestDeploymentTLSDelete404AcceptsEmptyTLS(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.Method == http.MethodDelete {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = writer.Write([]byte(`{}`))
	}))
	defer server.Close()

	resource := testTLSResource(t, server.URL)
	if err := resource.deleteTLS(context.Background(), "deployment-1"); err != nil {
		t.Fatalf("delete absent TLS: %v", err)
	}
}

func TestDeploymentTLSRefreshPreservesConfiguredType(t *testing.T) {
	t.Parallel()

	model := deploymentTLSModel{
		DeploymentID: types.StringValue("deployment-1"),
		TLSType:      types.StringValue("two-way"),
		Certificate:  types.StringValue("cert"),
		PrivateKey:   types.StringValue("key"),
	}
	// The Platform API marks every TLS response field optional, so a response may omit tlsType.
	updateTLSState(&model, deploymentTLSResponse{Status: "running", CommonName: "example.com"})

	if model.TLSType.ValueString() != "two-way" {
		t.Fatalf("configured tls_type was overwritten with %q", model.TLSType.ValueString())
	}
	if model.Status.ValueString() != "running" || model.CommonName.ValueString() != "example.com" {
		t.Fatalf("readable remote fields did not refresh: %#v", model)
	}
}

func testTLSResource(t *testing.T, endpoint string) *deploymentTLSResource {
	t.Helper()
	return &deploymentTLSResource{
		platform:     testAPIClient(t, endpoint),
		pollInterval: time.Nanosecond,
		timeout:      time.Second,
	}
}

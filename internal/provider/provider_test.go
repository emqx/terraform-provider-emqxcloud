package provider

import (
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestResolveCredentialsAllowsCompleteConfiguration(t *testing.T) {
	t.Parallel()

	credentials, err := resolveCredentials(credentialInput{
		name:      "Platform",
		endpoint:  types.StringValue("https://cloud.example/public_api/v1"),
		apiKey:    types.StringValue("key"),
		apiSecret: types.StringValue("secret"),
	})
	if err != nil {
		t.Fatalf("resolve credentials: %v", err)
	}
	if credentials == nil || credentials.endpoint != "https://cloud.example/public_api/v1" {
		t.Fatalf("unexpected credentials: %#v", credentials)
	}
}

func TestResolveCredentialsAllowsEmptyConfiguration(t *testing.T) {
	t.Parallel()

	credentials, err := resolveCredentials(credentialInput{name: "Platform"})
	if err != nil {
		t.Fatalf("resolve credentials: %v", err)
	}
	if credentials != nil {
		t.Fatalf("expected no credentials, got %#v", credentials)
	}
}

func TestResolveCredentialsRejectsPartialConfiguration(t *testing.T) {
	t.Parallel()

	_, err := resolveCredentials(credentialInput{
		name:     "Platform",
		endpoint: types.StringValue("https://cloud.example/public_api/v1"),
	})
	if err == nil || !strings.Contains(err.Error(), "must be configured together") {
		t.Fatalf("expected complete-group error, got %v", err)
	}
}

func TestResolveCredentialsUsesEnvironment(t *testing.T) {
	t.Setenv("TEST_ENDPOINT", "https://emqx.example/api/v5")
	t.Setenv("TEST_KEY", "key")
	t.Setenv("TEST_SECRET", "secret")

	credentials, err := resolveCredentials(credentialInput{
		name:         "Deployment",
		endpointEnv:  "TEST_ENDPOINT",
		apiKeyEnv:    "TEST_KEY",
		apiSecretEnv: "TEST_SECRET",
	})
	if err != nil {
		t.Fatalf("resolve credentials: %v", err)
	}
	if credentials == nil || credentials.apiKey != "key" || credentials.apiSecret != "secret" {
		t.Fatalf("unexpected environment credentials: %#v", credentials)
	}
}

func TestResolveCredentialsIgnoresLegacyEnvironment(t *testing.T) {
	t.Setenv("EMQXCLOUD_PLATFORM_ENDPOINT", "")
	t.Setenv("EMQXCLOUD_PLATFORM_API_KEY", "")
	t.Setenv("EMQXCLOUD_PLATFORM_API_SECRET", "")
	t.Setenv("EMQXCLOUD_ENDPOINT", "https://legacy.example/public_api/v1")
	t.Setenv("EMQXCLOUD_API_KEY", "legacy-key")
	t.Setenv("EMQXCLOUD_API_SECRET", "legacy-secret")

	credentials, err := resolveCredentials(credentialInput{
		name:         "Platform",
		endpointEnv:  "EMQXCLOUD_PLATFORM_ENDPOINT",
		apiKeyEnv:    "EMQXCLOUD_PLATFORM_API_KEY",
		apiSecretEnv: "EMQXCLOUD_PLATFORM_API_SECRET",
	})
	if err != nil || credentials != nil {
		t.Fatalf("legacy environment was used: credentials=%#v err=%v", credentials, err)
	}
}

func TestNewAPIClientRejectsWrongEndpointPath(t *testing.T) {
	t.Parallel()

	credentials := &credentialGroup{
		endpoint:  "https://example.com/api",
		apiKey:    "key",
		apiSecret: "secret",
	}
	for _, requiredPath := range []string{"/public_api/v1", "/api/v5"} {
		if _, err := newAPIClient(credentials, requiredPath, false); err == nil ||
			!strings.Contains(err.Error(), requiredPath) {
			t.Fatalf("expected endpoint path %s to be rejected, got %v", requiredPath, err)
		}
	}
}

func TestNewAPIClientRejectsHTTP(t *testing.T) {
	t.Parallel()

	credentials := &credentialGroup{
		endpoint:  "http://example.com/public_api/v1",
		apiKey:    "key",
		apiSecret: "secret",
	}
	if _, err := newAPIClient(credentials, "/public_api/v1", false); err == nil ||
		!strings.Contains(err.Error(), "must use https") {
		t.Fatalf("expected HTTPS validation error, got %v", err)
	}
}

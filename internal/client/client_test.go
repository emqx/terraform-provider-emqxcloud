package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientSendsBasicAuthAndEncodedPath(t *testing.T) {
	t.Parallel()

	var received map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		apiKey, apiSecret, ok := request.BasicAuth()
		if !ok || apiKey != "key" || apiSecret != "secret" {
			t.Fatalf("unexpected Basic Auth credentials")
		}
		if request.URL.EscapedPath() != "/api/v5/connectors/http%3Aname%20with%20space" {
			t.Fatalf("unexpected escaped path: %s", request.URL.EscapedPath())
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	apiClient, err := New(Options{
		Endpoint:  server.URL + "/api/v5",
		APIKey:    "key",
		APISecret: "secret",
		AllowHTTP: true,
	})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	_, err = apiClient.Do(context.Background(), Request{
		Method: http.MethodGet,
		Path:   "/connectors/" + EscapePathSegment("http:name with space"),
		Result: &received,
	})
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	if received["status"] != "ok" {
		t.Fatalf("unexpected response: %#v", received)
	}
}

func TestClientUsesRequestTimeout(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		time.Sleep(20 * time.Millisecond)
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	apiClient, err := New(Options{
		Endpoint:   server.URL,
		HTTPClient: &http.Client{Timeout: time.Millisecond},
		AllowHTTP:  true,
	})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	if _, err = apiClient.Do(context.Background(), Request{
		Method:  http.MethodPost,
		Path:    "/deployments",
		Timeout: time.Second,
	}); err != nil {
		t.Fatalf("send request with overridden timeout: %v", err)
	}
}

func TestClientRetryDelayBacksOffAndHonoursRetryAfter(t *testing.T) {
	t.Parallel()

	apiClient, err := New(Options{
		Endpoint:  "https://example.com",
		APIKey:    "key",
		APISecret: "secret",
		RetryWait: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}

	tests := []struct {
		name       string
		attempt    int
		retryAfter time.Duration
		expected   time.Duration
	}{
		{name: "first attempt", attempt: 1, expected: 200 * time.Millisecond},
		{name: "second attempt", attempt: 2, expected: 400 * time.Millisecond},
		{name: "third attempt", attempt: 3, expected: 800 * time.Millisecond},
		{name: "retry-after wins", attempt: 1, retryAfter: 2 * time.Second, expected: 2 * time.Second},
		{name: "retry-after capped", attempt: 1, retryAfter: time.Hour, expected: maxRetryWait},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if delay := apiClient.retryDelay(test.attempt, test.retryAfter); delay != test.expected {
				t.Fatalf("expected %s, got %s", test.expected, delay)
			}
		})
	}
}

func TestClientParsesRetryAfter(t *testing.T) {
	t.Parallel()

	if delay := parseRetryAfter("3"); delay != 3*time.Second {
		t.Fatalf("delay-seconds form: %s", delay)
	}
	if delay := parseRetryAfter(time.Now().Add(2 * time.Second).UTC().Format(http.TimeFormat)); delay <= 0 {
		t.Fatalf("HTTP-date form: %s", delay)
	}
	for _, value := range []string{"", "soon", "-1", "0"} {
		if delay := parseRetryAfter(value); delay != 0 {
			t.Fatalf("expected %q to be ignored, got %s", value, delay)
		}
	}
}

func TestClientWaitsForRetryAfterOnRateLimit(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			writer.Header().Set("Retry-After", "1")
			writer.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = writer.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	apiClient, err := New(Options{
		Endpoint:  server.URL,
		APIKey:    "key",
		APISecret: "secret",
		RetryWait: time.Nanosecond,
		AllowHTTP: true,
	})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	start := time.Now()
	var result map[string]string
	if _, err = apiClient.Do(context.Background(), Request{
		Method: http.MethodGet,
		Path:   "/resource",
		Result: &result,
	}); err != nil {
		t.Fatalf("send request: %v", err)
	}
	if elapsed := time.Since(start); elapsed < time.Second {
		t.Fatalf("Retry-After was ignored, retried after %s", elapsed)
	}
	if attempts.Load() != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts.Load())
	}
}

func TestClientRetriesOnlyRetryableGet(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) < 3 {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = writer.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	apiClient, err := New(Options{
		Endpoint:  server.URL,
		APIKey:    "key",
		APISecret: "secret",
		RetryWait: time.Nanosecond,
		AllowHTTP: true,
	})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	var result map[string]string
	_, err = apiClient.Do(context.Background(), Request{
		Method: http.MethodGet,
		Path:   "/resource",
		Result: &result,
	})
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	if attempts.Load() != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts.Load())
	}
}

func TestClientDoesNotRetryWrite(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		writer.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	apiClient, err := New(Options{
		Endpoint:  server.URL,
		APIKey:    "key",
		APISecret: "secret",
		RetryWait: time.Nanosecond,
		AllowHTTP: true,
	})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	_, err = apiClient.Do(context.Background(), Request{
		Method: http.MethodPost,
		Path:   "/resource",
		Body:   map[string]string{"name": "example"},
	})
	if !IsStatus(err, http.StatusServiceUnavailable) {
		t.Fatalf("expected HTTP 503 error, got %v", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("expected 1 attempt, got %d", attempts.Load())
	}
}

func TestClientErrorDoesNotExposeResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"code":   "BAD_REQUEST",
			"detail": "private-key-value",
		})
	}))
	defer server.Close()

	apiClient, err := New(Options{
		Endpoint:  server.URL,
		APIKey:    "key",
		APISecret: "api-secret-value",
		AllowHTTP: true,
	})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	_, err = apiClient.Do(context.Background(), Request{
		Method: http.MethodGet,
		Path:   "/resource",
	})
	if err == nil {
		t.Fatal("expected API error")
	}
	if !strings.Contains(err.Error(), "BAD_REQUEST") {
		t.Fatalf("expected safe error code, got %v", err)
	}
	for _, secret := range []string{"private-key-value", "api-secret-value"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error exposed secret %q: %v", secret, err)
		}
	}
}

func TestNewRejectsInvalidEndpoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		endpoint string
		expected string
	}{
		{endpoint: "ftp://example.com", expected: "must use https"},
		{endpoint: "http://example.com", expected: "must use https"},
		{endpoint: "https:///api", expected: "include a host"},
		{endpoint: "https://user:password@example.com", expected: "user information"},
		{endpoint: "https://example.com/api?region=us", expected: "query or fragment"},
		{endpoint: "https://example.com/api#region", expected: "query or fragment"},
	}
	for _, test := range tests {
		if _, err := New(Options{Endpoint: test.endpoint}); err == nil ||
			!strings.Contains(err.Error(), test.expected) {
			t.Errorf("expected %q to be rejected with %q, got %v", test.endpoint, test.expected, err)
		}
	}
	if _, err := New(Options{Endpoint: "http://example.com", AllowHTTP: true}); err == nil {
		t.Fatal("AllowHTTP accepted a non-loopback endpoint")
	}
}

func TestClientFollowsSameOriginRedirect(t *testing.T) {
	t.Parallel()

	var received map[string]string
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/redirect" {
			http.Redirect(writer, request, "/result", http.StatusFound)
			return
		}
		apiKey, apiSecret, ok := request.BasicAuth()
		if !ok || apiKey != "key" || apiSecret != "secret" {
			t.Error("redirected request did not retain Basic Auth")
		}
		_, _ = writer.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	apiClient, err := New(Options{
		Endpoint:   server.URL,
		APIKey:     "key",
		APISecret:  "secret",
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	if _, err = apiClient.Do(context.Background(), Request{
		Method: http.MethodGet,
		Path:   "/redirect",
		Result: &received,
	}); err != nil {
		t.Fatalf("follow same-origin redirect: %v", err)
	}
	if received["status"] != "ok" {
		t.Fatalf("unexpected response: %#v", received)
	}
}

func TestClientRejectsUnsafeRedirects(t *testing.T) {
	t.Parallel()

	var crossOriginReached atomic.Bool
	target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		crossOriginReached.Store(true)
	}))
	defer target.Close()

	tests := []struct {
		name     string
		location func(*http.Request) string
		expected string
	}{
		{
			name:     "cross origin",
			location: func(*http.Request) string { return target.URL },
			expected: "changed the configured endpoint origin",
		},
		{
			name:     "protocol downgrade",
			location: func(request *http.Request) string { return "http://" + request.Host + "/leak" },
			expected: "changed the configured endpoint origin",
		},
		{
			name: "redirect userinfo",
			location: func(request *http.Request) string {
				return "https://user:password@" + request.Host + "/leak"
			},
			expected: "must not include user information",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				http.Redirect(writer, request, test.location(request), http.StatusFound)
			}))
			defer server.Close()

			apiClient, err := New(Options{Endpoint: server.URL, HTTPClient: server.Client()})
			if err != nil {
				t.Fatalf("create client: %v", err)
			}
			if _, err = apiClient.Do(context.Background(), Request{
				Method: http.MethodGet,
				Path:   "/redirect",
			}); err == nil || !strings.Contains(err.Error(), test.expected) {
				t.Fatalf("expected redirect rejection %q, got %v", test.expected, err)
			}
		})
	}
	if crossOriginReached.Load() {
		t.Fatal("cross-origin redirect was sent")
	}
}

func TestClientPreservesRedirectPolicyWithoutMutatingHTTPClient(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, "/result", http.StatusFound)
	}))
	defer server.Close()

	policyError := errors.New("caller rejected redirect")
	policyCalled := false
	httpClient := server.Client()
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		policyCalled = true
		return policyError
	}
	apiClient, err := New(Options{Endpoint: server.URL, HTTPClient: httpClient})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	if apiClient.http == httpClient {
		t.Fatal("client.New mutated the supplied HTTP client")
	}
	if _, err = apiClient.Do(context.Background(), Request{
		Method: http.MethodGet,
		Path:   "/redirect",
	}); !errors.Is(err, policyError) {
		t.Fatalf("expected caller redirect policy error, got %v", err)
	}
	if !policyCalled {
		t.Fatal("caller redirect policy was not used")
	}
}

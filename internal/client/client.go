package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultTimeout   = 30 * time.Second
	defaultRetryWait = 200 * time.Millisecond
	maxRetryWait     = 5 * time.Second
	maxAttempts      = 3
	maxResponseBytes = 4 << 20
)

type Options struct {
	Endpoint   string
	APIKey     string
	APISecret  string
	HTTPClient *http.Client
	RetryWait  time.Duration
	// AllowHTTP permits plaintext loopback endpoints for hermetic tests.
	AllowHTTP bool
}

type Request struct {
	Method  string
	Path    string
	Query   url.Values
	Body    any
	Result  any
	Timeout time.Duration
}

type Client struct {
	endpoint  *url.URL
	apiKey    string
	apiSecret string
	http      *http.Client
	retryWait time.Duration
}

type APIError struct {
	StatusCode int
	Code       string
}

func (e *APIError) Error() string {
	status := http.StatusText(e.StatusCode)
	if status == "" {
		status = "unknown status"
	}
	if e.Code == "" {
		return fmt.Sprintf("API request failed: HTTP %d %s", e.StatusCode, status)
	}
	return fmt.Sprintf("API request failed: HTTP %d %s (code %s)", e.StatusCode, status, e.Code)
}

func New(options Options) (*Client, error) {
	endpoint, err := url.Parse(strings.TrimRight(options.Endpoint, "/"))
	if err != nil {
		return nil, fmt.Errorf("parse API endpoint: %w", err)
	}
	if endpoint.Scheme != "https" {
		ip := net.ParseIP(endpoint.Hostname())
		if endpoint.Scheme != "http" || !options.AllowHTTP || ip == nil || !ip.IsLoopback() {
			return nil, fmt.Errorf("API endpoint must use https")
		}
	}
	if endpoint.Host == "" {
		return nil, fmt.Errorf("API endpoint must include a host")
	}
	if endpoint.User != nil {
		return nil, fmt.Errorf("API endpoint must not include user information")
	}
	if endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, fmt.Errorf("API endpoint must not include a query or fragment")
	}

	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultTimeout}
	}
	httpClientCopy := *httpClient
	checkRedirect := httpClientCopy.CheckRedirect
	httpClientCopy.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if request.URL.Scheme != endpoint.Scheme || request.URL.Host != endpoint.Host {
			return errors.New("API redirect changed the configured endpoint origin")
		}
		if request.URL.User != nil {
			return errors.New("API redirect must not include user information")
		}
		if checkRedirect != nil {
			return checkRedirect(request, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	retryWait := options.RetryWait
	if retryWait <= 0 {
		retryWait = defaultRetryWait
	}

	apiClient := &Client{
		endpoint:  endpoint,
		apiKey:    options.APIKey,
		apiSecret: options.APISecret,
		http:      &httpClientCopy,
		retryWait: retryWait,
	}
	return apiClient, nil
}

func (c *Client) Do(ctx context.Context, request Request) (int, error) {
	method := strings.ToUpper(request.Method)
	body, err := encodeBody(request.Body)
	if err != nil {
		return 0, err
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		statusCode, retryAfter, retry, requestErr := c.doOnce(ctx, method, request, body)
		if !retry || method != http.MethodGet || attempt == maxAttempts {
			return statusCode, requestErr
		}
		select {
		case <-ctx.Done():
			return statusCode, fmt.Errorf("wait to retry API request: %w", ctx.Err())
		case <-time.After(c.retryDelay(attempt, retryAfter)):
		}
	}
	return 0, errors.New("API request exhausted retry attempts")
}

// retryDelay backs off exponentially so a rate-limited API is not retried immediately, and
// prefers a bounded Retry-After hint when the API sends one.
func (c *Client) retryDelay(attempt int, retryAfter time.Duration) time.Duration {
	delay := retryAfter
	if delay <= 0 {
		delay = c.retryWait << (attempt - 1)
	}
	if delay > maxRetryWait {
		return maxRetryWait
	}
	return delay
}

func (c *Client) doOnce(ctx context.Context,
	method string,
	request Request,
	body []byte) (int, time.Duration, bool, error) {
	requestURL, err := c.requestURL(request.Path, request.Query)
	if err != nil {
		return 0, 0, false, err
	}

	httpRequest, err := http.NewRequestWithContext(ctx, method, requestURL, bytes.NewReader(body))
	if err != nil {
		return 0, 0, false, fmt.Errorf("create API request: %w", err)
	}
	httpRequest.SetBasicAuth(c.apiKey, c.apiSecret)
	httpRequest.Header.Set("Accept", "application/json")
	if request.Body != nil {
		httpRequest.Header.Set("Content-Type", "application/json")
	}

	httpClient := c.http
	if request.Timeout > 0 {
		clientCopy := *httpClient
		clientCopy.Timeout = request.Timeout
		httpClient = &clientCopy
	}
	response, err := httpClient.Do(httpRequest)
	if err != nil {
		return 0, 0, false, fmt.Errorf("send API request: %w", err)
	}
	defer response.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return response.StatusCode, 0, false, fmt.Errorf("read API response: %w", err)
	}
	if len(responseBody) > maxResponseBytes {
		return response.StatusCode, 0, false, errors.New("API response exceeds 4 MiB limit")
	}

	if response.StatusCode >= http.StatusBadRequest {
		apiErr := newAPIError(response.StatusCode, responseBody)
		retryAfter := parseRetryAfter(response.Header.Get("Retry-After"))
		return response.StatusCode, retryAfter, isRetryable(response.StatusCode), apiErr
	}
	if request.Result == nil || len(bytes.TrimSpace(responseBody)) == 0 {
		return response.StatusCode, 0, false, nil
	}
	if err := json.Unmarshal(responseBody, request.Result); err != nil {
		return response.StatusCode, 0, false, fmt.Errorf("decode API response: %w", err)
	}
	return response.StatusCode, 0, false, nil
}

// parseRetryAfter reads the delay-seconds and HTTP-date forms of Retry-After, ignoring any
// value the API cannot be trusted to have sent correctly.
func parseRetryAfter(value string) time.Duration {
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if deadline, err := http.ParseTime(value); err == nil {
		if delay := time.Until(deadline); delay > 0 {
			return delay
		}
	}
	return 0
}

func (c *Client) requestURL(path string, query url.Values) (string, error) {
	rawURL := strings.TrimRight(c.endpoint.String(), "/") + "/" + strings.TrimLeft(path, "/")
	requestURL, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("build API URL: %w", err)
	}
	if requestURL.Host != c.endpoint.Host || requestURL.Scheme != c.endpoint.Scheme {
		return "", errors.New("API path changed the configured endpoint")
	}
	requestURL.RawQuery = query.Encode()
	return requestURL.String(), nil
}

func encodeBody(payload any) ([]byte, error) {
	if payload == nil {
		return nil, nil
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode API request: %w", err)
	}
	return body, nil
}

func newAPIError(statusCode int, body []byte) *APIError {
	var payload struct {
		Code any `json:"code"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return &APIError{StatusCode: statusCode}
	}
	code := safeErrorCode(payload.Code)
	return &APIError{StatusCode: statusCode, Code: code}
}

func safeErrorCode(value any) string {
	var code string
	switch typedValue := value.(type) {
	case string:
		code = typedValue
	case float64:
		code = strconv.FormatFloat(typedValue, 'f', -1, 64)
	default:
		return ""
	}
	if len(code) > 128 {
		return ""
	}
	for _, char := range code {
		if (char >= 'a' && char <= 'z') ||
			(char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') ||
			strings.ContainsRune("._:-", char) {
			continue
		}
		return ""
	}
	return code
}

func isRetryable(statusCode int) bool {
	return statusCode == http.StatusTooManyRequests ||
		statusCode == http.StatusBadGateway ||
		statusCode == http.StatusServiceUnavailable ||
		statusCode == http.StatusGatewayTimeout
}

func EscapePathSegment(value string) string {
	escaped := url.QueryEscape(value)
	return strings.ReplaceAll(escaped, "+", "%20")
}

func IsStatus(err error, statusCode int) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == statusCode
}

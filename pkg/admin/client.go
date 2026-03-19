package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Error is returned when the admin API returns a non-2xx status code.
type Error struct {
	StatusCode int
	Message    string
}

func (e *Error) Error() string {
	return fmt.Sprintf("admin API error (HTTP %d): %s", e.StatusCode, e.Message)
}

// Client is a Go client for the llm-proxy admin API.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// NewClient creates a new admin API client.
//
//	client := admin.NewClient("http://llm-proxy-admin:9003", os.Getenv("ADMIN_API_KEY"))
//
// Pass an optional *http.Client to customize timeouts, TLS, etc.
// If nil or omitted, http.DefaultClient is used.
func NewClient(baseURL string, adminAPIKey string, httpClient ...*http.Client) *Client {
	c := &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     adminAPIKey,
		httpClient: http.DefaultClient,
	}
	if len(httpClient) > 0 && httpClient[0] != nil {
		c.httpClient = httpClient[0]
	}
	return c
}

// CreateKey creates a new managed API key.
// Returns the created key (with actual_key redacted).
func (c *Client) CreateKey(ctx context.Context, req CreateKeyRequest) (*APIKey, error) {
	var key APIKey
	if err := c.do(ctx, http.MethodPost, "/admin/keys", req, &key); err != nil {
		return nil, err
	}
	return &key, nil
}

// GetKey retrieves a managed API key by its iw: identifier.
// Returns the key regardless of enabled/expired status (with actual_key redacted).
func (c *Client) GetKey(ctx context.Context, key string) (*APIKey, error) {
	var apiKey APIKey
	if err := c.do(ctx, http.MethodGet, "/admin/keys/"+key, nil, &apiKey); err != nil {
		return nil, err
	}
	return &apiKey, nil
}

// UpdateKey updates fields on an existing key. Only non-nil fields in the
// request are updated. Use the pointer helpers (String, Bool, etc.)
// to set fields:
//
//	client.UpdateKey(ctx, "iw:abc123", admin.UpdateKeyRequest{
//	    Enabled: admin.Bool(false),
//	})
func (c *Client) UpdateKey(ctx context.Context, key string, req UpdateKeyRequest) error {
	return c.do(ctx, http.MethodPatch, "/admin/keys/"+key, req, nil)
}

// DeleteKey permanently deletes a managed API key.
func (c *Client) DeleteKey(ctx context.Context, key string) error {
	return c.do(ctx, http.MethodDelete, "/admin/keys/"+key, nil, nil)
}

// do performs an HTTP request to the admin API.
// If body is non-nil, it is JSON-encoded and sent as the request body.
// If dest is non-nil, the response body is JSON-decoded into it.
func (c *Client) do(ctx context.Context, method, path string, body interface{}, dest interface{}) error {
	var bodyReader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode >= 400 {
		var errResp ErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error != "" {
			return &Error{StatusCode: resp.StatusCode, Message: errResp.Error}
		}
		return &Error{StatusCode: resp.StatusCode, Message: string(respBody)}
	}

	if dest != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, dest); err != nil {
			return fmt.Errorf("failed to decode response: %w", err)
		}
	}

	return nil
}

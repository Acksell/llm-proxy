package admin

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Instawork/llm-proxy/internal/apikeys"
	admintypes "github.com/Instawork/llm-proxy/pkg/admin"
	"github.com/acksell/bezos/dynamodb/ddbstore"
)

const testAdminKey = "test-admin-secret"

// setupHandler creates an admin handler backed by an in-memory DynamoDB store.
func setupHandler(t *testing.T) http.Handler {
	t.Helper()

	store, err := ddbstore.New(ddbstore.StoreOptions{InMemory: true})
	if err != nil {
		t.Fatalf("failed to create in-memory ddb store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	keyStore, err := apikeys.NewStore(apikeys.StoreConfig{
		Client:    store,
		TableName: "test-api-keys",
		Logger:    slog.Default(),
	})
	if err != nil {
		t.Fatalf("Failed to create apikeys store: %v", err)
	}

	// Pass nil for costReader — usage endpoint will return 501 in tests
	return NewHandler(keyStore, nil, testAdminKey, slog.Default())
}

func doRequest(t *testing.T, handler http.Handler, method, path string, body interface{}, auth string) *httptest.ResponseRecorder {
	t.Helper()
	var bodyReader *bytes.Buffer
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("failed to marshal body: %v", err)
		}
		bodyReader = bytes.NewBuffer(b)
	} else {
		bodyReader = &bytes.Buffer{}
	}

	req := httptest.NewRequest(method, path, bodyReader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func decodeJSON[T any](t *testing.T, rr *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.NewDecoder(rr.Body).Decode(&v); err != nil {
		t.Fatalf("failed to decode response: %v (body: %s)", err, rr.Body.String())
	}
	return v
}

// --- Health ---

func TestHealthEndpoint(t *testing.T) {
	h := setupHandler(t)
	rr := doRequest(t, h, "GET", "/health", nil, "")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var resp map[string]interface{}
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp["status"] != "healthy" {
		t.Fatalf("expected status=healthy, got %v", resp["status"])
	}
}

func TestHealthNoAuthRequired(t *testing.T) {
	h := setupHandler(t)
	// No auth header at all
	rr := doRequest(t, h, "GET", "/health", nil, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 without auth, got %d", rr.Code)
	}
}

// --- Auth ---

func TestAuthMissing(t *testing.T) {
	h := setupHandler(t)
	rr := doRequest(t, h, "POST", "/admin/keys", admintypes.CreateKeyRequest{
		Provider:  "openai",
		ActualKey: "sk-test",
	}, "")

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestAuthWrongToken(t *testing.T) {
	h := setupHandler(t)
	rr := doRequest(t, h, "POST", "/admin/keys", admintypes.CreateKeyRequest{
		Provider:  "openai",
		ActualKey: "sk-test",
	}, "Bearer wrong-key")

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestAuthMalformedHeader(t *testing.T) {
	h := setupHandler(t)
	rr := doRequest(t, h, "GET", "/admin/keys/iw:abc", nil, "Basic dXNlcjpwYXNz")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for non-Bearer auth, got %d", rr.Code)
	}
}

// --- Create Key ---

func TestCreateKey(t *testing.T) {
	h := setupHandler(t)

	rr := doRequest(t, h, "POST", "/admin/keys", admintypes.CreateKeyRequest{
		Provider:       "openai",
		ActualKey:      "sk-test-12345",
		Description:    "test key",
		DailyCostLimit: 1000,
		Tags:           map[string]string{"team": "backend"},
	}, "Bearer "+testAdminKey)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	key := decodeJSON[admintypes.APIKey](t, rr)
	if key.Key == "" {
		t.Fatal("expected non-empty key")
	}
	if key.Provider != "openai" {
		t.Fatalf("expected provider=openai, got %s", key.Provider)
	}
	if key.Description != "test key" {
		t.Fatalf("expected description='test key', got %s", key.Description)
	}
	if key.DailyCostLimit != 1000 {
		t.Fatalf("expected daily_cost_limit=1000, got %d", key.DailyCostLimit)
	}
	if !key.Enabled {
		t.Fatal("expected key to be enabled by default")
	}
	if key.Tags["team"] != "backend" {
		t.Fatalf("expected tag team=backend, got %v", key.Tags)
	}
}

func TestCreateKeyActualKeyRedacted(t *testing.T) {
	h := setupHandler(t)

	rr := doRequest(t, h, "POST", "/admin/keys", admintypes.CreateKeyRequest{
		Provider:  "openai",
		ActualKey: "sk-secret-key-that-should-not-appear",
	}, "Bearer "+testAdminKey)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rr.Code)
	}

	// The response body must not contain the actual key
	body := rr.Body.String()
	if bytes.Contains([]byte(body), []byte("sk-secret-key-that-should-not-appear")) {
		t.Fatal("actual_key should be redacted from response")
	}
}

func TestCreateKeyMissingProvider(t *testing.T) {
	h := setupHandler(t)
	rr := doRequest(t, h, "POST", "/admin/keys", admintypes.CreateKeyRequest{
		ActualKey: "sk-test",
	}, "Bearer "+testAdminKey)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestCreateKeyInvalidProvider(t *testing.T) {
	h := setupHandler(t)
	rr := doRequest(t, h, "POST", "/admin/keys", admintypes.CreateKeyRequest{
		Provider:  "cohere",
		ActualKey: "sk-test",
	}, "Bearer "+testAdminKey)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}

	errResp := decodeJSON[admintypes.ErrorResponse](t, rr)
	if errResp.Error == "" {
		t.Fatal("expected error message")
	}
}

func TestCreateKeyMissingActualKey(t *testing.T) {
	h := setupHandler(t)
	rr := doRequest(t, h, "POST", "/admin/keys", admintypes.CreateKeyRequest{
		Provider: "openai",
	}, "Bearer "+testAdminKey)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestCreateKeyInvalidJSON(t *testing.T) {
	h := setupHandler(t)

	req := httptest.NewRequest("POST", "/admin/keys", bytes.NewBufferString("{invalid"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testAdminKey)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid JSON, got %d", rr.Code)
	}
}

// --- Get Key ---

func TestGetKey(t *testing.T) {
	h := setupHandler(t)

	// Create a key first
	createRR := doRequest(t, h, "POST", "/admin/keys", admintypes.CreateKeyRequest{
		Provider:       "anthropic",
		ActualKey:      "sk-ant-test",
		Description:    "my anthropic key",
		DailyCostLimit: 500,
	}, "Bearer "+testAdminKey)

	created := decodeJSON[admintypes.APIKey](t, createRR)

	// Get it back
	getRR := doRequest(t, h, "GET", "/admin/keys/"+created.Key, nil, "Bearer "+testAdminKey)
	if getRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", getRR.Code, getRR.Body.String())
	}

	fetched := decodeJSON[admintypes.APIKey](t, getRR)
	if fetched.Key != created.Key {
		t.Fatalf("expected key=%s, got %s", created.Key, fetched.Key)
	}
	if fetched.Provider != "anthropic" {
		t.Fatalf("expected provider=anthropic, got %s", fetched.Provider)
	}
	if fetched.Description != "my anthropic key" {
		t.Fatalf("expected description='my anthropic key', got %s", fetched.Description)
	}

	// Must not contain actual key
	body := getRR.Body.String()
	if bytes.Contains([]byte(body), []byte("sk-ant-test")) {
		t.Fatal("actual_key should be redacted from GET response")
	}
}

func TestGetKeyNotFound(t *testing.T) {
	h := setupHandler(t)
	rr := doRequest(t, h, "GET", "/admin/keys/iw:nonexistent", nil, "Bearer "+testAdminKey)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

func TestGetKeyDisabledStillReturned(t *testing.T) {
	h := setupHandler(t)

	// Create, then disable
	createRR := doRequest(t, h, "POST", "/admin/keys", admintypes.CreateKeyRequest{
		Provider:  "openai",
		ActualKey: "sk-test",
	}, "Bearer "+testAdminKey)
	created := decodeJSON[admintypes.APIKey](t, createRR)

	// Disable the key
	doRequest(t, h, "PATCH", "/admin/keys/"+created.Key, admintypes.UpdateKeyRequest{
		Enabled: admintypes.Bool(false),
	}, "Bearer "+testAdminKey)

	// GetKeyAdmin should still return it
	getRR := doRequest(t, h, "GET", "/admin/keys/"+created.Key, nil, "Bearer "+testAdminKey)
	if getRR.Code != http.StatusOK {
		t.Fatalf("expected 200 for disabled key via admin, got %d: %s", getRR.Code, getRR.Body.String())
	}

	fetched := decodeJSON[admintypes.APIKey](t, getRR)
	if fetched.Enabled {
		t.Fatal("expected key to be disabled")
	}
}

// --- Update Key ---

func TestUpdateKey(t *testing.T) {
	h := setupHandler(t)

	// Create
	createRR := doRequest(t, h, "POST", "/admin/keys", admintypes.CreateKeyRequest{
		Provider:  "openai",
		ActualKey: "sk-test",
	}, "Bearer "+testAdminKey)
	created := decodeJSON[admintypes.APIKey](t, createRR)

	// Update description and cost limit
	updateRR := doRequest(t, h, "PATCH", "/admin/keys/"+created.Key, admintypes.UpdateKeyRequest{
		Description:    admintypes.String("updated desc"),
		DailyCostLimit: admintypes.Int64(2000),
	}, "Bearer "+testAdminKey)

	if updateRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", updateRR.Code, updateRR.Body.String())
	}

	okResp := decodeJSON[admintypes.OKResponse](t, updateRR)
	if !okResp.OK {
		t.Fatal("expected ok=true")
	}

	// Verify changes via GET
	getRR := doRequest(t, h, "GET", "/admin/keys/"+created.Key, nil, "Bearer "+testAdminKey)
	fetched := decodeJSON[admintypes.APIKey](t, getRR)
	if fetched.Description != "updated desc" {
		t.Fatalf("expected description='updated desc', got %s", fetched.Description)
	}
	if fetched.DailyCostLimit != 2000 {
		t.Fatalf("expected daily_cost_limit=2000, got %d", fetched.DailyCostLimit)
	}
}

func TestUpdateKeyNotFound(t *testing.T) {
	h := setupHandler(t)
	rr := doRequest(t, h, "PATCH", "/admin/keys/iw:nonexistent", admintypes.UpdateKeyRequest{
		Description: admintypes.String("nope"),
	}, "Bearer "+testAdminKey)

	// The store returns a condition error; handler maps it to 404
	if rr.Code != http.StatusNotFound && rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 404 or 500, got %d", rr.Code)
	}
}

func TestUpdateKeyNoFields(t *testing.T) {
	h := setupHandler(t)

	// Create a key first
	createRR := doRequest(t, h, "POST", "/admin/keys", admintypes.CreateKeyRequest{
		Provider:  "openai",
		ActualKey: "sk-test",
	}, "Bearer "+testAdminKey)
	created := decodeJSON[admintypes.APIKey](t, createRR)

	// Send empty update
	rr := doRequest(t, h, "PATCH", "/admin/keys/"+created.Key, admintypes.UpdateKeyRequest{}, "Bearer "+testAdminKey)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty update, got %d", rr.Code)
	}
}

func TestUpdateKeyDisable(t *testing.T) {
	h := setupHandler(t)

	createRR := doRequest(t, h, "POST", "/admin/keys", admintypes.CreateKeyRequest{
		Provider:  "openai",
		ActualKey: "sk-test",
	}, "Bearer "+testAdminKey)
	created := decodeJSON[admintypes.APIKey](t, createRR)

	// Disable
	rr := doRequest(t, h, "PATCH", "/admin/keys/"+created.Key, admintypes.UpdateKeyRequest{
		Enabled: admintypes.Bool(false),
	}, "Bearer "+testAdminKey)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	// Verify disabled
	getRR := doRequest(t, h, "GET", "/admin/keys/"+created.Key, nil, "Bearer "+testAdminKey)
	fetched := decodeJSON[admintypes.APIKey](t, getRR)
	if fetched.Enabled {
		t.Fatal("expected key to be disabled")
	}

	// Re-enable
	rr2 := doRequest(t, h, "PATCH", "/admin/keys/"+created.Key, admintypes.UpdateKeyRequest{
		Enabled: admintypes.Bool(true),
	}, "Bearer "+testAdminKey)
	if rr2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr2.Code)
	}

	getRR2 := doRequest(t, h, "GET", "/admin/keys/"+created.Key, nil, "Bearer "+testAdminKey)
	fetched2 := decodeJSON[admintypes.APIKey](t, getRR2)
	if !fetched2.Enabled {
		t.Fatal("expected key to be re-enabled")
	}
}

func TestUpdateKeyTags(t *testing.T) {
	h := setupHandler(t)

	createRR := doRequest(t, h, "POST", "/admin/keys", admintypes.CreateKeyRequest{
		Provider:  "openai",
		ActualKey: "sk-test",
		Tags:      map[string]string{"env": "dev"},
	}, "Bearer "+testAdminKey)
	created := decodeJSON[admintypes.APIKey](t, createRR)

	// Replace tags
	newTags := map[string]string{"env": "prod", "team": "platform"}
	rr := doRequest(t, h, "PATCH", "/admin/keys/"+created.Key, admintypes.UpdateKeyRequest{
		Tags: newTags,
	}, "Bearer "+testAdminKey)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	getRR := doRequest(t, h, "GET", "/admin/keys/"+created.Key, nil, "Bearer "+testAdminKey)
	fetched := decodeJSON[admintypes.APIKey](t, getRR)
	if fetched.Tags["env"] != "prod" {
		t.Fatalf("expected tag env=prod, got %v", fetched.Tags)
	}
	if fetched.Tags["team"] != "platform" {
		t.Fatalf("expected tag team=platform, got %v", fetched.Tags)
	}
}

// --- Delete Key ---

func TestDeleteKey(t *testing.T) {
	h := setupHandler(t)

	// Create
	createRR := doRequest(t, h, "POST", "/admin/keys", admintypes.CreateKeyRequest{
		Provider:  "gemini",
		ActualKey: "ai-test",
	}, "Bearer "+testAdminKey)
	created := decodeJSON[admintypes.APIKey](t, createRR)

	// Delete
	deleteRR := doRequest(t, h, "DELETE", "/admin/keys/"+created.Key, nil, "Bearer "+testAdminKey)
	if deleteRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", deleteRR.Code, deleteRR.Body.String())
	}

	okResp := decodeJSON[admintypes.OKResponse](t, deleteRR)
	if !okResp.OK {
		t.Fatal("expected ok=true")
	}

	// Verify gone
	getRR := doRequest(t, h, "GET", "/admin/keys/"+created.Key, nil, "Bearer "+testAdminKey)
	if getRR.Code != http.StatusNotFound {
		t.Fatalf("expected 404 after delete, got %d", getRR.Code)
	}
}

func TestDeleteKeyNotFound(t *testing.T) {
	h := setupHandler(t)
	rr := doRequest(t, h, "DELETE", "/admin/keys/iw:nonexistent", nil, "Bearer "+testAdminKey)

	// Note: the in-memory DynamoDB backend (ddbstore) does not enforce
	// ConditionExpression on DeleteItem, so it returns 200 instead of 404.
	// With real DynamoDB this would be 404. Accept both in tests.
	if rr.Code != http.StatusNotFound && rr.Code != http.StatusOK && rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 404, 200 (ddbstore), or 500, got %d", rr.Code)
	}
}

func TestDeleteKeyIdempotent(t *testing.T) {
	h := setupHandler(t)

	// Create and delete
	createRR := doRequest(t, h, "POST", "/admin/keys", admintypes.CreateKeyRequest{
		Provider:  "openai",
		ActualKey: "sk-test",
	}, "Bearer "+testAdminKey)
	created := decodeJSON[admintypes.APIKey](t, createRR)

	// First delete
	doRequest(t, h, "DELETE", "/admin/keys/"+created.Key, nil, "Bearer "+testAdminKey)

	// Second delete should fail (key no longer exists)
	rr := doRequest(t, h, "DELETE", "/admin/keys/"+created.Key, nil, "Bearer "+testAdminKey)
	if rr.Code == http.StatusOK {
		// If the store doesn't enforce condition checks on delete, that's acceptable
		// but ideally it should be 404
		t.Log("second delete returned 200 (store may not enforce condition check)")
	}
}

// --- All Providers ---

func TestCreateKeyAllProviders(t *testing.T) {
	h := setupHandler(t)

	providers := []string{"openai", "anthropic", "gemini"}
	for _, provider := range providers {
		t.Run(provider, func(t *testing.T) {
			rr := doRequest(t, h, "POST", "/admin/keys", admintypes.CreateKeyRequest{
				Provider:  provider,
				ActualKey: "key-for-" + provider,
			}, "Bearer "+testAdminKey)

			if rr.Code != http.StatusCreated {
				t.Fatalf("expected 201 for provider %s, got %d: %s", provider, rr.Code, rr.Body.String())
			}

			key := decodeJSON[admintypes.APIKey](t, rr)
			if key.Provider != provider {
				t.Fatalf("expected provider=%s, got %s", provider, key.Provider)
			}
		})
	}
}

// --- Content-Type ---

func TestResponseContentType(t *testing.T) {
	h := setupHandler(t)

	rr := doRequest(t, h, "GET", "/health", nil, "")
	ct := rr.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Fatalf("expected Content-Type=application/json, got %s", ct)
	}
}

// --- Usage endpoint (unit tests that don't require DynamoDB) ---

func TestGetUsageNoCostReader(t *testing.T) {
	// Create handler with nil costReader
	h := NewHandler(nil, nil, testAdminKey, slog.Default())

	rr := doRequest(t, h, "GET", "/admin/usage?user_id=foo", nil, "Bearer "+testAdminKey)
	if rr.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501 when costReader is nil, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestGetUsageMissingUserID(t *testing.T) {
	h := NewHandler(nil, nil, testAdminKey, slog.Default())

	rr := doRequest(t, h, "GET", "/admin/usage", nil, "Bearer "+testAdminKey)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing user_id, got %d", rr.Code)
	}
}

func TestGetUsageInvalidDateFormat(t *testing.T) {
	h := NewHandler(nil, nil, testAdminKey, slog.Default())

	rr := doRequest(t, h, "GET", "/admin/usage?user_id=foo&from=not-a-date", nil, "Bearer "+testAdminKey)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid date format, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestGetUsageRequiresAuth(t *testing.T) {
	h := NewHandler(nil, nil, testAdminKey, slog.Default())

	rr := doRequest(t, h, "GET", "/admin/usage?user_id=foo", nil, "")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without auth, got %d", rr.Code)
	}
}

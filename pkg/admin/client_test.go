package admin_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http/httptest"
	"testing"

	internaladmin "github.com/Instawork/llm-proxy/internal/admin"
	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/pkg/admin"
	"github.com/acksell/bezos/dynamodb/ddbstore"
)

const testAdminKey = "test-admin-secret"

// setupServer returns a running httptest.Server and a Client connected to it.
func setupServer(t *testing.T) (*admin.Client, *httptest.Server) {
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
		t.Fatalf("failed to create apikeys store: %v", err)
	}

	handler := internaladmin.NewHandler(keyStore, testAdminKey, slog.Default())
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client := admin.NewClient(server.URL, testAdminKey)
	return client, server
}

func TestClientCreateKey(t *testing.T) {
	client, _ := setupServer(t)
	ctx := context.Background()

	key, err := client.CreateKey(ctx, admin.CreateKeyRequest{
		Provider:       "openai",
		ActualKey:      "sk-test-12345",
		Description:    "test key",
		DailyCostLimit: 1000,
		Tags:           map[string]string{"team": "backend"},
	})
	if err != nil {
		t.Fatalf("CreateKey failed: %v", err)
	}

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
		t.Fatal("expected key to be enabled")
	}
	if key.Tags["team"] != "backend" {
		t.Fatalf("expected tag team=backend, got %v", key.Tags)
	}
}

func TestClientGetKey(t *testing.T) {
	client, _ := setupServer(t)
	ctx := context.Background()

	// Create
	created, err := client.CreateKey(ctx, admin.CreateKeyRequest{
		Provider:  "anthropic",
		ActualKey: "sk-ant-test",
	})
	if err != nil {
		t.Fatalf("CreateKey failed: %v", err)
	}

	// Get
	fetched, err := client.GetKey(ctx, created.Key)
	if err != nil {
		t.Fatalf("GetKey failed: %v", err)
	}

	if fetched.Key != created.Key {
		t.Fatalf("expected key=%s, got %s", created.Key, fetched.Key)
	}
	if fetched.Provider != "anthropic" {
		t.Fatalf("expected provider=anthropic, got %s", fetched.Provider)
	}
}

func TestClientGetKeyNotFound(t *testing.T) {
	client, _ := setupServer(t)
	ctx := context.Background()

	_, err := client.GetKey(ctx, "iw:nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent key")
	}

	var apiErr *admin.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *admin.Error, got %T: %v", err, err)
	}
	if apiErr.StatusCode != 404 {
		t.Fatalf("expected 404, got %d", apiErr.StatusCode)
	}
}

func TestClientUpdateKey(t *testing.T) {
	client, _ := setupServer(t)
	ctx := context.Background()

	// Create
	created, err := client.CreateKey(ctx, admin.CreateKeyRequest{
		Provider:  "openai",
		ActualKey: "sk-test",
	})
	if err != nil {
		t.Fatalf("CreateKey failed: %v", err)
	}

	// Update
	err = client.UpdateKey(ctx, created.Key, admin.UpdateKeyRequest{
		Description:    admin.String("updated via client"),
		DailyCostLimit: admin.Int64(5000),
	})
	if err != nil {
		t.Fatalf("UpdateKey failed: %v", err)
	}

	// Verify
	fetched, err := client.GetKey(ctx, created.Key)
	if err != nil {
		t.Fatalf("GetKey failed: %v", err)
	}
	if fetched.Description != "updated via client" {
		t.Fatalf("expected description='updated via client', got %s", fetched.Description)
	}
	if fetched.DailyCostLimit != 5000 {
		t.Fatalf("expected daily_cost_limit=5000, got %d", fetched.DailyCostLimit)
	}
}

func TestClientDeleteKey(t *testing.T) {
	client, _ := setupServer(t)
	ctx := context.Background()

	// Create
	created, err := client.CreateKey(ctx, admin.CreateKeyRequest{
		Provider:  "gemini",
		ActualKey: "ai-test",
	})
	if err != nil {
		t.Fatalf("CreateKey failed: %v", err)
	}

	// Delete
	err = client.DeleteKey(ctx, created.Key)
	if err != nil {
		t.Fatalf("DeleteKey failed: %v", err)
	}

	// Verify gone
	_, err = client.GetKey(ctx, created.Key)
	if err == nil {
		t.Fatal("expected error after delete")
	}

	var apiErr *admin.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *admin.Error, got %T: %v", err, err)
	}
	if apiErr.StatusCode != 404 {
		t.Fatalf("expected 404 after delete, got %d", apiErr.StatusCode)
	}
}

func TestClientDisableReEnable(t *testing.T) {
	client, _ := setupServer(t)
	ctx := context.Background()

	created, err := client.CreateKey(ctx, admin.CreateKeyRequest{
		Provider:  "openai",
		ActualKey: "sk-test",
	})
	if err != nil {
		t.Fatalf("CreateKey failed: %v", err)
	}

	// Disable
	err = client.UpdateKey(ctx, created.Key, admin.UpdateKeyRequest{
		Enabled: admin.Bool(false),
	})
	if err != nil {
		t.Fatalf("UpdateKey (disable) failed: %v", err)
	}

	fetched, _ := client.GetKey(ctx, created.Key)
	if fetched.Enabled {
		t.Fatal("expected key to be disabled")
	}

	// Re-enable
	err = client.UpdateKey(ctx, created.Key, admin.UpdateKeyRequest{
		Enabled: admin.Bool(true),
	})
	if err != nil {
		t.Fatalf("UpdateKey (enable) failed: %v", err)
	}

	fetched, _ = client.GetKey(ctx, created.Key)
	if !fetched.Enabled {
		t.Fatal("expected key to be re-enabled")
	}
}

func TestClientWrongAPIKey(t *testing.T) {
	client, server := setupServer(t)
	_ = client // don't use the good client

	badClient := admin.NewClient(server.URL, "wrong-key")
	ctx := context.Background()

	_, err := badClient.CreateKey(ctx, admin.CreateKeyRequest{
		Provider:  "openai",
		ActualKey: "sk-test",
	})
	if err == nil {
		t.Fatal("expected error with wrong API key")
	}

	var apiErr *admin.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *admin.Error, got %T: %v", err, err)
	}
	if apiErr.StatusCode != 401 {
		t.Fatalf("expected 401, got %d", apiErr.StatusCode)
	}
}

func TestClientCreateKeyValidationErrors(t *testing.T) {
	client, _ := setupServer(t)
	ctx := context.Background()

	tests := []struct {
		name string
		req  admin.CreateKeyRequest
	}{
		{"missing provider", admin.CreateKeyRequest{ActualKey: "sk-test"}},
		{"missing actual_key", admin.CreateKeyRequest{Provider: "openai"}},
		{"invalid provider", admin.CreateKeyRequest{Provider: "cohere", ActualKey: "sk-test"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := client.CreateKey(ctx, tt.req)
			if err == nil {
				t.Fatal("expected error")
			}

			var apiErr *admin.Error
			if !errors.As(err, &apiErr) {
				t.Fatalf("expected *admin.Error, got %T: %v", err, err)
			}
			if apiErr.StatusCode != 400 {
				t.Fatalf("expected 400, got %d", apiErr.StatusCode)
			}
		})
	}
}

func TestClientUpdateKeyTags(t *testing.T) {
	client, _ := setupServer(t)
	ctx := context.Background()

	created, err := client.CreateKey(ctx, admin.CreateKeyRequest{
		Provider:  "openai",
		ActualKey: "sk-test",
		Tags:      map[string]string{"env": "dev"},
	})
	if err != nil {
		t.Fatalf("CreateKey failed: %v", err)
	}

	// Replace tags
	err = client.UpdateKey(ctx, created.Key, admin.UpdateKeyRequest{
		Tags: map[string]string{"env": "prod", "team": "platform"},
	})
	if err != nil {
		t.Fatalf("UpdateKey failed: %v", err)
	}

	fetched, err := client.GetKey(ctx, created.Key)
	if err != nil {
		t.Fatalf("GetKey failed: %v", err)
	}
	if fetched.Tags["env"] != "prod" {
		t.Fatalf("expected tag env=prod, got %v", fetched.Tags)
	}
	if fetched.Tags["team"] != "platform" {
		t.Fatalf("expected tag team=platform, got %v", fetched.Tags)
	}
}

// TestClientFullLifecycle tests the complete CRUD lifecycle through the client.
func TestClientFullLifecycle(t *testing.T) {
	client, _ := setupServer(t)
	ctx := context.Background()

	// 1. Create
	key, err := client.CreateKey(ctx, admin.CreateKeyRequest{
		Provider:       "openai",
		ActualKey:      "sk-lifecycle-test",
		Description:    "lifecycle test",
		DailyCostLimit: 100,
		Tags:           map[string]string{"test": "true"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	pk := key.Key

	// 2. Read
	key, err = client.GetKey(ctx, pk)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if key.Description != "lifecycle test" {
		t.Fatalf("expected description='lifecycle test', got %s", key.Description)
	}

	// 3. Update
	err = client.UpdateKey(ctx, pk, admin.UpdateKeyRequest{
		Description: admin.String("updated lifecycle"),
		Enabled:     admin.Bool(false),
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	// 4. Verify update
	key, err = client.GetKey(ctx, pk)
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if key.Description != "updated lifecycle" {
		t.Fatalf("expected updated description, got %s", key.Description)
	}
	if key.Enabled {
		t.Fatal("expected disabled after update")
	}

	// 5. Delete
	err = client.DeleteKey(ctx, pk)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// 6. Verify deleted
	_, err = client.GetKey(ctx, pk)
	if err == nil {
		t.Fatal("expected error after delete")
	}
	var apiErr *admin.Error
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 404 {
		t.Fatalf("expected 404 error, got %v", err)
	}
}

package cost

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/Instawork/llm-proxy/internal/providers"
	"github.com/acksell/bezos/dynamodb/ddbstore"
)

// setupTestTransport creates a DynamoDBTransport backed by an in-memory ddbstore.
func setupTestTransport(t *testing.T) *DynamoDBTransport {
	t.Helper()

	store, err := ddbstore.New(ddbstore.StoreOptions{InMemory: true})
	if err != nil {
		t.Fatalf("failed to create in-memory ddb store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	transport, err := NewDynamoDBTransport(DynamoDBTransportConfig{
		Client:    store,
		TableName: "llm-proxy-cost-tracking-test",
		Logger:    slog.Default(),
	})
	if err != nil {
		t.Fatalf("failed to create DynamoDB transport: %v", err)
	}

	return transport
}

// TestDynamoDBTransportIntegration demonstrates how to use the DynamoDB transport
func TestDynamoDBTransportIntegration(t *testing.T) {
	transport := setupTestTransport(t)

	// Create cost tracker with DynamoDB transport
	tracker := NewCostTracker(transport)

	// Set up some test pricing
	testPricing := &ModelPricing{
		Tiers: []PricingTier{
			{
				Threshold: 0,
				Input:     0.5, // $0.50 per 1M input tokens
				Output:    1.5, // $1.50 per 1M output tokens
			},
		},
	}
	tracker.SetPricingForModel("openai", "gpt-3.5-turbo", testPricing)

	// Create test metadata
	metadata := &providers.LLMResponseMetadata{
		RequestID:    "test-request-123",
		Provider:     "openai",
		Model:        "gpt-3.5-turbo",
		InputTokens:  1000,
		OutputTokens: 500,
		TotalTokens:  1500,
		IsStreaming:  false,
		FinishReason: "stop",
	}

	// Track a test request
	err := tracker.TrackRequest(metadata, "test-user", "192.168.1.1", "/v1/chat/completions")
	if err != nil {
		t.Errorf("Failed to track request: %v", err)
	}

	t.Log("Successfully tracked cost record to DynamoDB (write-only)")
}

// TestDynamoDBTransportAggregation verifies that WriteRecord creates both the
// detail record and the aggregation counter via a DynamoDB transaction, and
// that QueryUserCosts can read the aggregation back.
func TestDynamoDBTransportAggregation(t *testing.T) {
	transport := setupTestTransport(t)

	now := time.Now()
	dateStr := now.Format("2006-01-02")
	userID := "test-agg-user-" + dateStr

	// Write two records for the same user on the same day
	for i, tokens := range []int{1000, 2000} {
		record := &CostRecord{
			Timestamp:    now,
			RequestID:    fmt.Sprintf("test-agg-%s-%d", dateStr, i),
			UserID:       userID,
			IPAddress:    "10.0.0.1",
			Provider:     "openai",
			Model:        "gpt-4o",
			Endpoint:     "/v1/chat/completions",
			IsStreaming:  false,
			InputTokens:  tokens,
			OutputTokens: tokens / 2,
			TotalTokens:  tokens + tokens/2,
			InputCost:    float64(tokens) / 1_000_000.0 * 2.50,
			OutputCost:   float64(tokens/2) / 1_000_000.0 * 10.0,
			TotalCost:    float64(tokens)/1_000_000.0*2.50 + float64(tokens/2)/1_000_000.0*10.0,
			FinishReason: "stop",
		}
		if err := transport.WriteRecord(record); err != nil {
			t.Fatalf("WriteRecord #%d failed: %v", i, err)
		}
	}

	// Query the aggregation for this user
	aggregates, err := transport.QueryUserCosts(context.Background(), userID, dateStr, dateStr)
	if err != nil {
		t.Fatalf("QueryUserCosts failed: %v", err)
	}

	if len(aggregates) != 1 {
		t.Fatalf("expected 1 aggregate row, got %d", len(aggregates))
	}

	agg := aggregates[0]
	if agg.RequestCount < 2 {
		t.Errorf("expected request_count >= 2, got %d", agg.RequestCount)
	}
	if agg.InputTokens < 3000 {
		t.Errorf("expected input_tokens >= 3000, got %d", agg.InputTokens)
	}
	if agg.TotalCost <= 0 {
		t.Errorf("expected positive total_cost, got %f", agg.TotalCost)
	}

	t.Logf("Aggregation verified: requests=%d, input_tokens=%d, total_cost=%.6f",
		agg.RequestCount, agg.InputTokens, agg.TotalCost)
}

// TestDynamoDBTransportUnknownUser verifies that records with an empty UserID
// are aggregated under an empty-string user ID (PK = "USERCOST#").
func TestDynamoDBTransportUnknownUser(t *testing.T) {
	transport := setupTestTransport(t)

	now := time.Now()
	dateStr := now.Format("2006-01-02")

	record := &CostRecord{
		Timestamp:    now,
		RequestID:    "test-unknown-" + dateStr,
		UserID:       "", // empty user ID
		Provider:     "anthropic",
		Model:        "claude-sonnet-4-20250514",
		Endpoint:     "/v1/messages",
		InputTokens:  500,
		OutputTokens: 250,
		TotalTokens:  750,
		InputCost:    0.0015,
		OutputCost:   0.00375,
		TotalCost:    0.00525,
		FinishReason: "end_turn",
	}
	if err := transport.WriteRecord(record); err != nil {
		t.Fatalf("WriteRecord failed: %v", err)
	}

	// The record should be aggregated under empty-string user ID
	aggregates, err := transport.QueryUserCosts(context.Background(), "", dateStr, dateStr)
	if err != nil {
		t.Fatalf("QueryUserCosts for empty user failed: %v", err)
	}

	if len(aggregates) == 0 {
		t.Fatal("expected at least one aggregate row for empty user")
	}

	agg := aggregates[0]
	if agg.UserID != "" {
		t.Errorf("expected user_id=%q, got %q", "", agg.UserID)
	}
	if agg.RequestCount < 1 {
		t.Errorf("expected request_count >= 1, got %d", agg.RequestCount)
	}

	t.Logf("Unknown user aggregation verified: requests=%d, total_cost=%.6f",
		agg.RequestCount, agg.TotalCost)
}

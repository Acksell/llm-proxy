// Package admin provides shared types and a client SDK for the llm-proxy admin API.
package admin

import "time"

// CreateKeyRequest is the request body for POST /admin/keys.
type CreateKeyRequest struct {
	Provider       string            `json:"provider"`
	ActualKey      string            `json:"actual_key"`
	Description    string            `json:"description,omitempty"`
	DailyCostLimit int64             `json:"daily_cost_limit,omitempty"`
	Tags           map[string]string `json:"tags,omitempty"`
}

// UpdateKeyRequest is the request body for PATCH /admin/keys/{key}.
// Nil fields are omitted from the JSON and not updated.
type UpdateKeyRequest struct {
	Description    *string           `json:"description,omitempty"`
	DailyCostLimit *int64            `json:"daily_cost_limit,omitempty"`
	Enabled        *bool             `json:"enabled,omitempty"`
	ActualKey      *string           `json:"actual_key,omitempty"`
	Tags           map[string]string `json:"tags,omitempty"`
	ExpiresAt      *time.Time        `json:"expires_at,omitempty"`
}

// APIKey is the response representation of a managed API key.
// The actual provider key is never included in responses.
type APIKey struct {
	Key            string            `json:"key"`
	Provider       string            `json:"provider"`
	DailyCostLimit int64             `json:"daily_cost_limit"`
	Description    string            `json:"description,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
	ExpiresAt      *time.Time        `json:"expires_at,omitempty"`
	Enabled        bool              `json:"enabled"`
	Tags           map[string]string `json:"tags,omitempty"`
}

// ErrorResponse is the standard error response body.
type ErrorResponse struct {
	Error string `json:"error"`
}

// OKResponse is the standard success response for operations
// that don't return a resource.
type OKResponse struct {
	OK bool `json:"ok"`
}

// Pointer helpers for constructing UpdateKeyRequest fields.

func String(s string) *string     { return &s }
func Bool(b bool) *bool           { return &b }
func Int64(n int64) *int64        { return &n }
func Time(t time.Time) *time.Time { return &t }

// UsageResponse is the response body for GET /admin/usage.
type UsageResponse struct {
	UserID       string       `json:"user_id"`
	From         string       `json:"from"`
	To           string       `json:"to"`
	TotalCost    float64      `json:"total_cost"`
	InputCost    float64      `json:"input_cost"`
	OutputCost   float64      `json:"output_cost"`
	InputTokens  int          `json:"input_tokens"`
	OutputTokens int          `json:"output_tokens"`
	TotalTokens  int          `json:"total_tokens"`
	RequestCount int          `json:"request_count"`
	Daily        []DailyUsage `json:"daily"`
}

// DailyUsage represents aggregated usage for a single day.
type DailyUsage struct {
	Date         string  `json:"date"`
	TotalCost    float64 `json:"total_cost"`
	InputCost    float64 `json:"input_cost"`
	OutputCost   float64 `json:"output_cost"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	TotalTokens  int     `json:"total_tokens"`
	RequestCount int     `json:"request_count"`
}

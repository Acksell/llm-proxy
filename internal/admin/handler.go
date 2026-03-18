package admin

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	admintypes "github.com/Instawork/llm-proxy/pkg/admin"

	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/gorilla/mux"
)

// Handler holds dependencies for the admin API.
type Handler struct {
	store       *apikeys.Store
	adminAPIKey string
	logger      *slog.Logger
}

// NewHandler creates a new admin API router with all routes registered.
func NewHandler(store *apikeys.Store, adminAPIKey string, logger *slog.Logger) http.Handler {
	h := &Handler{
		store:       store,
		adminAPIKey: adminAPIKey,
		logger:      logger,
	}

	r := mux.NewRouter()

	// Health check (unauthenticated)
	r.HandleFunc("/health", h.handleHealth).Methods("GET", "HEAD")

	// All /admin routes require auth
	sub := r.PathPrefix("/admin").Subrouter()
	sub.Use(h.authMiddleware)

	sub.HandleFunc("/keys", h.handleCreateKey).Methods("POST")
	sub.HandleFunc("/keys/{key}", h.handleGetKey).Methods("GET")
	sub.HandleFunc("/keys/{key}", h.handleUpdateKey).Methods("PATCH")
	sub.HandleFunc("/keys/{key}", h.handleDeleteKey).Methods("DELETE")

	return r
}

// authMiddleware checks the Authorization bearer token against the configured admin API key.
func (h *Handler) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			writeError(w, http.StatusUnauthorized, "missing or invalid Authorization header")
			return
		}
		token := auth[len("Bearer "):]
		if token != h.adminAPIKey {
			writeError(w, http.StatusUnauthorized, "invalid admin API key")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":    "healthy",
		"timestamp": time.Now().Unix(),
	})
}

// toAPIKeyResponse converts an internal APIKey to the shared response type,
// redacting the actual provider key.
func toAPIKeyResponse(k *apikeys.APIKey) *admintypes.APIKey {
	return &admintypes.APIKey{
		Key:            k.PK,
		Provider:       k.Provider,
		DailyCostLimit: k.DailyCostLimit,
		Description:    k.Description,
		CreatedAt:      k.CreatedAt,
		UpdatedAt:      k.UpdatedAt,
		ExpiresAt:      k.ExpiresAt,
		Enabled:        k.Enabled,
		Tags:           k.Tags,
	}
}

// --- Create Key ---

func (h *Handler) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	var req admintypes.CreateKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	// Validate required fields
	if req.Provider == "" {
		writeError(w, http.StatusBadRequest, "provider is required")
		return
	}
	validProviders := map[string]bool{"openai": true, "anthropic": true, "gemini": true}
	if !validProviders[req.Provider] {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid provider: %s (must be openai, anthropic, or gemini)", req.Provider))
		return
	}
	if req.ActualKey == "" {
		writeError(w, http.StatusBadRequest, "actual_key is required")
		return
	}

	key, err := h.store.CreateKey(r.Context(), req.Provider, req.ActualKey, req.Description, req.DailyCostLimit, req.Tags)
	if err != nil {
		h.logger.Error("Failed to create API key", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create key")
		return
	}

	h.logger.Info("Admin API: created key", "key", key.PK, "provider", key.Provider)
	writeJSON(w, http.StatusCreated, toAPIKeyResponse(key))
}

// --- Get Key ---

func (h *Handler) handleGetKey(w http.ResponseWriter, r *http.Request) {
	keyID := mux.Vars(r)["key"]

	key, err := h.store.GetKey(r.Context(), keyID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, "key not found")
			return
		}
		h.logger.Error("Failed to get API key", "error", err, "key", keyID)
		writeError(w, http.StatusInternalServerError, "failed to get key")
		return
	}

	writeJSON(w, http.StatusOK, toAPIKeyResponse(key))
}

// --- Update Key ---

func (h *Handler) handleUpdateKey(w http.ResponseWriter, r *http.Request) {
	keyID := mux.Vars(r)["key"]

	var req admintypes.UpdateKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	// Build updates map from non-nil fields
	updates := make(map[string]interface{})
	if req.Description != nil {
		updates["description"] = *req.Description
	}
	if req.DailyCostLimit != nil {
		updates["daily_cost_limit"] = *req.DailyCostLimit
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	if req.ActualKey != nil {
		updates["actual_key"] = *req.ActualKey
	}
	if req.Tags != nil {
		updates["tags"] = req.Tags
	}
	if req.ExpiresAt != nil {
		updates["expires_at"] = *req.ExpiresAt
	}

	if len(updates) == 0 {
		writeError(w, http.StatusBadRequest, "no fields to update")
		return
	}

	if err := h.store.UpdateKey(r.Context(), keyID, updates); err != nil {
		if strings.Contains(err.Error(), "ConditionalCheckFailedException") || strings.Contains(err.Error(), "condition") {
			writeError(w, http.StatusNotFound, "key not found")
			return
		}
		h.logger.Error("Failed to update API key", "error", err, "key", keyID)
		writeError(w, http.StatusInternalServerError, "failed to update key")
		return
	}

	h.logger.Info("Admin API: updated key", "key", keyID, "fields", mapKeys(updates))
	writeJSON(w, http.StatusOK, admintypes.OKResponse{OK: true})
}

// --- Delete Key ---

func (h *Handler) handleDeleteKey(w http.ResponseWriter, r *http.Request) {
	keyID := mux.Vars(r)["key"]

	if err := h.store.DeleteKey(r.Context(), keyID); err != nil {
		if strings.Contains(err.Error(), "ConditionalCheckFailedException") || strings.Contains(err.Error(), "condition") {
			writeError(w, http.StatusNotFound, "key not found")
			return
		}
		h.logger.Error("Failed to delete API key", "error", err, "key", keyID)
		writeError(w, http.StatusInternalServerError, "failed to delete key")
		return
	}

	h.logger.Info("Admin API: deleted key", "key", keyID)
	writeJSON(w, http.StatusOK, admintypes.OKResponse{OK: true})
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, admintypes.ErrorResponse{Error: msg})
}

func mapKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

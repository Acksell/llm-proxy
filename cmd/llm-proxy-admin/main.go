package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Instawork/llm-proxy/internal/admin"
	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/Instawork/llm-proxy/internal/cost"
	ddb "github.com/Instawork/llm-proxy/internal/dynamodb"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	// Read required env vars
	adminAPIKey := os.Getenv("ADMIN_API_KEY")
	if adminAPIKey == "" {
		logger.Error("ADMIN_API_KEY environment variable is required")
		os.Exit(1)
	}

	port := os.Getenv("ADMIN_PORT")
	if port == "" {
		port = "9003"
	}

	// Load config (same as gateway — needs DynamoDB and API key management settings)
	yamlConfig, err := config.LoadEnvironmentConfig()
	if err != nil {
		logger.Error("Failed to load config", "error", err)
		os.Exit(1)
	}

	if !yamlConfig.Features.APIKeyManagement.Enabled {
		logger.Error("API key management is not enabled in configuration")
		os.Exit(1)
	}

	// Create AWS DynamoDB client for API key store
	apiKeysClient, err := ddb.NewClient(yamlConfig.Features.APIKeyManagement.Region)
	if err != nil {
		logger.Error("Failed to create DynamoDB client for API keys", "error", err)
		os.Exit(1)
	}

	// Create API key store
	store, err := apikeys.NewStore(apikeys.StoreConfig{
		Client:    apiKeysClient,
		TableName: yamlConfig.Features.APIKeyManagement.TableName,
		Logger:    logger,
	})
	if err != nil {
		logger.Error("Failed to create API key store", "error", err)
		os.Exit(1)
	}

	// Create CostReader from cost tracking transport config (if DynamoDB is configured)
	var costReader cost.CostReader
	for _, tc := range yamlConfig.GetAllTransports() {
		if tc.Type == "dynamodb" && tc.DynamoDB != nil {
			ddbClient, err := ddb.NewClient(tc.DynamoDB.Region)
			if err != nil {
				logger.Error("Failed to create DynamoDB client for cost tracking", "error", err)
				os.Exit(1)
			}
			transport, err := cost.NewDynamoDBTransport(cost.DynamoDBTransportConfig{
				Client:    ddbClient,
				TableName: tc.DynamoDB.TableName,
				Logger:    logger,
			})
			if err != nil {
				logger.Error("Failed to create DynamoDB cost transport for reading", "error", err)
				os.Exit(1)
			}
			costReader = transport
			logger.Info("Cost reader configured",
				"table", tc.DynamoDB.TableName,
				"region", tc.DynamoDB.Region)
			break
		}
	}

	if costReader == nil {
		logger.Warn("No DynamoDB cost transport configured; GET /admin/usage will return 501")
	}

	// Create admin handler
	handler := admin.NewHandler(store, costReader, adminAPIKey, logger)

	server := &http.Server{
		Addr:    "0.0.0.0:" + port,
		Handler: handler,
	}

	// Start server
	go func() {
		logger.Info("Starting admin API server", "address", "0.0.0.0:"+port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("Server failed", "error", err)
			os.Exit(1)
		}
	}()

	// Graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigChan
	logger.Info("Received shutdown signal", "signal", sig.String())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		logger.Error("Server shutdown failed", "error", err)
	} else {
		logger.Info("Server shut down successfully")
	}
}

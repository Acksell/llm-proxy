package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Instawork/llm-proxy/internal/admin"
	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/acksell/bezos/dynamodb/ddbiface"
	"github.com/acksell/bezos/dynamodb/ddbstore"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
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

	// Create DynamoDB client
	ddbClient, ddbCleanup, err := createDynamoDBClient(yamlConfig, logger)
	if err != nil {
		logger.Error("Failed to create DynamoDB client", "error", err)
		os.Exit(1)
	}
	defer ddbCleanup()

	// Create API key store
	store, err := apikeys.NewStore(apikeys.StoreConfig{
		Client:    ddbClient,
		TableName: yamlConfig.Features.APIKeyManagement.TableName,
		Logger:    logger,
	})
	if err != nil {
		logger.Error("Failed to create API key store", "error", err)
		os.Exit(1)
	}

	// Create admin handler
	handler := admin.NewHandler(store, adminAPIKey, logger)

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

// createDynamoDBClient creates a DynamoDB client based on configuration.
func createDynamoDBClient(yamlConfig *config.YAMLConfig, logger *slog.Logger) (ddbiface.Client, func(), error) {
	if yamlConfig.DynamoDB.IsMemoryBackend() {
		logger.Info("DynamoDB: Using in-memory backend")
		store, err := ddbstore.New(ddbstore.StoreOptions{InMemory: true})
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create in-memory DynamoDB store: %w", err)
		}
		cleanup := func() {
			if err := store.Close(); err != nil {
				logger.Error("Failed to close in-memory DynamoDB store", "error", err)
			}
		}
		return store, cleanup, nil
	}

	region := yamlConfig.DynamoDB.Region
	if region == "" {
		region = yamlConfig.Features.APIKeyManagement.Region
	}
	if region == "" {
		region = "us-west-2"
	}

	logger.Info("DynamoDB: Using AWS backend", "region", region)
	awsCfg, err := awsconfig.LoadDefaultConfig(context.TODO(),
		awsconfig.WithRegion(region),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load AWS config: %w", err)
	}
	client := dynamodb.NewFromConfig(awsCfg)
	return client, func() {}, nil
}

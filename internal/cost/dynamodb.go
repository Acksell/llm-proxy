package cost

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/acksell/bezos/dynamodb/ddbiface"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	configPkg "github.com/acksell/llm-proxy/internal/config"
	ddb "github.com/acksell/llm-proxy/internal/dynamodb"
)

// DynamoDBTransportConfig holds configuration for the DynamoDB transport
type DynamoDBTransportConfig struct {
	Client    ddbiface.Client
	TableName string
	Logger    *slog.Logger
}

// DynamoDBTransport implements Transport and CostReader interfaces for DynamoDB-based cost tracking
type DynamoDBTransport struct {
	client    ddbiface.Client
	tableName string
	logger    *slog.Logger
}

// DynamoDBCostRecord represents a cost record as stored in DynamoDB
type DynamoDBCostRecord struct {
	PK                       string  `dynamodbav:"pk"`        // Partition key: "COST#YYYY-MM-DD"
	SK                       string  `dynamodbav:"sk"`        // Sort key: "TIMESTAMP#requestId"
	GSI1PK                   string  `dynamodbav:"gsi1pk"`    // ProviderModelIndex partition key: "PROVIDER#providerName"
	GSI1SK                   string  `dynamodbav:"gsi1sk"`    // ProviderModelIndex sort key: "MODEL#modelName#TIMESTAMP"
	GSI2PK                   string  `dynamodbav:"gsi2pk"`    // UserProviderIndex partition key: "USER#userID"
	GSI2SK                   string  `dynamodbav:"gsi2sk"`    // UserProviderIndex sort key: "PROVIDER#providerName#TIMESTAMP"
	GSI3PK                   string  `dynamodbav:"gsi3pk"`    // ModelProviderIndex partition key: "MODEL#modelName"
	GSI3SK                   string  `dynamodbav:"gsi3sk"`    // ModelProviderIndex sort key: "PROVIDER#providerName#TIMESTAMP"
	TTL                      int64   `dynamodbav:"ttl"`       // TTL for automatic cleanup (optional)
	Timestamp                int64   `dynamodbav:"timestamp"` // Unix timestamp for easier queries
	RequestID                string  `dynamodbav:"request_id,omitempty"`
	UserID                   string  `dynamodbav:"user_id,omitempty"`
	IPAddress                string  `dynamodbav:"ip_address,omitempty"`
	Provider                 string  `dynamodbav:"provider"`
	Model                    string  `dynamodbav:"model"`
	Endpoint                 string  `dynamodbav:"endpoint"`
	IsStreaming              bool    `dynamodbav:"is_streaming"`
	InputTokens              int     `dynamodbav:"input_tokens"`
	OutputTokens             int     `dynamodbav:"output_tokens"`
	TotalTokens              int     `dynamodbav:"total_tokens"`
	CachedInputTokens        int     `dynamodbav:"cached_input_tokens,omitempty"`
	CacheCreationInputTokens int     `dynamodbav:"cache_creation_input_tokens,omitempty"`
	InputCost                float64 `dynamodbav:"input_cost"`
	OutputCost               float64 `dynamodbav:"output_cost"`
	CachedInputCost          float64 `dynamodbav:"cached_input_cost,omitempty"`
	CacheCreationInputCost   float64 `dynamodbav:"cache_creation_input_cost,omitempty"`
	TotalCost                float64 `dynamodbav:"total_cost"`
	FinishReason             string  `dynamodbav:"finish_reason,omitempty"`
}

// NewDynamoDBTransport creates a new DynamoDB-based transport
func NewDynamoDBTransport(cfg DynamoDBTransportConfig) (*DynamoDBTransport, error) {
	if cfg.Client == nil {
		return nil, fmt.Errorf("DynamoDB client is required")
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	transport := &DynamoDBTransport{
		client:    cfg.Client,
		tableName: cfg.TableName,
		logger:    logger,
	}

	// Ensure table exists
	if err := transport.ensureTableExists(context.TODO()); err != nil {
		return nil, fmt.Errorf("failed to ensure table exists: %w", err)
	}

	return transport, nil
}

// FromConfig creates a DynamoDBTransport from configuration
func (dt *DynamoDBTransport) FromConfig(transportConfig interface{}, logger *slog.Logger) (Transport, error) {
	switch cfg := transportConfig.(type) {
	case *configPkg.TransportConfig:
		if cfg.DynamoDB == nil {
			return nil, fmt.Errorf("dynamodb transport configuration not found")
		}

		logger.Debug("💰 DynamoDB Transport: Creating from structured config",
			"table_name", cfg.DynamoDB.TableName,
			"region", cfg.DynamoDB.Region)

		client, err := ddb.NewClient(cfg.DynamoDB.Region)
		if err != nil {
			return nil, err
		}

		config := DynamoDBTransportConfig{
			Client:    client,
			TableName: cfg.DynamoDB.TableName,
			Logger:    logger,
		}
		return NewDynamoDBTransport(config)

	case map[string]interface{}:
		dynamoConfig, ok := cfg["dynamodb"].(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("dynamodb transport configuration not found")
		}
		tableName, ok := dynamoConfig["table_name"].(string)
		if !ok {
			return nil, fmt.Errorf("dynamodb table_name not specified")
		}
		region, ok := dynamoConfig["region"].(string)
		if !ok {
			return nil, fmt.Errorf("dynamodb region not specified")
		}

		logger.Debug("💰 DynamoDB Transport: Creating from map config",
			"table_name", tableName,
			"region", region)

		client, err := ddb.NewClient(region)
		if err != nil {
			return nil, err
		}

		config := DynamoDBTransportConfig{
			Client:    client,
			TableName: tableName,
			Logger:    logger,
		}
		return NewDynamoDBTransport(config)

	default:
		return nil, fmt.Errorf("unsupported config type for dynamodb transport: %T", transportConfig)
	}
}

// NewDynamoDBTransportFromConfig creates a DynamoDBTransport from configuration (convenience function)
func NewDynamoDBTransportFromConfig(transportConfig interface{}, logger *slog.Logger) (Transport, error) {
	dt := &DynamoDBTransport{}
	return dt.FromConfig(transportConfig, logger)
}

// ensureTableExists creates the DynamoDB table if it doesn't exist
func (dt *DynamoDBTransport) ensureTableExists(ctx context.Context) error {
	// Check if table exists
	_, err := dt.client.DescribeTable(ctx, &dynamodb.DescribeTableInput{
		TableName: aws.String(dt.tableName),
	})
	if err == nil {
		dt.logger.Debug("DynamoDB table already exists", "table", dt.tableName)
		return nil
	}

	// Create table if it doesn't exist
	dt.logger.Info("Creating DynamoDB table for cost tracking", "table", dt.tableName)

	createInput := &dynamodb.CreateTableInput{
		TableName: aws.String(dt.tableName),
		KeySchema: []types.KeySchemaElement{
			{
				AttributeName: aws.String("pk"),
				KeyType:       types.KeyTypeHash,
			},
			{
				AttributeName: aws.String("sk"),
				KeyType:       types.KeyTypeRange,
			},
		},
		AttributeDefinitions: []types.AttributeDefinition{
			{
				AttributeName: aws.String("pk"),
				AttributeType: types.ScalarAttributeTypeS,
			},
			{
				AttributeName: aws.String("sk"),
				AttributeType: types.ScalarAttributeTypeS,
			},
			{
				AttributeName: aws.String("gsi1pk"),
				AttributeType: types.ScalarAttributeTypeS,
			},
			{
				AttributeName: aws.String("gsi1sk"),
				AttributeType: types.ScalarAttributeTypeS,
			},
			{
				AttributeName: aws.String("gsi2pk"),
				AttributeType: types.ScalarAttributeTypeS,
			},
			{
				AttributeName: aws.String("gsi2sk"),
				AttributeType: types.ScalarAttributeTypeS,
			},
			{
				AttributeName: aws.String("gsi3pk"),
				AttributeType: types.ScalarAttributeTypeS,
			},
			{
				AttributeName: aws.String("gsi3sk"),
				AttributeType: types.ScalarAttributeTypeS,
			},
		},
		GlobalSecondaryIndexes: []types.GlobalSecondaryIndex{
			{
				IndexName: aws.String("ProviderModelIndex"),
				KeySchema: []types.KeySchemaElement{
					{
						AttributeName: aws.String("gsi1pk"),
						KeyType:       types.KeyTypeHash,
					},
					{
						AttributeName: aws.String("gsi1sk"),
						KeyType:       types.KeyTypeRange,
					},
				},
				Projection: &types.Projection{
					ProjectionType: types.ProjectionTypeAll,
				},
			},
			{
				IndexName: aws.String("UserProviderIndex"),
				KeySchema: []types.KeySchemaElement{
					{
						AttributeName: aws.String("gsi2pk"),
						KeyType:       types.KeyTypeHash,
					},
					{
						AttributeName: aws.String("gsi2sk"),
						KeyType:       types.KeyTypeRange,
					},
				},
				Projection: &types.Projection{
					ProjectionType: types.ProjectionTypeAll,
				},
			},
			{
				IndexName: aws.String("ModelProviderIndex"),
				KeySchema: []types.KeySchemaElement{
					{
						AttributeName: aws.String("gsi3pk"),
						KeyType:       types.KeyTypeHash,
					},
					{
						AttributeName: aws.String("gsi3sk"),
						KeyType:       types.KeyTypeRange,
					},
				},
				Projection: &types.Projection{
					ProjectionType: types.ProjectionTypeAll,
				},
			},
		},
		BillingMode: types.BillingModePayPerRequest,
	}

	_, err = dt.client.CreateTable(ctx, createInput)
	if err != nil {
		return fmt.Errorf("failed to create table: %w", err)
	}

	// Wait for table to become active
	waiter := dynamodb.NewTableExistsWaiter(dt.client)
	err = waiter.Wait(ctx, &dynamodb.DescribeTableInput{
		TableName: aws.String(dt.tableName),
	}, 5*time.Minute)
	if err != nil {
		return fmt.Errorf("failed waiting for table to become active: %w", err)
	}

	dt.logger.Info("DynamoDB table created successfully", "table", dt.tableName)
	return nil
}

// WriteRecord writes a cost record and atomically updates the user's daily
// aggregation counter using a DynamoDB transaction. This guarantees that the
// detail record and the pre-aggregated counter are always consistent.
func (dt *DynamoDBTransport) WriteRecord(record *CostRecord) error {
	ctx := context.TODO()

	// Convert CostRecord to DynamoDBCostRecord
	dynamoRecord := dt.toDynamoDBRecord(record)

	// Marshal to DynamoDB item
	item, err := attributevalue.MarshalMap(dynamoRecord)
	if err != nil {
		return fmt.Errorf("failed to marshal record: %w", err)
	}

	dateStr := record.Timestamp.Format("2006-01-02")
	aggregatePK := fmt.Sprintf("USERCOST#%s", record.UserID)
	aggregateSK := fmt.Sprintf("DAY#%s", dateStr)
	ttl := record.Timestamp.AddDate(1, 0, 0).Unix()

	// Build the transaction: Put detail record + Update aggregation counter
	_, err = dt.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{
				// 1. Insert the detailed cost record (same as before)
				Put: &types.Put{
					TableName: aws.String(dt.tableName),
					Item:      item,
				},
			},
			{
				// 2. Atomically increment the user's daily aggregation counter
				Update: &types.Update{
					TableName: aws.String(dt.tableName),
					Key: map[string]types.AttributeValue{
						"pk": &types.AttributeValueMemberS{Value: aggregatePK},
						"sk": &types.AttributeValueMemberS{Value: aggregateSK},
					},
					UpdateExpression: aws.String(
						"ADD total_cost :tc, input_cost :ic, output_cost :oc, " +
							"input_tokens :it, output_tokens :ot, total_tokens :tt, " +
							"request_count :rc " +
							"SET #uid = :uid, #d = :d, #ttl = :ttl"),
					ExpressionAttributeNames: map[string]string{
						"#uid": "user_id",
						"#d":   "date",
						"#ttl": "ttl",
					},
					ExpressionAttributeValues: map[string]types.AttributeValue{
						":tc":  &types.AttributeValueMemberN{Value: fmt.Sprintf("%g", record.TotalCost)},
						":ic":  &types.AttributeValueMemberN{Value: fmt.Sprintf("%g", record.InputCost)},
						":oc":  &types.AttributeValueMemberN{Value: fmt.Sprintf("%g", record.OutputCost)},
						":it":  &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", record.InputTokens)},
						":ot":  &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", record.OutputTokens)},
						":tt":  &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", record.TotalTokens)},
						":rc":  &types.AttributeValueMemberN{Value: "1"},
						":uid": &types.AttributeValueMemberS{Value: record.UserID},
						":d":   &types.AttributeValueMemberS{Value: dateStr},
						":ttl": &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", ttl)},
					},
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to write record to DynamoDB: %w", err)
	}

	dt.logger.Debug("Cost record written to DynamoDB",
		"table", dt.tableName,
		"provider", record.Provider,
		"model", record.Model,
		"cost", record.TotalCost,
		"aggregate_user", record.UserID)

	return nil
}

// toDynamoDBRecord converts a CostRecord to DynamoDBCostRecord
func (dt *DynamoDBTransport) toDynamoDBRecord(record *CostRecord) *DynamoDBCostRecord {
	dateStr := record.Timestamp.Format("2006-01-02")
	timestampStr := record.Timestamp.Format("2006-01-02T15:04:05.000Z")

	return &DynamoDBCostRecord{
		PK:                       fmt.Sprintf("COST#%s", dateStr),
		SK:                       fmt.Sprintf("TIMESTAMP#%s#%s", timestampStr, record.RequestID),
		GSI1PK:                   fmt.Sprintf("PROVIDER#%s", record.Provider),
		GSI1SK:                   fmt.Sprintf("MODEL#%s#%s", record.Model, timestampStr),
		GSI2PK:                   fmt.Sprintf("USER#%s", record.UserID),
		GSI2SK:                   fmt.Sprintf("PROVIDER#%s#%s", record.Provider, timestampStr),
		GSI3PK:                   fmt.Sprintf("MODEL#%s", record.Model),
		GSI3SK:                   fmt.Sprintf("PROVIDER#%s#%s", record.Provider, timestampStr),
		TTL:                      record.Timestamp.AddDate(1, 0, 0).Unix(), // 1 year TTL
		Timestamp:                record.Timestamp.Unix(),
		RequestID:                record.RequestID,
		UserID:                   record.UserID,
		IPAddress:                record.IPAddress,
		Provider:                 record.Provider,
		Model:                    record.Model,
		Endpoint:                 record.Endpoint,
		IsStreaming:              record.IsStreaming,
		InputTokens:              record.InputTokens,
		OutputTokens:             record.OutputTokens,
		TotalTokens:              record.TotalTokens,
		CachedInputTokens:        record.CachedInputTokens,
		CacheCreationInputTokens: record.CacheCreationInputTokens,
		InputCost:                record.InputCost,
		OutputCost:               record.OutputCost,
		CachedInputCost:          record.CachedInputCost,
		CacheCreationInputCost:   record.CacheCreationInputCost,
		TotalCost:                record.TotalCost,
		FinishReason:             record.FinishReason,
	}
}

// QueryUserCosts retrieves pre-aggregated daily cost records for a user within
// an inclusive date range. The from/to parameters must be in "YYYY-MM-DD" format.
// This queries the aggregation items written by WriteRecord's transaction,
// so it returns results in O(days) items rather than O(requests).
func (dt *DynamoDBTransport) QueryUserCosts(ctx context.Context, userID, from, to string) ([]DailyAggregate, error) {
	pk := fmt.Sprintf("USERCOST#%s", userID)
	skFrom := fmt.Sprintf("DAY#%s", from)
	skTo := fmt.Sprintf("DAY#%s", to)

	result, err := dt.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(dt.tableName),
		KeyConditionExpression: aws.String("pk = :pk AND sk BETWEEN :from AND :to"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk":   &types.AttributeValueMemberS{Value: pk},
			":from": &types.AttributeValueMemberS{Value: skFrom},
			":to":   &types.AttributeValueMemberS{Value: skTo},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query user costs: %w", err)
	}

	aggregates := make([]DailyAggregate, 0, len(result.Items))
	for _, item := range result.Items {
		var agg DailyAggregate
		if err := attributevalue.UnmarshalMap(item, &agg); err != nil {
			dt.logger.Warn("Failed to unmarshal aggregate record", "error", err)
			continue
		}
		aggregates = append(aggregates, agg)
	}

	return aggregates, nil
}

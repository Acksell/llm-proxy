package dynamodb

import (
	"context"
	"fmt"
	"os"

	"github.com/acksell/bezos/dynamodb/ddbiface"
	"github.com/acksell/bezos/dynamodb/ddbstore"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

// NewClient creates a DynamoDB client for the given region.
//
// If the LOCAL_DYNAMODB environment variable is set to "true", an in-memory
// DynamoDB (ddbstore) is returned instead of a real AWS client. This is useful
// for local development without requiring AWS credentials or a running
// DynamoDB Local instance.
func NewClient(region string) (ddbiface.Client, error) {
	if os.Getenv("LOCAL_DYNAMODB") == "true" {
		return ddbstore.New(ddbstore.StoreOptions{Path: "data/ddb"})
	}

	cfg, err := awsconfig.LoadDefaultConfig(context.TODO(),
		awsconfig.WithRegion(region),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	return dynamodb.NewFromConfig(cfg), nil
}

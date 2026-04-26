package main

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/testcontainers/testcontainers-go/modules/localstack"
)

func setupTestDynamoDB(
	t *testing.T,
	tableName string,
) (*localstack.LocalStackContainer, *DynamoDBLockStore) {
	ctx := context.Background()

	container, err := localstack.Run(
		ctx,
		"localstack/localstack:3.0.0",
	)
	if err != nil {
		t.Fatalf("failed to start localstack: %s", err)
	}

	provider, err := container.PortEndpoint(ctx, "4566/tcp", "")
	if err != nil {
		t.Fatalf("failed to get localstack endpoint: %s", err)
	}
	endpoint := "http://" + provider

	cfg, err := config.LoadDefaultConfig(
		ctx,
		config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider("test", "test", ""),
		),
	)
	if err != nil {
		t.Fatalf("failed to load aws config: %s", err)
	}

	client := dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) {
		o.BaseEndpoint = aws.String(endpoint)
	})

	_, err = client.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String(tableName),
		KeySchema: []types.KeySchemaElement{
			{AttributeName: aws.String("Key"), KeyType: types.KeyTypeHash},
		},
		AttributeDefinitions: []types.AttributeDefinition{
			{AttributeName: aws.String("Key"), AttributeType: types.ScalarAttributeTypeS},
		},
		BillingMode: types.BillingModePayPerRequest,
	})
	if err != nil {
		t.Fatalf("failed to create table: %s", err)
	}

	return container, NewDynamoDBLockStore(client, tableName)
}

func TestDynamoDBLockStore_Acquire_Release_Extend(t *testing.T) {
	container, store := setupTestDynamoDB(t, "DistLocks1")
	defer func() { _ = container.Terminate(context.Background()) }()

	req1 := LockRequest{
		Key:    "test:lock:1",
		Token:  "user-A",
		TTLSec: 10,
	}

	lock, err := store.Acquire(req1)
	if err != nil {
		t.Fatalf("expected no error acquiring lock, got: %v", err)
	}
	if lock.Key != req1.Key || lock.Token != req1.Token {
		t.Errorf("lock mismatch. got: %+v, want: %+v", lock, req1)
	}

	req2 := LockRequest{
		Key:    "test:lock:1",
		Token:  "user-B",
		TTLSec: 10,
	}
	_, err = store.Acquire(req2)
	if err != ErrLockAlreadyExists {
		t.Fatalf("expected ErrLockAlreadyExists, got: %v", err)
	}

	err = store.Extend(req2)
	if err != ErrTokenMismatch {
		t.Fatalf("expected ErrTokenMismatch, got: %v", err)
	}

	err = store.Extend(req1)
	if err != nil {
		t.Fatalf("expected no error extending lock, got: %v", err)
	}

	err = store.Release(req2)
	if err != ErrTokenMismatch {
		t.Fatalf("expected ErrTokenMismatch, got: %v", err)
	}

	err = store.Release(req1)
	if err != nil {
		t.Fatalf("expected no error releasing lock, got: %v", err)
	}

	_, err = store.Acquire(req2)
	if err != nil {
		t.Fatalf("expected no error acquiring lock after release, got: %v", err)
	}
}

func TestDynamoDBLockStore_TTL_Expiration(t *testing.T) {
	container, store := setupTestDynamoDB(t, "DistLocks2")
	defer func() { _ = container.Terminate(context.Background()) }()

	req := LockRequest{
		Key:    "test:lock:ttl",
		Token:  "user-A",
		TTLSec: 1,
	}

	_, err := store.Acquire(req)
	if err != nil {
		t.Fatalf("failed to acquire lock: %v", err)
	}

	time.Sleep(1500 * time.Millisecond)

	req2 := LockRequest{
		Key:    "test:lock:ttl",
		Token:  "user-B",
		TTLSec: 10,
	}
	_, err = store.Acquire(req2)
	if err != nil {
		t.Fatalf("expected no error acquiring expired lock, got: %v", err)
	}
}

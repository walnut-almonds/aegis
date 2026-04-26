package main

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// DynamoDBLockStore 實作基於 DynamoDB 的分散式鎖
type DynamoDBLockStore struct {
	client    *dynamodb.Client
	tableName string
}

func NewDynamoDBLockStore(client *dynamodb.Client, tableName string) *DynamoDBLockStore {
	return &DynamoDBLockStore{
		client:    client,
		tableName: tableName,
	}
}

func (s *DynamoDBLockStore) Acquire(req LockRequest) (*Lock, error) {
	ctx := context.Background()
	now := time.Now()
	expiredAt := now.Add(time.Duration(req.TTLSec) * time.Second)
	expiredAtUnix := expiredAt.Unix()
	nowUnix := now.Unix()

	// 嘗試寫入鎖。條件：不存在，或者已存在但過期時間小於現在 (即已經過期)
	_, err := s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(s.tableName),
		Item: map[string]types.AttributeValue{
			"Key":       &types.AttributeValueMemberS{Value: req.Key},
			"Token":     &types.AttributeValueMemberS{Value: req.Token},
			"ExpiredAt": &types.AttributeValueMemberN{Value: strconv.FormatInt(expiredAtUnix, 10)},
		},
		ConditionExpression: aws.String("attribute_not_exists(#K) OR #E < :now"),
		ExpressionAttributeNames: map[string]string{
			"#K": "Key",
			"#E": "ExpiredAt",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":now": &types.AttributeValueMemberN{Value: strconv.FormatInt(nowUnix, 10)},
		},
	})
	if err != nil {
		var condFailedErr *types.ConditionalCheckFailedException
		if errors.As(err, &condFailedErr) {
			return nil, ErrLockAlreadyExists
		}
		return nil, err
	}

	return &Lock{
		Key:       req.Key,
		Token:     req.Token,
		ExpiredAt: expiredAt,
	}, nil
}

func (s *DynamoDBLockStore) Release(req LockRequest) error {
	ctx := context.Background()

	// 刪除條件：Token 相等
	_, err := s.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"Key": &types.AttributeValueMemberS{Value: req.Key},
		},
		ConditionExpression: aws.String("#T = :tok"),
		ExpressionAttributeNames: map[string]string{
			"#T": "Token",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":tok": &types.AttributeValueMemberS{Value: req.Token},
		},
	})
	if err != nil {
		var condFailedErr *types.ConditionalCheckFailedException
		if errors.As(err, &condFailedErr) {
			return ErrTokenMismatch
		}
		return err
	}

	return nil
}

func (s *DynamoDBLockStore) Extend(req LockRequest) error {
	ctx := context.Background()
	expiredAt := time.Now().Add(time.Duration(req.TTLSec) * time.Second).Unix()

	// 更新條件：Token 相等
	_, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"Key": &types.AttributeValueMemberS{Value: req.Key},
		},
		UpdateExpression:    aws.String("SET #E = :exp"),
		ConditionExpression: aws.String("#T = :tok"),
		ExpressionAttributeNames: map[string]string{
			"#E": "ExpiredAt",
			"#T": "Token",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":exp": &types.AttributeValueMemberN{Value: strconv.FormatInt(expiredAt, 10)},
			":tok": &types.AttributeValueMemberS{Value: req.Token},
		},
	})
	if err != nil {
		var condFailedErr *types.ConditionalCheckFailedException
		if errors.As(err, &condFailedErr) {
			return ErrTokenMismatch
		}
		return err
	}

	return nil
}

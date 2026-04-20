package main

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisLockStore 實作基於 Redis 的分散式鎖
type RedisLockStore struct {
	client *redis.Client
}

func NewRedisLockStore(client *redis.Client) *RedisLockStore {
	return &RedisLockStore{
		client: client,
	}
}

func (s *RedisLockStore) Acquire(req LockRequest) (*Lock, error) {
	ctx := context.Background()
	ttl := time.Duration(req.TTLSec) * time.Second

	// Redis 的 SET key value NX PX ttl 達成了原子性「如果不存在才寫入並設定過期時間」
	ok, err := s.client.SetNX(ctx, req.Key, req.Token, ttl).Result()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrLockAlreadyExists
	}

	return &Lock{
		Key:       req.Key,
		Token:     req.Token,
		ExpiredAt: time.Now().Add(ttl),
	}, nil
}

func (s *RedisLockStore) Release(req LockRequest) error {
	ctx := context.Background()

	// 使用 Lua script 確保 Token 吻合才准許刪除 (Atomic Delete)
	script := `
		if redis.call("get", KEYS[1]) == ARGV[1] then
			return redis.call("del", KEYS[1])
		else
			return 0
		end
	`
	res, err := s.client.Eval(ctx, script, []string{req.Key}, req.Token).Result()
	if err != nil {
		return err
	}

	if res.(int64) == 0 {
		return ErrTokenMismatch // 或鎖早已過期
	}
	return nil
}

func (s *RedisLockStore) Extend(req LockRequest) error {
	ctx := context.Background()
	ttlMs := int64(req.TTLSec * 1000) // PEXPIRE 接受毫秒

	// 使用 Lua script 確保 Token 吻合才准許延長期限 (Atomic Extend)
	script := `
		if redis.call("get", KEYS[1]) == ARGV[1] then
			return redis.call("pexpire", KEYS[1], ARGV[2])
		else
			return 0
		end
	`
	res, err := s.client.Eval(ctx, script, []string{req.Key}, req.Token, ttlMs).Result()
	if err != nil {
		return err
	}

	if res.(int64) == 0 {
		return ErrTokenMismatch // 或是鎖已經過期消失了
	}
	return nil
}

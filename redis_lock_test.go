package main

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func setupTestRedis() (*miniredis.Miniredis, *RedisLockStore) {
	mr, err := miniredis.Run()
	if err != nil {
		panic(err)
	}

	rdb := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})

	return mr, NewRedisLockStore(rdb)
}

func TestRedisLockStore_Acquire_Release_Extend(t *testing.T) {
	mr, store := setupTestRedis()
	defer mr.Close()

	req1 := LockRequest{
		Key:    "test:lock:1",
		Token:  "user-A",
		TTLSec: 10,
	}

	// 1. 成功取得鎖
	lock, err := store.Acquire(req1)
	if err != nil {
		t.Fatalf("expected no error acquiring lock, got: %v", err)
	}
	if lock.Key != req1.Key || lock.Token != req1.Token {
		t.Errorf("lock mismatch. got: %+v, want: %+v", lock, req1)
	}

	// 2. 被其他 Token 嘗試取得鎖 (應失敗)
	req2 := LockRequest{
		Key:    "test:lock:1",
		Token:  "user-B",
		TTLSec: 10,
	}
	_, err = store.Acquire(req2)
	if err != ErrLockAlreadyExists {
		t.Fatalf("expected ErrLockAlreadyExists, got: %v", err)
	}

	// 3. User-B 嘗試延長鎖 (應失敗)
	err = store.Extend(req2)
	if err != ErrTokenMismatch {
		t.Fatalf("expected ErrTokenMismatch, got: %v", err)
	}

	// 4. User-A 延長自身擁有的鎖 (應成功)
	err = store.Extend(req1)
	if err != nil {
		t.Fatalf("expected no error extending lock, got: %v", err)
	}

	// 5. User-B 嘗試解鎖 (應失敗，不該刪除別人的鎖)
	err = store.Release(req2)
	if err != ErrTokenMismatch {
		t.Fatalf("expected ErrTokenMismatch, got: %v", err)
	}

	// 6. User-A 解鎖自身擁有的鎖 (應成功)
	err = store.Release(req1)
	if err != nil {
		t.Fatalf("expected no error releasing lock, got: %v", err)
	}

	// 7. 解鎖後，User-B 此時可以重新取得鎖
	_, err = store.Acquire(req2)
	if err != nil {
		t.Fatalf("expected no error acquiring lock after release, got: %v", err)
	}
}

func TestRedisLockStore_TTL_Expiration(t *testing.T) {
	mr, store := setupTestRedis()
	defer mr.Close()

	req := LockRequest{
		Key:    "test:lock:ttl",
		Token:  "user-A",
		TTLSec: 1, // 只設定 1 秒的 TTL
	}

	// 取鎖
	_, err := store.Acquire(req)
	if err != nil {
		t.Fatalf("failed to acquire lock: %v", err)
	}

	// 推進時間 (讓 Redis 以為時間過了一點多秒)
	mr.FastForward(1500 * time.Millisecond)

	// User-B 可以取得該鎖 (代表上面那個已經自己過期刪除了)
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

package main

import (
	"database/sql"
	"fmt"
	"net"
	"testing"
	"time"

	sqle "github.com/dolthub/go-mysql-server"
	"github.com/dolthub/go-mysql-server/memory"
	"github.com/dolthub/go-mysql-server/server"
	gsql "github.com/dolthub/go-mysql-server/sql"
	embeddedpostgres "github.com/fergusstrange/embedded-postgres"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
)

// freePort 動態找一個可用的 TCP 埠
func freePort() int {
	l, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		panic(fmt.Sprintf("freePort: %v", err))
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// ─── PostgreSQL helpers (embedded-postgres，不需要 Docker) ───────────────────────

func setupPostgres(t *testing.T) *SQLLockStore {
	t.Helper()

	port := uint32(freePort())
	pg := embeddedpostgres.NewDatabase(
		embeddedpostgres.DefaultConfig().
			Username("testuser").
			Password("testpass").
			Database("testdb").
			Port(port),
	)
	if err := pg.Start(); err != nil {
		t.Fatalf("failed to start embedded postgres: %v", err)
	}
	t.Cleanup(func() {
		if err := pg.Stop(); err != nil {
			t.Logf("warning: embedded postgres stop: %v", err)
		}
	})

	dsn := fmt.Sprintf(
		"host=localhost port=%d user=testuser password=testpass dbname=testdb sslmode=disable",
		port,
	)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("failed to open embedded postgres db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := NewSQLLockStore(db, DialectPostgres)
	if err != nil {
		t.Fatalf("failed to create postgres lock store: %v", err)
	}
	return store
}

// ─── MySQL helpers (go-mysql-server in-memory，不需要 Docker) ──────────────────

func setupMySQL(t *testing.T) *SQLLockStore {
	t.Helper()

	// 建立 in-memory 資料庫與引擎
	dbName := "testdb"
	memDB := memory.NewDatabase(dbName)
	pro := memory.NewDBProvider(memDB)
	engine := sqle.NewDefault(pro)

	port := freePort()
	cfg := server.Config{
		Protocol: "tcp",
		Address:  fmt.Sprintf("localhost:%d", port),
	}
	s, err := server.NewServer(cfg, engine, gsql.NewContext, memory.NewSessionBuilder(pro), nil)
	if err != nil {
		t.Fatalf("failed to create go-mysql-server: %v", err)
	}
	go func() { _ = s.Start() }()
	t.Cleanup(func() { s.Close() })

	// 等待 server 啟動（最多 2 秒）
	dsn := fmt.Sprintf("root:@tcp(localhost:%d)/%s?parseTime=true", port, dbName)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("failed to open in-memory mysql db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	db.SetConnMaxLifetime(0)
	for i := 0; i < 20; i++ {
		if err = db.Ping(); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("in-memory mysql server did not become ready: %v", err)
	}

	store, err := NewSQLLockStore(db, DialectMySQL)
	if err != nil {
		t.Fatalf("failed to create mysql lock store: %v", err)
	}
	return store
}

// ─── Shared test logic ─────────────────────────────────────────────────────────

func runLockStoreTests(t *testing.T, store *SQLLockStore) {
	t.Run("Acquire_Release_Extend", func(t *testing.T) {
		req1 := LockRequest{Key: "sql:lock:1", Token: "user-A", TTLSec: 10}

		lock, err := store.Acquire(req1)
		if err != nil {
			t.Fatalf("expected no error acquiring lock, got: %v", err)
		}
		if lock.Key != req1.Key || lock.Token != req1.Token {
			t.Errorf("lock mismatch. got: %+v", lock)
		}

		// 同一把鎖，另一個使用者搶不到
		req2 := LockRequest{Key: "sql:lock:1", Token: "user-B", TTLSec: 10}
		_, err = store.Acquire(req2)
		if err != ErrLockAlreadyExists {
			t.Fatalf("expected ErrLockAlreadyExists, got: %v", err)
		}

		// 錯誤 token 無法 Extend
		err = store.Extend(req2)
		if err != ErrTokenMismatch {
			t.Fatalf("expected ErrTokenMismatch on extend, got: %v", err)
		}

		// 正確 token 可以 Extend
		err = store.Extend(req1)
		if err != nil {
			t.Fatalf("expected no error extending lock, got: %v", err)
		}

		// 錯誤 token 無法 Release
		err = store.Release(req2)
		if err != ErrTokenMismatch {
			t.Fatalf("expected ErrTokenMismatch on release, got: %v", err)
		}

		// 正確 token 可以 Release
		err = store.Release(req1)
		if err != nil {
			t.Fatalf("expected no error releasing lock, got: %v", err)
		}

		// 釋放後可以被其他人取得
		_, err = store.Acquire(req2)
		if err != nil {
			t.Fatalf("expected no error acquiring lock after release, got: %v", err)
		}

		// 清理
		_ = store.Release(req2)
	})

	t.Run("TTL_Expiration", func(t *testing.T) {
		req := LockRequest{Key: "sql:lock:ttl", Token: "user-A", TTLSec: 1}

		_, err := store.Acquire(req)
		if err != nil {
			t.Fatalf("failed to acquire lock: %v", err)
		}

		// 等待 TTL 過期（睡 2s 確保跨越 Unix 秒邊界）
		time.Sleep(2 * time.Second)

		// 過期後可被搶佔
		req2 := LockRequest{Key: "sql:lock:ttl", Token: "user-B", TTLSec: 10}
		_, err = store.Acquire(req2)
		if err != nil {
			t.Fatalf("expected no error acquiring expired lock, got: %v", err)
		}

		// 清理
		_ = store.Release(req2)
	})

	t.Run("Extend_Expired_Lock", func(t *testing.T) {
		req := LockRequest{Key: "sql:lock:extend-exp", Token: "user-A", TTLSec: 1}

		_, err := store.Acquire(req)
		if err != nil {
			t.Fatalf("failed to acquire lock: %v", err)
		}

		time.Sleep(2 * time.Second)

		// 過期後 Extend 應失敗
		err = store.Extend(req)
		if err != ErrTokenMismatch {
			t.Fatalf("expected ErrTokenMismatch extending expired lock, got: %v", err)
		}
	})

	t.Run("Release_NonExistent_Lock", func(t *testing.T) {
		req := LockRequest{Key: "sql:lock:nonexistent", Token: "ghost", TTLSec: 10}
		err := store.Release(req)
		if err != ErrLockNotFound {
			t.Fatalf("expected ErrLockNotFound releasing non-existent lock, got: %v", err)
		}
	})
}

// ─── PostgreSQL Tests ──────────────────────────────────────────────────────────

func TestSQLLockStore_Postgres(t *testing.T) {
	store := setupPostgres(t)
	runLockStoreTests(t, store)
}

// ─── MySQL Tests ───────────────────────────────────────────────────────────────

func TestSQLLockStore_MySQL(t *testing.T) {
	store := setupMySQL(t)
	runLockStoreTests(t, store)
}

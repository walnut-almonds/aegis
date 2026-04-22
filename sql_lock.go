package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	pq "github.com/lib/pq"
)

// SQLDialect 代表 SQL 方言類型
type SQLDialect string

const (
	DialectPostgres SQLDialect = "postgres"
	DialectMySQL    SQLDialect = "mysql"
)

// SQLLockStore 實作基於 PostgreSQL / MySQL 的分散式鎖
//
// 資料表結構 (PostgreSQL):
//
//	CREATE TABLE IF NOT EXISTS locks (
//	    key        VARCHAR(512) PRIMARY KEY,
//	    token      VARCHAR(512) NOT NULL,
//	    expired_at BIGINT       NOT NULL,
//	    version    BIGINT       NOT NULL DEFAULT 0
//	);
//
// 資料表結構 (MySQL):
//
//	CREATE TABLE IF NOT EXISTS locks (
//	    `key`        VARCHAR(512) NOT NULL PRIMARY KEY,
//	    token        VARCHAR(512) NOT NULL,
//	    expired_at   BIGINT       NOT NULL,
//	    version      BIGINT       NOT NULL DEFAULT 0
//	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
type SQLLockStore struct {
	db      *sql.DB
	dialect SQLDialect
}

// NewSQLLockStore 建立 SQLLockStore，並自動建立所需的 locks 資料表。
func NewSQLLockStore(db *sql.DB, dialect SQLDialect) (*SQLLockStore, error) {
	store := &SQLLockStore{db: db, dialect: dialect}
	if err := store.createTable(); err != nil {
		return nil, fmt.Errorf("sql_lock: create table: %w", err)
	}
	return store, nil
}

// createTable 確保 locks 資料表存在。
func (s *SQLLockStore) createTable() error {
	var ddl string
	switch s.dialect {
	case DialectPostgres:
		ddl = `
CREATE TABLE IF NOT EXISTS locks (
    key        VARCHAR(512) PRIMARY KEY,
    token      VARCHAR(512) NOT NULL,
	expired_at BIGINT       NOT NULL,
	version    BIGINT       NOT NULL DEFAULT 0
);`
	case DialectMySQL:
		ddl = `
CREATE TABLE IF NOT EXISTS locks (
    ` + "`key`" + `        VARCHAR(512) NOT NULL,
    token        VARCHAR(512) NOT NULL,
    expired_at   BIGINT       NOT NULL,
	version      BIGINT       NOT NULL DEFAULT 0,
    PRIMARY KEY (` + "`key`" + `)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;`
	default:
		return fmt.Errorf("unsupported dialect: %s", s.dialect)
	}
	_, err := s.db.ExecContext(context.Background(), ddl)
	return err
}

// isDuplicateKeyError 判斷錯誤是否為主鍵衝突（INSERT 失敗）。
func (s *SQLLockStore) isDuplicateKeyError(err error) bool {
	if err == nil {
		return false
	}
	switch s.dialect {
	case DialectPostgres:
		var pqErr *pq.Error
		if errors.As(err, &pqErr) {
			return pqErr.Code == "23505" // unique_violation
		}
	case DialectMySQL:
		var myErr *mysql.MySQLError
		if errors.As(err, &myErr) {
			return myErr.Number == 1062 // ER_DUP_ENTRY
		}
		// go-sql-driver/mysql 回傳 "Duplicate entry"
		// go-mysql-server (dolthub) 回傳 "duplicate primary key"
		msg := strings.ToLower(err.Error())
		return strings.Contains(msg, "duplicate entry") ||
			strings.Contains(msg, "duplicate primary key") ||
			strings.Contains(msg, "duplicate key") ||
			strings.Contains(msg, "unique constraint")
	}
	return false
}

func (s *SQLLockStore) validateLockRequest(req LockRequest, requireTTL bool) error {
	if strings.TrimSpace(req.Key) == "" {
		return fmt.Errorf("sql_lock: empty key")
	}
	if strings.TrimSpace(req.Token) == "" {
		return fmt.Errorf("sql_lock: empty token")
	}
	if requireTTL && req.TTLSec <= 0 {
		return fmt.Errorf("sql_lock: ttl_sec must be > 0")
	}
	return nil
}

// dbNowUnix 取得 DB 目前時間（Unix 秒），降低多節點時鐘偏差影響。
func (s *SQLLockStore) dbNowUnix(ctx context.Context) (int64, error) {
	var q string
	if s.dialect == DialectPostgres {
		q = `SELECT CAST(EXTRACT(EPOCH FROM NOW()) AS BIGINT)`
	} else {
		q = `SELECT UNIX_TIMESTAMP()`
	}

	var nowUnix int64
	if err := s.db.QueryRowContext(ctx, q).Scan(&nowUnix); err != nil {
		return 0, fmt.Errorf("sql_lock: db now: %w", err)
	}
	return nowUnix, nil
}

// Acquire 嘗試取得分散式鎖。
//
// 樂觀鎖策略（無 SELECT FOR UPDATE，降低競爭）：
//  1. 直接 INSERT；成功 → 取得鎖。
//  2. 若主鍵衝突（鎖已存在） → 嘗試 UPDATE WHERE key=? AND expired_at<=now。
//  3. UPDATE 影響 1 列 → 搶佔過期鎖成功。
//  4. UPDATE 影響 0 列 → 鎖仍有效 → 回傳 ErrLockAlreadyExists。
func (s *SQLLockStore) Acquire(req LockRequest) (*Lock, error) {
	if err := s.validateLockRequest(req, true); err != nil {
		return nil, err
	}

	ctx := context.Background()
	nowUnix, err := s.dbNowUnix(ctx)
	if err != nil {
		return nil, fmt.Errorf("sql_lock acquire: %w", err)
	}
	expiredAtUnix := nowUnix + int64(req.TTLSec)
	expiredAt := time.Unix(expiredAtUnix, 0)

	// Step 1：樂觀 INSERT
	var insertQ string
	if s.dialect == DialectPostgres {
		insertQ = `INSERT INTO locks (key, token, expired_at, version) VALUES ($1, $2, $3, 1)`
	} else {
		insertQ = "INSERT INTO locks (`key`, token, expired_at, version) VALUES (?, ?, ?, 1)"
	}

	_, err = s.db.ExecContext(ctx, insertQ, req.Key, req.Token, expiredAtUnix)
	if err == nil {
		// INSERT 成功，直接回傳
		return &Lock{
			Key:       req.Key,
			Token:     req.Token,
			ExpiredAt: expiredAt,
		}, nil
	}
	if !s.isDuplicateKeyError(err) {
		return nil, fmt.Errorf("sql_lock acquire: insert: %w", err)
	}

	// Step 2：主鍵衝突 → 嘗試搶佔過期鎖（條件式 UPDATE，無需 SELECT FOR UPDATE）
	var updateQ string
	if s.dialect == DialectPostgres {
		updateQ = `UPDATE locks SET token = $1, expired_at = $2, version = version + 1 WHERE key = $3 AND expired_at <= $4`
	} else {
		updateQ = "UPDATE locks SET token = ?, expired_at = ?, version = version + 1 WHERE `key` = ? AND expired_at <= ?"
	}

	result, err := s.db.ExecContext(ctx, updateQ, req.Token, expiredAtUnix, req.Key, nowUnix)
	if err != nil {
		return nil, fmt.Errorf("sql_lock acquire: update expired: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("sql_lock acquire: rows affected: %w", err)
	}
	if rows == 0 {
		// 鎖仍有效，無法取得
		return nil, ErrLockAlreadyExists
	}

	return &Lock{
		Key:       req.Key,
		Token:     req.Token,
		ExpiredAt: expiredAt,
	}, nil
}

// Release 釋放分散式鎖（需 Token 完全吻合）。
func (s *SQLLockStore) Release(req LockRequest) error {
	if err := s.validateLockRequest(req, false); err != nil {
		return err
	}

	ctx := context.Background()

	var q string
	if s.dialect == DialectPostgres {
		q = `DELETE FROM locks WHERE key = $1 AND token = $2`
	} else {
		q = "DELETE FROM locks WHERE `key` = ? AND `token` = ?"
	}

	result, err := s.db.ExecContext(ctx, q, req.Key, req.Token)
	if err != nil {
		return fmt.Errorf("sql_lock release: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("sql_lock release: rows affected: %w", err)
	}
	if rows == 0 {
		// 分辨「鎖不存在」與「token 不符」
		var existsQ string
		if s.dialect == DialectPostgres {
			existsQ = `SELECT 1 FROM locks WHERE key = $1`
		} else {
			existsQ = "SELECT 1 FROM locks WHERE `key` = ?"
		}

		var one int
		err = s.db.QueryRowContext(ctx, existsQ, req.Key).Scan(&one)
		switch {
		case err == sql.ErrNoRows:
			return ErrLockNotFound
		case err != nil:
			return fmt.Errorf("sql_lock release: check exists: %w", err)
		default:
			return ErrTokenMismatch
		}
	}
	return nil
}

// Extend 延長分散式鎖的 TTL（需 Token 吻合且鎖尚未過期）。
//
// 樂觀鎖策略（無 SELECT FOR UPDATE、無 transaction）：
//  1. 普通 SELECT 讀取目前狀態，在 Go 層驗證 token 與過期時間。
//  2. 條件式 UPDATE WHERE key=? AND token=?（token 本身即版本戳）。
//  3. 0 列受影響 → token 不符或鎖已被他人搶佔 → ErrTokenMismatch。
func (s *SQLLockStore) Extend(req LockRequest) error {
	if err := s.validateLockRequest(req, true); err != nil {
		return err
	}

	ctx := context.Background()
	nowUnix, err := s.dbNowUnix(ctx)
	if err != nil {
		return fmt.Errorf("sql_lock extend: %w", err)
	}
	newExpiredAt := nowUnix + int64(req.TTLSec)

	// Step 1：條件式 UPDATE（同時檢查 token 與未過期，避免復活已過期鎖）
	var updateQ string
	if s.dialect == DialectPostgres {
		updateQ = `UPDATE locks SET expired_at = $1, version = version + 1 WHERE key = $2 AND token = $3 AND expired_at > $4`
	} else {
		updateQ = "UPDATE locks SET expired_at = ?, version = version + 1 WHERE `key` = ? AND token = ? AND expired_at > ?"
	}

	result, err := s.db.ExecContext(ctx, updateQ, newExpiredAt, req.Key, req.Token, nowUnix)
	if err != nil {
		return fmt.Errorf("sql_lock extend: update: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("sql_lock extend: rows affected: %w", err)
	}
	if rows > 0 {
		return nil
	}

	// Step 2：細分錯誤原因（不存在 / token 不符 / 已過期）
	var selectQ string
	if s.dialect == DialectPostgres {
		selectQ = `SELECT token, expired_at FROM locks WHERE key = $1`
	} else {
		selectQ = "SELECT token, expired_at FROM locks WHERE `key` = ?"
	}

	var existingToken string
	var existingExpiredAt int64
	err = s.db.QueryRowContext(ctx, selectQ, req.Key).Scan(&existingToken, &existingExpiredAt)
	switch {
	case err == sql.ErrNoRows:
		return ErrLockNotFound
	case err != nil:
		return fmt.Errorf("sql_lock extend: select: %w", err)
	}

	if existingToken != req.Token {
		return ErrTokenMismatch
	}
	if existingExpiredAt <= nowUnix {
		return ErrTokenMismatch
	}

	// 理論上不應到這裡，保守回傳 token mismatch（可能為競態）
	return ErrTokenMismatch
}

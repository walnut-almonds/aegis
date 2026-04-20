# Lock Service APIs

## CreateLock
    input
        key(string)
        token(string)
        ttl_sec(int)
    output
        lock(Lock)
        error(Error) // e.g. ErrAlreadyExists

## DeleteLock (UnLock)
    input
        key(string)
        token(string)
    output
        error(Error) // e.g. ErrLockNotFound, ErrTokenMismatch

## ExtendLock
    input
        key(string)
        token(string)
        ttl_sec(int)
    output
        error(Error) // e.g. ErrLockExpired, ErrTokenMismatch

# Optimistic Lock Module (Atomic Operations)
> ⚠️ **重要觀念**: 所有的 Lock 動作必須交給 Database 層級做「原子操作 (Atomic)」，避免程式庫層級的 Race Condition (如：先 Get 再 Set)。

- **Redis**: 
  - Create: `SET key token NX PX ttl`
  - Extend/Delete: Lua Script (檢查 Token 一致才執行 TTL 更新或 DEL)
- **AWS DynamoDB**: ConditionExpression (`attribute_not_exists(key) OR expired_at < :now`)
- **RDBMS**: `UPDATE ... WHERE key = ? AND token = ?`

```json
// Lock Resource
{
    "key": "book:{book_id}", // Unique
    "token": "UserService:{new_uuid}", // 用於解鎖與延長鎖的防呆驗證
    "expired_at": "{RFC3339}", // 依賴 DB 或 Redis TTL 較佳，避免伺服器時鐘飄移 (Clock Drift)
    "owner_metadata": "IP: 192.168.1.5, Host: Worker-1" // (選用) 幫助 Debug 誰握有這把鎖
}
```

```go
// 以下 Pseudocode 重點在於：
// 1. 將條件判斷與寫入交給驅動層 (Driver/DB) 做到 Atomic
// 2. 嚴格把關 Error 回傳，過期或被搶走絕對不能回傳 nil

func Lock(input Input) (Lock, error) {
    // 呼叫底層 Atomic 操作: Set 如果不存在或已過期
    lock, err := db.AtomicSetIfNotExistsOrExpired(input.Key, input.Token, input.TTL)
    if err != nil {
        return nil, errors.New("ErrAlreadyExists: lock is acquired by others")    
    }
    
    return lock, nil
}
```

```go
func UnLock(input Input) error {
    // 呼叫底層 Atomic 操作: 刪除，但前提是 Token 必須吻合
    err := db.AtomicDeleteIfTokenMatches(input.Key, input.Token)
    if err != nil {
        // 鎖可能已經過期自己消失了，或是被別的服務暴力覆蓋
        return errors.New("ErrUnlockFailed: lock not found or unexpected token")
    }
    
    return nil
}
```

```go
func ExtendLock(input Input) error {
    // 呼叫底層 Atomic 操作: 延長過期時間，前提是鎖還在且 Token 必須吻合
    err := db.AtomicExtendIfTokenMatches(input.Key, input.Token, input.TTL)
    if err != nil {
        // ⚠️ 這裡絕對不能回傳 nil 或是無聲無息。
        // 如果延長失敗，Client 必須立刻知道並中斷後續的業務邏輯，否則會破壞資料一致性
        return errors.New("ErrExtendFailed: lock already expired or unexpected token")
    }
    
    return nil
}
```
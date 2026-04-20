# Lock Service APIs

## CreateLock
    input
        key(string)
        token(string)
        ttl_sec(int)
    output

## DeleteLock
    input
        key(string)
        token(string)
    output

## ExtendLock
    input
        key(string)
        token(string)
        ttl_sec(int)
    output

# Optimistic Lock Module
- Redis: Lua Script + SetNX (Set if not exists)
- AWS DynamoDB: Conditional Write
- RDBMS: Update Where

```
// Lock Resource
{
    "key": "book:{book_id}" // Unique
    "token": "UserService:{new_uuid}"
    "expired_at": "{RFC3339}"
    ...
}
```

```
func Lock(input Input) (Lock, error) {
    lock := get(input.Key)
    if lock == nil or lock.IsExpired() {
        lock = newLock(input)
        set(key, lock)
        
        return lock, nil
    }
    
    return nil, errors.New("already exists")    
}
```

```
func UnLock(input Input) (error) {
    lock := get(input.Key)
    if lock == nil or lock.IsExpired() {
        return nil
    }
    
    if lock.Token != input.Token {
        return errors.New("unexpected token")
    }
    
    set(input.Key, nil)
    
    return nil
}
```

```
func ExtendLock(input Input) (error) {
    lock := get(input.Key)
    if lock == nil or lock.IsExpired() {
        return nil
    }
    
    if lock.Token != input.Token {
        return errors.New("unexpected token")
    }
    
    lock.Extend(input)
    
    set(input.Key, lock)
    
    return nil
}
```
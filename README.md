# Aegis 分散式鎖服務 (Distributed Lock Service)

Aegis 是一個基於 Go 語言開發的輕量級分散式鎖服務 (Distributed Lock Service)，提供簡單易用的 RESTful API 來進行鎖的獲取、釋放與展期。系統設計支援可插拔的儲存後端，目前已實作 Redis 與 DynamoDB 兩種儲存引擎。

## 🌟 主要功能

- **標準 HTTP API**：提供單一且標準的 RESTful API 溝通介面。
- **多儲存後端支援**：
  - **Redis**：適用於高併發、低延遲的快取鎖場景。
  - **DynamoDB**：適用於需要高可用性與持久化儲存的雲原生環境。
- **鎖生命週期管理**：支援獲取鎖 (Acquire)、釋放鎖 (Release) 以及鎖展期 (Extend/Renew)。

## 🚀 快速開始

### 啟動依賴服務
專案內附 `docker-compose.yml`，可快速啟動本地的 Redis 或 LocalStack (模擬 DynamoDB) 環境：
```bash
docker-compose up -d
```

### 執行服務
```bash
go run main.go
```
*預設伺服器會依照設定連接至對應的 backend 並在本地特定的 port 啟動監聽。*

## 📚 API 文件

所有的 API 請求皆採用 JSON 格式，核心的資料模型如下：
```json
{
  "key": "resource:123",  // 鎖的唯一鍵值 (Resource ID)
  "token": "uuid-xxxx",   // 用戶端/擁有者的唯一識別碼
  "ttl_sec": 30           // 鎖定存活時間 (秒)
}
```

### 1. 獲取鎖 (Acquire)
- **Method**: `POST` `/acquire` *(依照實際路由配置)*
- **說明**: 嘗試獲取指定 `key` 的鎖。若鎖已被其他 `token` 持有，將回傳 409 Conflict。
- **Success Response**: `201 Created`

### 2. 釋放鎖 (Release)
- **Method**: `DELETE` 或 `POST` `/release`
- **說明**: 使用獲取鎖時的 `token` 來釋放鎖。若 `token` 不符或鎖已過期，則無法刪除（避免誤刪別人的鎖）。
- **Success Response**: `200 OK`

### 3. 鎖展期 (Extend)
- **Method**: `PATCH` 或 `POST` `/extend`
- **說明**: 當現有操作尚未完成，但鎖即將過期時，可以帶上正確的 `token` 來延長 `ttl_sec`。
- **Success Response**: `200 OK`

## 🛠️ 開發與測試

本專案測試整合了 [testcontainers-go](https://golang.testcontainers.org/) 與 miniredis：
```bash
go test -v ./...
```

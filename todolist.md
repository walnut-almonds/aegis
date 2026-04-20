# Aegis - Todo List

## 🔥 核心功能

- [ ] **SDK**
  - [x] Go SDK (`sdk/go/`)
  - [ ] Python SDK (`sdk/python/`) — 測試腳本已存在，client 尚未完成
  - [ ] Node.js / TypeScript SDK
- [ ] **gRPC Buf Tool** — 導入 `buf` 管理 `.proto`，取代手動 `protoc`
- [ ] **Support PostgreSQL / MySQL** — RDBMS backend (`UPDATE WHERE key = ? AND token = ?`)
- [ ] **Swagger / OpenAPI** — 為 HTTP API 產生文件 (`/api/v1/lock` 等)

## 📊 可觀測性 (Observability)

- [ ] **OpenTracing / OpenTelemetry** — 分散式追蹤，trace 每次 Lock/Unlock/Extend
- [ ] **Metrics (Prometheus)** — 暴露 `/metrics`，記錄鎖競爭次數、延遲、過期率
- [ ] **Structured Logging** — 統一 log 格式 (JSON)，方便接 ELK / Loki
- [ ] **Dashboard** — 可視化目前持有的鎖（持有者、TTL、key 等），考慮用 Grafana 或輕量 Web UI

## 🔒 功能強化

- [ ] **Lock Heartbeat / Auto-Renew** — SDK 自動延長 TTL，避免 client 忘記 ExtendLock
- [ ] **WaitLock API** — 支援帶 timeout 的等待鎖（long-polling 或 gRPC streaming）
- [ ] **Lock Metadata / owner_metadata** — 記錄 IP、hostname，方便除錯死鎖持有者
- [ ] **Rate Limiting** — 防止單一 client 惡意搶鎖
- [ ] **Audit Log** — 紀錄每次 lock/unlock 的操作歷史

## 🧪 測試 & 品質

- [ ] **Integration Test** — 使用 `docker-compose` 跑完整 Redis + DynamoDB local 測試
- [ ] **Benchmark Test** — 高並發下的吞吐量與延遲測試
- [ ] **Chaos Test** — 模擬 backend 掛掉時 lock service 的行為

## 🚀 CI/CD & 部署

- [ ] **CI/CD** — GitHub Actions：build、test、lint、docker push
- [ ] **Docker Image** — 多架構 image (`linux/amd64`, `linux/arm64`)
- [ ] **Health Check Endpoint** — `/healthz` / `/readyz`

## ☁️ 雲端部署 (Secondary)

- [ ] **Helm Chart** — 打包為 Helm chart，方便一鍵部署
- [ ] **Kubernetes** — Deployment / Service / HPA manifest
- [ ] **Multi-Region / HA** — 考慮 Redis Cluster 或 DynamoDB Global Table 高可用方案

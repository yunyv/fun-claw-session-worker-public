# fun-claw-session-worker

Go 实现的 Session Hub Worker，通过 WebSocket 连接 Hub 并将任务转发给 OpenClaw Gateway 执行。

## 功能特性

- 通过 WebSocket 连接 Session Hub (`connect.challenge` → `connect` → `hello-ok`)
- 支持心跳机制，每 15 秒发送 `worker.heartbeat`
- 自动接收并处理 Session Hub 派发的任务
- 将任务转发至 OpenClaw Gateway 并返回结果

## 支持的操作

| Action | 说明 |
|--------|------|
| `responses.create` | 调用 Gateway 创建响应 |
| `session.history.get` | 获取会话历史 |
| `node.invoke` | 调用节点，支持 Artifacts 注册 |

## 快速开始

### 构建

```bash
go build -o bin/go-worker ./cmd/go-worker
```

### 运行

```bash
./bin/go-worker \
  --hub-url ws://127.0.0.1:31880/ws \
  --hub-token "your-hub-token" \
  --worker-id "my-worker" \
  --gateway-url http://127.0.0.1:18789
```

### 参数说明

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `--hub-url` | `ws://127.0.0.1:31880/ws` | Session Hub WebSocket 地址 |
| `--hub-token` | 空 | Hub 认证 Token |
| `--worker-id` | **必填** | Worker 唯一标识 |
| `--gateway-url` | `http://127.0.0.1:18789` | OpenClaw Gateway HTTP 地址 |
| `--gateway-token` | 空 | Gateway 认证 Token |
| `--gateway-ws-url` | `ws://127.0.0.1:18789` | OpenClaw Gateway WebSocket 地址 |
| `--capabilities` | `responses.create,session.history.get,node.invoke` | 支持的能力列表 |
| `--version` | `1.0.0` | Worker 版本号 |

## 架构

```
Session Hub (Hub)  ←→  go-worker (Worker)  ←→  OpenClaw Gateway
     ↑                      ↑                       ↑
  WebSocket            处理任务               HTTP/WS 调用
  派发任务             转发结果               执行实际操作
```

### 协议流程

1. `connect.challenge` (Hub → Worker)
2. `connect` (Worker → Hub)
3. `hello-ok` (Hub → Worker)
4. `worker.heartbeat` (Worker → Hub, 每15s)
5. `task.assigned` (Hub → Worker)
6. `task.accepted` (Worker → Hub)
7. Gateway HTTP/WS 调用 (Worker → Gateway)
8. `task.completed` / `task.failed` (Worker → Hub)

## 目录结构

```
├── cmd/go-worker/          # 程序入口
├── internal/
│   ├── constants/          # 常量定义
│   ├── protocol/           # 协议定义
│   ├── hubclient/          # Hub 客户端 (WebSocket)
│   ├── gatewayclient/      # Gateway 客户端 (HTTP/WS)
│   └── integration/         # 集成测试
└── docs/                   # 文档
```

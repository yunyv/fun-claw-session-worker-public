# Go Worker 集成测试指南

## 环境准备

### 1. 启动 Session Hub (TypeScript 版本)

由于这是一个纯 TypeScript/Node.js 项目，需要先检查环境：

```bash
cd C:/work/fun-claw-session-hub/src

# 检查是否有 Node.js 和必要的依赖
node --version

# 注意：当前项目似乎没有 package.json，可能需要额外的设置
```

### 2. 启动 OpenClaw Gateway

确保 Gateway 在 `http://127.0.0.1:18789` 运行。

### 3. 启动 Go Worker

```bash
cd C:/work/fun-claw-session-hub/go-worker
./bin/go-worker \
  --hub-url ws://127.0.0.1:31880/ws \
  --hub-token "hub-secret" \
  --worker-id "test-go-worker" \
  --gateway-url http://127.0.0.1:18789
```

## 手动验证步骤

### 验证 1: Worker 在线检测

启动 Worker 后，验证 Hub 能看到它：

```bash
curl -H "Authorization: Bearer hub-secret" \
     http://127.0.0.1:31880/api/v1/workers
```

应该能看到 `test-go-worker` 在列表中。

### 验证 2: 完整任务流程

创建 session 并发送消息：

```bash
# 1. 创建 session
curl -X POST \
  -H "Authorization: Bearer hub-secret" \
  -H "Content-Type: application/json" \
  -d '{"session_id": "test-session", "adapter_id": "test-adapter"}' \
  http://127.0.0.1:31880/api/v1/sessions

# 2. 发送消息触发任务
curl -X POST \
  -H "Authorization: Bearer hub-secret" \
  -H "Content-Type: application/json" \
  -d '{"action": "responses.create", "input": {"model": "test", "input": "hello"}}' \
  http://127.0.0.1:31880/api/v1/sessions/test-session/messages

# 3. 等待任务完成并查询结果
# request_id 从上一步响应中获取
curl -X POST \
  -H "Authorization: Bearer hub-secret" \
  -H "Content-Type: application/json" \
  -d '{"timeout_ms": 5000}' \
  http://127.0.0.1:31880/api/v1/requests/{request_id}/await
```

### 验证 3: 心跳和掉线检测

```bash
# 停止 Worker，观察 Hub 是否在 ~15 秒内检测到掉线
```

## 协议对照表

Go Worker 实现遵循与 TypeScript Worker 完全相同的协议：

| 步骤 | 协议消息 | 方向 |
|------|----------|------|
| 1 | `connect.challenge` (event) | Hub → Worker |
| 2 | `connect` (req) | Worker → Hub |
| 3 | `hello-ok` (res) | Hub → Worker |
| 4 | `worker.heartbeat` (req) | Worker → Hub (每15s) |
| 5 | `task.assigned` (event) | Hub → Worker |
| 6 | `task.accepted` (req) | Worker → Hub |
| 7 | Gateway HTTP/WS 调用 | Worker → Gateway |
| 8 | `task.completed/failed` (req) | Worker → Hub |

## 调试技巧

1. **查看 WebSocket 流量**：使用 `wscat` 或浏览器开发者工具监听 `/ws`

2. **Hub 日志**：如果 Hub 有日志输出，可以看到任务路由情况

3. **常见问题**：
   - `worker-id is required`：必须指定 `--worker-id`
   - 连接失败：检查 Hub 是否运行在正确端口
   - 鉴权失败：检查 `--hub-token` 是否与 Hub 配置一致

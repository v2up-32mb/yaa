# Yaa! Remote API 设计

> 本文件是 `/api/v1` 路由、REST envelope 与权限元数据的唯一总表。各模块文件只补充 DTO 和行为。

---

## 1. v1 边界

- HTTP REST 用于查询和有界 mutation；SSE/WS 用于流式事件。
- Agent、Provider、Tool、Skill 和 MCP Server 定义来自当前 Config。v1 不提供另一套动态配置 CRUD 或 Token 签发存储。
- 所有 Remote Tool 名称都是 [canonical Tool name](../tool/provider.md)：`ToolInfo`、Agent/Skill Tool 列表、MCP detail、Session history、`tool_call` 和 `tool_result.name` 都不暴露 Provider-safe alias，REST/SSE/WS 也没有 alias 字段。
- Session 和 Memory 有明确的 Manager/持久化契约，因此保留 mutation API。
- 不承诺通用 `Idempotency-Key`。具体操作是否幂等以端点文档为准。
- REST 使用统一 envelope；SSE/WS frame 不套 REST envelope。

## 2. 文档与端点数

| 文件 | 模块 | 端点数 |
|------|------|--------|
| [system.md](system.md) | 系统 | 3 |
| [agent.md](agent.md) | Agent 配置视图与运行态 | 5 |
| [session.md](session.md) | Session | 10 |
| [conversation.md](conversation.md) | 对话 | 3 |
| [tool.md](tool.md) | Tool 元数据 | 2 |
| [skill.md](skill.md) | Skill 元数据 | 2 |
| [provider.md](provider.md) | Provider 元数据 | 3 |
| [memory.md](memory.md) | Memory | 7 |
| [mcp.md](mcp.md) | MCP 连接状态 | 2 |
| [auth.md](auth.md) | Bearer/RBAC 协议 | 0 |
| **合计** | **10 模块** | **39** |

## 3. 路由总表

### 3.1 系统

| 方法 | 路径 | 权限 | 说明 |
|------|------|------|------|
| GET | `/api/v1/health` | public by default; otherwise `read:system` | 健康与 readiness |
| GET | `/api/v1/version` | public by default; otherwise `read:system` | 构建版本 |
| GET | `/api/v1/config` | `read:config` | 当前脱敏 Config |
| PUT | `/api/v1/config` | `write:config` | 在线改配置（全量文档；热字段立即生效，结构变更需重启） |

### 3.2 Agent

| 方法 | 路径 | 权限 | 说明 |
|------|------|------|------|
| GET | `/api/v1/agents` | `read:agents` | 列出配置中的 Agent |
| GET | `/api/v1/agents/:id` | `read:agents` | 获取 Agent 配置视图与状态 |
| POST | `/api/v1/agents/:id/start` | `write:agents` | 启动 |
| POST | `/api/v1/agents/:id/pause` | `write:agents` | 暂停 |
| POST | `/api/v1/agents/:id/stop` | `write:agents` | 停止 |

### 3.3 Session

| 方法 | 路径 | 权限 | 说明 |
|------|------|------|------|
| POST | `/api/v1/agents/:id/sessions` | `write:sessions` | 创建 Session |
| GET | `/api/v1/agents/:id/sessions` | `read:sessions` | 列出 Agent 的 Session |
| GET | `/api/v1/sessions/:id` | `read:sessions` | 获取 Session |
| POST | `/api/v1/sessions/:id/pause` | `write:sessions` | 暂停 |
| POST | `/api/v1/sessions/:id/resume` | `write:sessions` | 恢复 |
| POST | `/api/v1/sessions/:id/close` | `write:sessions` | 关闭并保留历史 |
| DELETE | `/api/v1/sessions/:id` | `delete:sessions` | 物理删除 |
| POST | `/api/v1/sessions/:id/clear` | `write:sessions` | 清空消息 |
| POST | `/api/v1/sessions/:id/model` | `write:sessions` | 设置/清除会话级模型覆盖 |
| GET | `/api/v1/sessions/:id/messages` | `read:sessions` | 查询消息 |
| DELETE | `/api/v1/sessions/:id/messages/:msgid` | `delete:sessions` | 原子删除消息或 Tool unit |

### 3.4 对话

| 方法 | 路径 | 权限 | 说明 |
|------|------|------|------|
| POST | `/api/v1/sessions/:id/messages` | `write:sessions` | 非流式 Agent turn |
| GET | `/api/v1/sessions/:id/events` | `read:sessions` | SSE 事件订阅 |
| GET | `/api/v1/sessions/:id/stream` | `write:sessions` | WebSocket upgrade；双向流式 Agent turn |

### 3.5 Tool、Skill 与 Provider

| 方法 | 路径 | 权限 | 说明 |
|------|------|------|------|
| GET | `/api/v1/tools` | `read:tools` | 列出已注册 Tool |
| GET | `/api/v1/tools/:name` | `read:tools` | 获取 Tool Schema |
| GET | `/api/v1/skills` | `read:skills` | 列出已加载 Skill |
| GET | `/api/v1/skills/:name` | `read:skills` | 获取 Skill 定义 |
| GET | `/api/v1/providers` | `read:providers` | 列出已注册 Provider |
| GET | `/api/v1/providers/:id` | `read:providers` | 获取脱敏 Provider 配置 |
| GET | `/api/v1/providers/:id/models` | `read:providers` | 获取 canonical `ModelInfo` |

Tool 只能在经过 Agent 工具白名单检查的 turn 中执行；Skill 只能通过 Agent 的 Prompt/Tool loop 生效。v1 不暴露绕过 Agent principal 的直接执行端点。

### 3.6 Memory

| 方法 | 路径 | 权限 | 说明 |
|------|------|------|------|
| GET | `/api/v1/agents/:id/memory` | `read:memory` | 有界查询 |
| GET | `/api/v1/agents/:id/memory/:key` | `read:memory` | 读取完整 scope 的 item |
| POST | `/api/v1/agents/:id/memory` | `write:memory` | Put |
| DELETE | `/api/v1/agents/:id/memory/:key` | `delete:memory` | 删除 item |
| DELETE | `/api/v1/agents/:id/memory` | `delete:memory` | 清空 scope |
| POST | `/api/v1/agents/:id/memory/promote` | `write:memory` | 提升为 Agent 全局 item |
| POST | `/api/v1/agents/:id/memory/reindex` | `write:memory` | 重建派生向量索引 |

### 3.7 MCP

| 方法 | 路径 | 权限 | 说明 |
|------|------|------|------|
| GET | `/api/v1/mcp/servers` | `read:mcp` | 列出配置的上游连接及状态 |
| GET | `/api/v1/mcp/servers/:name` | `read:mcp` | 获取连接详情与 Tool 名称 |

MCP Tool 已映射到 Tool Manager，由 Agent turn 调用。增删配置或重连通过配置 reload/restart 完成，不在 Remote API 中复制配置所有权。

## 4. REST envelope

成功：

```json
{
  "code": 0,
  "message": "ok",
  "data": {},
  "request_id": "req_01J..."
}
```

错误：

```json
{
  "code": 40401,
  "message": "resource not found",
  "data": null,
  "request_id": "req_01J..."
}
```

`code=0` 表示成功，与 HTTP 成功状态独立；创建可以返回 HTTP 201。错误 code 使用 HTTP 状态加两位子码。`message` 不包含底层路径、凭据或上游响应正文。Remote API Server 只使用下面这一份错误 writer；Auth 包和业务 handler 不再定义第二套 envelope：

```go
type Envelope struct {
    Code      int    `json:"code"`
    Message   string `json:"message"`
    Data      any    `json:"data"`
    RequestID string `json:"request_id"`
}

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, status, code int, message string) {
    requestID := requestIDFromContext(r.Context()) // request-ID middleware 保证非空
    w.Header().Set("Content-Type", "application/json; charset=utf-8")
    w.Header().Set("X-Request-ID", requestID)
    w.WriteHeader(status)
    if err := json.NewEncoder(w).Encode(Envelope{
        Code: code, Message: message, Data: nil, RequestID: requestID,
    }); err != nil {
        s.logger.Warn("api error response write failed", "request_id", requestID, "error", err)
    }
}
```

只有 Agent、Session 和 Session Message 列表使用 `page`/`page_size`：

```json
{
  "items": [],
  "total": 0,
  "page": 1,
  "page_size": 20
}
```

默认 `page=1`、`page_size=20`，最大值由对应端点声明。Tool、Skill、Provider、Model 和 MCP 列表规模受 Config 限制，一次返回 `items`；Memory Search 使用 `limit<=100`，不承诺 total 或 cursor。

## 5. 认证与路由权限

非 public 请求使用：

```text
Authorization: Bearer <static-token-or-jwt>
```

静态 Token 来自 `runtime.auth.tokens`；JWT 由外部系统签发，Runtime 只验证。v1 没有登录、签发、刷新或撤销端点。完整协议见 [auth.md](auth.md)。

Router 注册每个端点时必须同时绑定上表的 `(action, resource)`；Remote API 的唯一 route wrapper 使用已匹配的 metadata 完成 AuthN/AuthZ。不得从 URL 第一段或 HTTP 方法猜测权限，否则嵌套的 Memory/Session 路由会被错误归类。

> 路由总表中的 `read:agents` / `write:sessions` 等记号仅为可读展示，表示 `action:resource` 组合；`RouteSpec` 注册时拆分为 `Action="read"` 与 `Resource="agents"` 两个字段，wrapper 调用 `Authorize(identity, spec.Action, spec.Resource)` 时传两个独立字符串而非 `"read:agents"`。

Health 与 version 的 `RouteSpec` 始终分别绑定 `Action="read", Resource="system"`；默认 `public_paths` 命中时 wrapper 才 bypass。管理员从 public paths 删除它们后，同一 metadata 立即用于授权。

## 6. 超时与连接

- 普通控制类 REST handler 使用 `runtime.api.http.write_timeout`。
- HTTP Server 本身不设置会截断全部连接的全局 `WriteTimeout`；middleware 用 `http.NewResponseController` 为普通 REST 设置响应 deadline。
- 对话 POST、SSE 和 WS 清除该响应 deadline，改用 request context、Provider/Tool timeout 与 Session turn 取消。
- SSE 每 15 秒发送 comment heartbeat；WS 使用 ping/pong。连接断开不回滚已经提交的 Session snapshot。

## 7. 错误码

| code | HTTP | 说明 |
|------|------|------|
| `40001` | 400 | 参数或字段无效 |
| `40002` | 400 | JSON/协议 frame 解析失败 |
| `40101` | 401 | 缺少或无效凭据 |
| `40102` | 401 | JWT 签名、算法、issuer、audience、subject、roles、exp 或 nbf 任一校验失败 |
| `40301` | 403 | RBAC 拒绝 |
| `40401` | 404 | 资源不存在 |
| `40901` | 409 | 当前状态不允许操作 |
| `42201` | 422 | 可解析但违反领域不变量 |
| `42901` | 429 | 容量或速率上限 |
| `50001` | 500 | 未分类内部错误 |
| `50201` | 502 | MCP 上游连接、鉴权或协议失败 |
| `50202` | 502 | Provider 上游失败 |
| `50301` | 503 | Runtime 未 Ready 或关键存储不可用 |
| `50401` | 504 | 请求 deadline exceeded |

Provider 的上游 401/403/429 不直接透传；统一映射为 `50202`/HTTP 502，并将可重试分类只写入服务端日志。客户端取消后通常不再写响应。

---

*最后更新: 2026-07-22*

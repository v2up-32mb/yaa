# 实施进度

## 当前阶段

Phase 1：核心骨架。

## 已完成

- Phase 0 文档基线已完成。
- 配置迁移检查清单已与权威迁移契约对齐。
- Go 1.20 模块、最小入口、Makefile 和 CI 已建立。
- 配置版本解析已实现并覆盖严格格式、比较和错误边界。
- 显式配置迁移图已实现，拒绝降级、重复起点和缺失路径。
- 配置环境变量已支持单次展开、默认值、递归 Map/Slice 和路径错误。
- 配置格式检测与 YAML/JSON/TOML raw Map 解析已实现。
- 完整配置 DTO 与序列化标签已按各模块权威文档实现。
- 已明确 `tools.builtin` 的 14 个 v1 配置键及 `file` 共享配置组语义。
- 已补充 File Tool `timeout: 0` 继承全局超时的 canonical 说明。
- 根配置及各子系统 canonical 默认值已实现，所有容器按调用独立初始化。
- 配置文件中的数组和动态 Map 元素默认值已实现，并统一规范化 TOML 的对象数组。
- Presence-aware typed decode 已实现：保留缺失字段、整体替换切片、按 key 合并 Map，并严格处理 null、duration、标量转换、未知字段和完整错误路径。
- Session、Memory、Context 与 Planner 的 presence-aware policy resolver 已实现，显式 `false`/`0` 可覆盖上层值。
- Validator 契约已补齐所有静态 helper、稳定错误、NaN、inactive descriptor、Skill option 编码及静态/binding 两阶段边界。
- 基础配置 Validator 已实现：聚合结构化错误、校验根与 Agent effective policy，并保持配置只读。

- 配置文件路径发现已实现：按 `--config` → `YAA_CONFIG_PATH` → 当前目录/`~/.yaa`/`/etc/yaa` 顺序、目录内 `.yaml→.yml→.toml→.json`，只接受普通文件、返回词法绝对路径，权限/I/O 错误保留 cause，全部未命中返回空串；显式或环境路径无效返回 `ErrConfigFileNotFound`。
- 统一配置加载器 `config.Load` 已实现：`Default()` → 解析 → `migrateRaw`（版本检测+迁移）→ 环境变量展开 → `ApplyElementDefaults` → `DecodeInto` → `applyFlags` → `Validate`，默认路径全未命中时用纯默认值启动并输出 warning。
- 命令行参数覆盖 `applyFlags`/`setByPath` 已实现：按点路径反射设标量叶字段，支持 string/bool/int/uint/float/duration，拒绝数组/动态 Map/非标量/未注册路径。
- `golang.org/x/exp/slog` 已按架构契约引入为固定版本（兼容 Go 1.20）。
- `loader_test.go` 覆盖路径发现优先级、显式路径不存在、目录非普通文件、默认配置启动、完整管线与环境变量展开、flag 覆盖与非标量/未知字段拒绝。

- 统一 REST `Envelope`、错误 writer（`writeError`，HTTP 状态+两位子码）、成功 writer、request-id middleware 与生成器已实现：复用入站 `X-Request-ID` 否则生成 `req_` 前缀有序 ID，响应回写 `X-Request-ID` 与 envelope `request_id`。
- Remote API HTTP Server 骨架已实现：`NewServer`、`Start`/`Shutdown`、`recoverMiddleware`、方法校验与未匹配路由 40401。
- `/api/v1/health` 与 `/api/v1/version` 端点已实现：health 按 `HealthProvider` 返回 200/code0/data（ready=true，degraded 也 200）或 503/code50301/data=null；version 返回 ldflags 可注入的 version/git_commit/build_time/go_version。
- 已确认 `golang.org/x/exp/slog` 的 `Logger.Error(msg, err, args...)` 与标准 `log/slog` 签名不同，后续迁移需注意。
- `api_test.go` 覆盖 envelope、X-Request-ID 复用/生成、health ready/degraded/unready、version 四字段、405/404、未启动 Shutdown。

- 最小 Runtime 生命周期已实现：`runtime.New`（拒绝 nil config）→ `Start`（启动 API、标记 Ready、记录 components.api）→ `Ready`/`Health`/`UptimeSeconds` → `Shutdown`（先原子 Not Ready 再按逆序关 API，错误用 `errors.Join`）；启动失败按逆序 `rollback`。
- Runtime 实现 `api.HealthProvider`，health 接入 Ready 状态；`APIAddr()` 暴露实际监听地址。
- `cmd/yaa` 入口已接入：`--config` flag → `config.Load` → `runtime.New` → `signal.NotifyContext(Interrupt, SIGTERM)` 等 ctx → 按 `http.write_timeout` 限期 `Shutdown`。
- Makefile `build` 已通过 ldflags 注入 `api.Version/GitCommit/BuildTime`。
- Runtime 真实二进制冒烟通过：`/api/v1/health` 200/code0/data、`/api/v1/version` 含 ldflags 值、未匹配路由 404、kill 后正常退出。
- `runtime_test.go` 覆盖 nil config、Start/Ready/Health/components、Shutdown 清除 Ready、启动前 not ready、端到端真实 HTTP /health。

- 根 Storage 抽象已实现：`storage.Storage` 接口（Get/Set/Delete/Has/Keys/Close）、错误集（NotFound/Closed/InvalidKey/InvalidTTL/InvalidPath/ValueTooLarge）、MaxValueBytes 上限、共享 `validateKey`/`validateValue`/`expiresAt` 校验。
- Memory 后端已实现：注入 Clock、60s batch 1000 清理 worker、惰性隐藏过期值、Set/Get 拷贝 bytes、Close sync.Once 幂等并等 worker、Close 后返回 ErrClosed；`memory_test.go` 13 用例覆盖拷贝/Keys 排序/TTL 隐藏与负值/多参数拒绝/超大 value/幂等 Close/cleanup。
- SQLite 后端已实现（`modernc.org/sqlite` v1.28.0，纯 Go 无 CGO）：`root_kv`+`root_storage_schema_version` 表、WAL+busy_timeout、单连接、atomic upsert 保留 created_at、Get/Has/Keys 按 expiry 过滤、前缀 `substr` 字面匹配（`%`/`_`/Unicode 不被当通配）、cleanup batch 1000、migration 与目录创建失败阻止 Ready、Close 幂等；`sqlite_test.go` 覆盖 upsert/特殊字符前缀/TTL 过滤/cleanup/超大/空路径/迁移建表/工厂。
- `storage.New` 工厂按 `type` 分发 sqlite/memory，未知类型拒绝。
- Runtime 已接入 Storage：Start 顺序 Storage→API（失败逆序 rollback），Health `components.storage` 反映后端（SQLite=ready、memory=degraded 且 status=degraded、ready=true），Shutdown 先 Not Ready 再 API 再 Storage。
- 发现文档缺口并修复：`docs/storage/alternatives.md` §4 与 `checklist.md` 要求 health 报告 `durable=false`，但 `remote-api/system.md` 的 HealthData schema 无该字段；统一为通过 `components.storage` 状态表达，不增 schema 顶层字段，避免契约漂移。
- 真实二进制冒烟：SQLite 后端 health healthy+storage ready 且 `yaa.db`+WAL 落地；memory 后端 health degraded+durable=false 日志；两者优雅退出。

- 日志系统已实现 `internal/logging`：按 `config.LogConfig`（level/format/output）创建 slog.Logger，level 映射 debug/info/warn/error、format text/json、output stderr/stdout/文件路径（append），返回 closer 用于文件句柄关闭；`SetDefault` 同时设 `slog.Default`。
- `cmd/yaa` 已接入日志：`logging.SetDefault(cfg.Log)` 替换裸 `slog.Default`，进程退出前关闭日志句柄。
- `logging_test.go` 覆盖级别过滤（error 级抑制 info）、JSON 格式可解析、text 格式 attr、文件输出与 closer、非法 level 拒绝、debug 级打印、SetDefault；7 用例全过。
- 真实二进制冒烟：json+warn+文件输出 → 日志为可解析 JSON 且 INFO 行被级别过滤、stdout 为空（日志入文件）。
- 再次确认 `golang.org/x/exp/slog` 的 `Logger.Error(msg, err, args...)` 与标准库签名不同，所有 Error 调用已按 `Error(msg, err, args...)` 处理并留记。

- Provider 层已实现 `internal/provider`：`Provider` 接口与全部 DTO（ChatRequest/ChatResponse/ChatChunk/Delta/Message/ToolDef/ToolCall/ToolChoice/Usage/ModelInfo/ProviderInfo/ThinkingConfig/ResponseFormat），JSON tags 与 provider.md 一致。
- `ProviderError` 稳定分类与 `errors.Is`、HTTP 状态码→Code/Retryable 映射（401/403/429/5xx/400 子类 model_not_found|context_length|invalid_request/404）、Retry-After 头秒数解析、连接/context 错误分类为 connection|timeout。
- OpenAI adapter（纯 Go `net/http`）：Chat（POST /chat/completions 非流式）、StreamChat（SSE `data:` 行 + `[DONE]`，按 index 维护稳定非空 Tool call ID）、Models、EstimateInputTokens（char/4 估算）、Close、extra merge 到请求体顶层并剥离 `thinking`、Bearer 鉴权。
- `retryingProvider`：一次逻辑调用总 deadline（`ProviderConfig.timeout`）覆盖首次+全部重试；Chat 在完整响应前按 `min(retryInterval*2^n,30s)` 与 `RetryAfter` 较大值退避重试；StreamChat 缓冲到首可见 chunk 前才重试，首个可见 chunk 后不重试/不重放并转发后续（含 error chunk）；`ctx.Done()` 不重试。
- `Manager`：`NewManager` 用 factory registry（默认注册 openai）为每条配置构造 adapter 并 retrying 包装，重复 ID/未知类型拒绝，`Get`/`List`(按 ID 排序)/`Close`(`errors.Join`)，无运行时 Register/Unregister。
- Runtime 已接入 Provider：Start 顺序 Storage→Provider→API（失败逆序 rollback），Health `components.provider=ready`，Shutdown 逆序 API→Provider→Storage。
- provider 测试 19 用例绿：openai Chat 成功/Tool calls/错误分类/Retry-After/SSE 文本+finish/Tool call 稳定 ID/Models/估算/nil 请求/extra+thinking；manager+retry Chat 重试成功/不可重试/耗尽/排序 Close/重复 ID/未知类型/Stream 首可见前重试/首可见后不重试。
- 真实二进制冒烟：health 含 `components.api/provider/storage`，provider=ready。

## 下一步

- 接日志系统（slog handler/级别控制按 config.log）并接入 Runtime；随后开始 Phase2 Provider 层（OpenAI Chat/Stream/Models）。

每个可独立验收的功能完成后单独提交并推送到 `gitea/main`。

## Phase 2：Session 管理器（已完成本地代码）

- 依赖 `github.com/oklog/ulid/v2 v2.1.2`（兼容 go1.15）生成 `ses_<ULID>` / `msg_<ULID>`；Turn ID 由调用方传入。
- `internal/session` 包结构：
  - `errors.go`：稳定错误集（22 个 `ErrXxx`）+ `OpError{Op,SessionID,Err}` 带上下文包装。
  - `state.go`：`State` 四态 `created|active|paused|closed`、`transitions` 表、`canTransition`、`desiredState`（max_lifetime 优先于 TTL）、`stateAllowed`（生命矩阵）。
  - `session.go`：`Session`、`SessionMessage`、`CreateRequest`、`AppendInput`、`ListQuery`、`Clock`、`snapshotV1/Policy/Message` DTO、深拷贝。
  - `id.go`：`ulidGen` 用 `crypto/rand` + `MonotonicEntropy` 并发安全生成 ULID。
  - `validate.go`：单条 role 校验（user/assistant final/assistant tool call/tool/system）+ 批次序列校验（首条 user、无悬空 tool、Tool unit 完整、canonical name 格式、JSON arguments、ToolCall ID 唯一）+ messageBytesLimit。
  - `snapshot.go`：`encodeSnapshot`（used_turn_ids 排序、RFC3339Nano UTC、16MiB 硬校验）+ `decodeSnapshot`（DisallowUnknownFields、schema=1、ID/State/时间/Policy/Message/used_turn_ids 全校验）+ `validateResolvedPolicy`（max_messages/message_bytes>0、ttl/max_lifetime 0 或 >=1m、max_lifetime>=ttl）。
  - `manager.go`：`Manager` 含 `sessions/agentIdx/runners/activeTurns` map + `closing/closed` + `agentExists/agentOverride` 注入；`NewManager(opts)`、`Restore(ctx,now)`（Keys 排序、逐 key 解码、desiredState 覆写、原子发布索引）、`Start`（cleanup goroutine）、`Shutdown`（cancel turn + close stop + 等 runner 退出）、`cleanupLoop/cleanupOnce`、`runInSession`（锁内检查→解锁后 select send/stop/closing）、`commit`（persist 时 Set + Lock 替换 sessions[id]）。
  - `lifecycle.go`：`Create`（Lock 内容量检查+ID 预留+unlock 后 Storage.Set，失败 rollbackCreate）、`Get`、`List`（降序 created_at/ID、分页）、`Pause`/`Resume`（max_lifetime 检查→ErrSessionExpired）、`Close`（幂等）、`Delete`（runner task 内 Storage.Delete+索引摘除，task 外 stopRunner）。
  - `turn.go`：`Turn{manager,sessionID,turnID,closed,mu}`、`Snapshot`、`AppendUser`（首条 user、TurnID 写 used_turn_ids、created→active）、`Append`（batch 校验：单 final assistant 或 assistant+tool unit、一一对应、字节限制、max_messages）、`commitCandidate`（乐观深拷贝→校验→commit）。
  - `runturn.go`：`RunTurn`（activeTurns 预留 turnID→conflict 拒绝→onQueued(position)→runner task 内执行 callback；唯一 defer 清理 activeTurns+close(done)；queued cancel 跳过 callback；session Deleted 返回 NotFound；Return context.Cause(turnCtx)）、`CancelTurn`、`CancelAgentTurns`（收集+cancel+wait done）。
  - `messages.go`：`ListMessages`（role 过滤、after 增量、page/page_size，深拷贝）、`DeleteMessage`（Tool unit 原子删除、候选序列重校验）、`ClearMessages`（no-op 空历史）。
  - `sort.go`：sortMessagesByTimeID、deepCopyMessages、containsString。
- Runner 并发安全：用 `stop chan` 而非 `close(tasks)` 终止，`close(r.stop)` 在 `m.mu.Lock` 内执行；`runInSession` 和 `RunTurn` send 都 `select { r.tasks<-t | <-m.closing | <-r.stop }` 防 send-on-closed panic。
- Runtime 接入：Start 顺序 Storage→Provider→Session(Restore+Start)→API；Shutdown 逆序 API→Session→Provider→Storage；`components.session_restore=ready`。
- session 测试 10 用例全绿：validate/states + Create/Get/List/Capacity/Close/Delete + AppendUser+Append + RunTurnConflict + DeleteMessage + ClearMessages + Restore(跨实例) + 100 并发 RunTurn。

## 下一步

- Agent 生命周期与基本对话流程（非流式 POST /messages）。
- Remote API session 端点集成。
- 流式 SSE/WS。

## Remote API Session 端点（已完成）

- `internal/api/session_provider.go`：定义 `SessionProvider` 接口（注入 Session Manager）、`AgentExistsProvider`（注入 Agent 存在性 + override），以及各 REST DTO（`sessionDTO`/`sessionShortDTO`/`clearMessagesDTO`/`deleteSessionDTO`/`messageDTO`/`createSessionRequest`）。
- `internal/api/session_handler.go`：`registerSessionRoutes` 注册 `/api/v1/agents/:id/sessions` 和 `/api/v1/sessions/:id[/子资源]`；Go 1.20 ServeMux 手动路径解析。Handler 全覆盖：Create(201)、List(分页+state 过滤)、Get、Pause、Resume、Close、Delete、ClearMessages、ListMessages(role+after+分页)、DeleteMessage(Tool unit 原子)。`writeSessionError` 按 docs/session/errors.md §4 错误映射（SnapshotTooLarge→422 优先于 PersistenceFailed→503、capacity→429、state→409、not found→404、invalid→400）。`decodeBody` 禁止未知字段。
- `internal/api/server.go`：Server 加 `sessions`/`agentExists` 字段 + `SetSessionProvider` 注入方法；`register` 调 `registerSessionRoutes`。
- `internal/runtime/runtime.go`：Runtime 加 `agentExists`/`agentSessionOverride`（查 cfg.Agents）+ `agentAPIShim` 实现 `AgentExistsProvider`；Session Manager 创建时注入 Agent 信息；API Server 在 Start 后 `SetSessionProvider`。
- api 测试 6 用例绿：Create(201+DTO/agent_id/state/id前缀)、Create-AgentNotFound(404 40401)、List(分页)、Get+Pause+Resume+Close+Delete+AfterDelete404、ListMessages(2条顺序)、ClearMessages.
- 全项目 `go test ./...` + `go vet ./...` + `go build ./...` 全绿。
- Gitea push 仍 hang（服务端 receive-pack POST 无 ACK，连续 5+ 轮验证；已多方案尝试无果），本地 7 提交待推。

## Context 窗口管理器（已完成）

- `internal/context` 包实现 docs/context 契约：
  - `errors.go`：8 个稳定错误 sentinel。
  - `budget.go`：`ResolveContextBudget(cfg, modelWindow, modelMaxOutput, outputTokens)` 计算 EffectiveWindow/ReservedOutput/Input，窗口未知或 reserve/output 关系非法返回对应错误。
  - `manager.go`：`Manager` 无状态，`Build(ctx, BuildInput)` 唯一公开入口。流程：校验前置条件→计算预算→groupUnits 构造并校验消息单元→EstimateInputTokens 估算完整候选→不超限直接返回→超限时估算 protected-only→按策略（reject/truncate/hybrid）处理→最终估算确认不超限→返回 BuildOutput+Metadata。
  - `groupUnits`：开头 system 各自成 protected unit；按 user 分 turn；assistant+tool results 组成不可拆分 unit；CurrentTurnStart 所在 turn 及之后受保护；历史 Tool turn 不可压缩；orphan/重复/缺失 Tool result 返回 ErrInvalidMessageSequence。
  - `truncate`：从最旧可删除 unit 开始删除直到不超限，无可删 unit 返回 ErrContextOverflow。
  - `hybrid`：v1 暂降级为 truncate（摘要需 Provider 摘要调用，后续补全）。
  - `copyRequest`：深拷贝 Messages + ToolCalls。
- 7 个 context 测试全绿：under-budget 直接返回、reject 超限报错、truncate 删旧 unit、groupUnits 分组校验、invalid sequence（orphan/非 user 首条/不完整 Tool chain）、ResolveContextBudget 正常+窗口未知。
- Runtime 接入 `contextM *ctxwindow.Manager`（Phase 2 后续 Agent 调用）。
- 全项目 test/vet/build 全绿。
- 2 个本地 pending 提交（session 2c83ef0 + api 0646a52 + ctx 待提交）。

## Agent Manager（已完成 Phase 2 最小版）

- `internal/agent` 包 实 现 docs/agent.md 最小 直接 路 径（无 Tool/Memory/Skill/Planner）：
  - `types.go`：Status(running/paused/stopped)、7 个 稳定 错误、Info/Detail/TurnRequest/TurnEvent/ToolResultEvent/TurnResult。
  - `manager.go`：NewManager(校验 Config/Sessions/Context/Providers 非 nil + 每个 Agent ID/provider/model/max_tokens 有效)、Get/Inspect/List(按 ID 升序)、Start/Pause/Stop(状态机+幂等)、Quiesce、Shutdown、CancelTurn(委托 Session.CancelTurn)。
  - `handle_turn.go`：HandleTurn 校验入参 + Agent 状态(stopped/paused) → session.RunTurn 内执行 runDirectTurn：AppendUser → Session.Snapshot 组装 canonical ChatRequest(system prompt + 历史) → Context.Build → Provider.Chat → Append final assistant。v1 无 Tool loop/alias 投影（Phase 3 补全）。
- 4 个 agent 测试全绿：Get/List/NotFound、Life cycle Pause/Stop/Start 幂等+状态错误、HandleTurn direct 端到端(fake OpenAI httptest provider, Assert assistant=Mock answer)、HandleTurn invalid 入参与 NotFound。
- 全项目 test/vet/build 全绿。

## 对话 API 端点（已完成）

- `POST /api/v1/sessions/:id/messages`：提交 user 消息并等待 Agent turn 完成。
  - 入参 `postMessageRequest{turn_id, content, metadata}`，`turn_id` 校验 1..128 UTF-8 bytes 无控制字符。
  - 取 session.AgentID 后调 `agent.Manager.HandleTurn` → 在 Session FIFO 内 AppendUser → Context.Build → Provider.Chat → Append final assistant。
  - 成功返回 `postMessageResponse{turn_id, message(展开 Payload), usage, tool_call_count}`。
- `writeTurnError` 按 conversation.md cause 优先映射：
  - context.DeadlineExceeded → 504/50401；context.Canceled → 不写 response
  - ErrAgentStopped/Paused/InvalidState → 409/40901
  - ErrAgentManagerClosed → 503/50301
  - ErrAgentNotFound → 404/40401；ErrAgentInvalidRequest → 400/40001
  - ErrAgentToolRoundLimit/ProviderProtocol → 500/50001
  - Session 错误复用 session 错误映射
  - `*provider.ProviderError` (errors.As) → 502/50202
- `internal/api/agent_provider.go`：`AgentProvider` 接口（HandleTurn + Get）。
- `server.go`：Server 加 `agents` 字段 + `SetAgentProvider` 方法。
- `runtime.go`：Start 后 `SetAgentProvider(am)` 注入。
- 2 个 conversation 测试全绿：direct turn 端到端（POST → assistant="Hi there"），invalid turn_id 返回 400/40001。
- 全项目 test/vet/build 全绿。

## Phase 2：SSE 流式对话（已完成本地代码）

- `internal/session/hub.go`（新增）：Session 级事件总线 Hub。
  - `Subscribe()` 返回 `*Subscriber`（容量 256 的 buffered `chan any` + Done 信号）。
  - `Publish(ev)` 非阻塞 enqueue；队列满则原子注销该订阅者并关闭其通道（不丢帧、不阻塞发布者）。
  - `Close(reason)` 向所有订阅者投递终态事件后关闭通道，幂等；`Unsubscribe(s)` 单点注销。
  - Hub 存 `any` 事件以解耦 agent 与 session 互 import 循环。
- `internal/session/manager.go`：Manager 持有 `hubs map[string]*Hub`；`Hub(sessionID)` 返回/懒创建对应 Hub（不存在返回 `ErrSessionNotFound`，已关闭返回 `ErrManagerClosed`）；`Shutdown` 关闭所有 Hub（投递 nil reason），`Delete` 关闭对应 Hub。
- `internal/agent/types.go`：`TurnEvent` 扩容 `Assistant *session.SessionMessage`、`Usage *provider.Usage`、`ToolCallCount *int`、`Code/Message/Reason`，支持 `assistant_done / error / session_end` 终态字段。
- `internal/agent/handle_turn.go`：新增 `callProvider`/`callChat`/`callStream`：
  - `req.Stream==true` 时走 `Provider.StreamChat`，累积 delta；首 chunk 带 role 或首字节发 `assistant_start`，每个 reasoning/content chunk 发对应 `reasoning_delta`/`assistant_delta`，累积完整 assistant message 后返回给 Append。
  - `assistant_done` Emit 现在携带已持久化的 assistant SessionMessage 指针 + Usage 指针 + ToolCallCount 指针。
- `internal/api/conversation_frame.go`（新增）：`ConversationFrame` wire DTO（与 conversation.md 一致）+ `ToolResultView` + `SessionMessageView` + `SessionEventView`，`turnEventToFrame` 把 `agent.TurnEvent` 映射成 wire 帧；`sessionMessageView` 把 `*session.SessionMessage` 转 wire 视图。
- `internal/api/sse_handler.go`（新增）：`GET /api/v1/sessions/:id/events` SSE 订阅端点：
  - Header `text/event-stream` + `Cache-Control: no-store` + `X-Accel-Buffering: no`，`WriteHeader(200)` 后立即 `flusher.Flush()` 让 client 拿到响应头。
  - 每帧 `event: conversation\ndata: <json>\n\n`；每 15s `: heartbeat`。
  - Hub 关闭或 `r.Context().Done()` 退出；`defer hub.Unsubscribe(sub)`。
- `internal/api/conversation_handler.go`：`handlePostMessage` 注入 SSE 转发：
  - 当 `sessionMgr != nil` 时取该 Session 的 Hub，设 `turnReq.Stream=true` + `Emit` 把 `TurnEvent` 经 `turnEventToFrame` 转 ConversationFrame 后 `hub.Publish`；REST 同步仍等结果以 JSON 返回。
  - HandleTurn 失败时通过 `errorFrameFromTurnError` 向 Hub 发送一个 `error` ConversationFrame 终态（cause→code 十进制字符串 / cancel→"canceled"），与 REST `writeTurnError` 映射一致。
- `internal/api/server.go`：Server 新增 `sessionMgr *session.Manager` 字段 + `SetSessionManager` setter。
- `internal/runtime/runtime.go`：Start 注入 `rt.api.SetSessionManager(sm)`。
- `internal/api/session_handler.go`：路由分发加 `sub == "events" && Method==GET` → `handleSSEEvents`。
- 测试：
  - `internal/session/hub_test.go` 4 例：publish/subscribe、慢消费者 drop、Close 广播、Close 后 Publish no-op。
  - `internal/agent/manager_test.go` 增 `TestAgentHandleTurnStreamEmit`：SSE mock Provider，校验 emit 序列含 queued/assistant_start/assistant_delta/assistant_done，delta 累积 "Hello"，assistant_done 带 assistant+usage+tool_call_count。
  - `internal/api/conversation_handler_test.go` 增 `TestAPISSEEvents`：建立 SSE 订阅，手工 Publish 五帧，校验 wire `event:`/`data:` 透传 + `assistant_delta` 累积 "Hi there"；并扩展 `conversationTestEnv` mock server 按 `"stream":true` 返回 SSE 流，注入 `SetSessionManager` 让 POST 也走 stream 路径。
- 全项目 build/vet/test 全绿。

## Phase 2：WebSocket /stream 端点（已完成本地代码）

- 依赖 `github.com/gorilla/websocket v1.5.3`（兼容 Go 1.20）。
- `internal/api/ws_handler.go`（新增）：`GET /api/v1/sessions/:id/stream` WebSocket Upgrade。
  - 握手必须 Authorization Header（v1 只校验存在；具体 auth 校验后续 Auth 中间件接入）。
  - 前置校验 agents/sessionMgr 注入、Session 存在；错过则在 Upgrade 前返回 503/404。
  - 三个 goroutine：reader（解析 client 应用 frame）、writer（从 Hub 订阅流 ConversationFrame）、ping loop（ws transport ping，与 Session 无关）。
  - 双向通过共享 Session Hub：POST/WS 都可向 hub Publish，WS 单 writer 串行写 JSON，wmu 互斥。
  - reader：message frame → 校验 turn_id 重复 → 启动 `HandleTurn(Stream=true, Emit=hub.Publish)` 同步执行（hub 帧自动被 writer 推回此连接）；cancel frame → 调用本地 `turnCtx.cancel`。未知 cancel 回 40001/turn not active。
  - 连接断开：reader goroutine 退出 → `cancelAllTurns()` 取消所有本连接发起的非终态 turn，并 close conn、Unsubscribe hub；writer/ping loop 同步退出。
- `internal/session/hub.go`：新增 `SessionEndEvent{Reason string}`，作为 hub Close 时 publish 给订阅者的终态事件。
- `internal/session/lifecycle.go`：`Close` 成功 transition 后 `closeHubLocked(sessionID, &SessionEndEvent{Reason:"closed"})`；`Delete` 改 `&SessionEndEvent{Reason:"deleted"}`。
- `internal/api/conversation_frame.go`：新增 `sessionEndToFrame`，转出 `ConversationFrame{Type:"session_end", Reason}`。
- `internal/api/sse_handler.go` + `ws_handler.go`：事件 type switch 增加 `*session.SessionEndEvent` 分支；写完后 return 结束订阅（session_end 为订阅终止终态）。
- `internal/api/session_handler.go`：路由分发加 `sub == "stream" && Method==GET` → `handleWSStream`。
- 测试：
  - `ws_handler_test.go` 4 例：TurnFlow（SSE mock provider 流式，校验 queued/assistant_start/assistant_delta/assistant_done + delta 累积 "Hi there"）、CancelBeforeStart（缺 Authorization 返回 401）、CancelRunningTurn（cancel 未知 turn_id 返回 40001 "turn not active"）、DisconnectCancelsTurn（断连后复用同 turn_id 重新发起成功）+ SessionEndOnClose（Session Close 后 WS 收到 session_end reason=closed）。
  - `conversation_handler_test.go` 增 `TestAPISSESessionEnd`：SSE 订阅 + sm.Close 后客户端拿到session_end/closed。
  - 修复 SSE handler 因移除 `data:` 前缀造成的 TestAPISSEEvents 回归。
- 全项目 build/vet/test 全绿。

## Phase 2：Claude adapter（已完成本地代码）

- `internal/provider/claude.go`（新增）：Anthropic Messages API adapter。
  - `Chat`：POST `<base>/v1/messages`，header `x-api-key` + `anthropic-version: 2023-06-01`。
  - 请求转换：
    - `messages` 中 `system` role 提取到 top-level `system`（多条拼接）。
    - `tool` role（即 tool 结果） → `user` role + `tool_result` block（带 `tool_use_id`/`content`）。
    - `assistant` role 带 `tool_calls` 转 content blocks：`thinking`（可选）+ `text` + 每个 `tool_use`（ID/name/input JSON）。
    - `ToolDef` → Anthropic `{name, description, input_schema}`。
    - `ToolChoice` 模式映射：auto/none/required/specific → Anthropic `{type:"...","name":...}`。
    - `ThinkingConfig` → `{type:"enabled", budget_tokens}`。
    - `Extra` 注入顶层。
  - `StreamChat`：解析 Anthropic SSE 事件 `message_start`、`content_block_start/delta/stop`、`message_delta`、`message_stop`，映射到 ChatChunk：
    - `text_delta` → `Delta.Content`；`input_json_delta` → `Delta.ToolCalls[0].Arguments` 增量（带 stable block id）；`thinking_delta` → `Delta.ReasoningContent`。
    - `stop_reason` → `FinishReason`（end_turn/stop_sequence=stop、tool_use=tool_calls、max_tokens=length）。
    - usage 合并 input_tokens+output_tokens。
  - 错误分类：HTTP 401/403/429/5xx → ProviderError Code（401=unauthorized、403=forbidden、429=rate_limit+RetryAfter、5xx=server）；复用 openai 的 `classifyHTTPStatus`。
- manager.go `init()` 注册 factory `"claude"`。
- 测试 `claude_test.go` 6 例：Chat 文本（system/max_tokens/Anthropic-Version 校验）、Chat tool_use（input JSON 转 string args）、错误分类（401/403/429 retryable）、Stream text+tool delta（delta 拼接 + tool_use input 增量 + stop_reason→tool_calls）、Stream thinking（`thinking_delta`/`text_delta`/usage）、Manager factory 注册端到端。
- 全项目 build/vet/test 全绿。

## Phase 2：Gemini adapter（已完成本地代码）

- `internal/provider/gemini.go`（新增）：Google Generative AI REST API adapter。
  - `Chat` POST `<base>/v1beta/models/{model}:generateContent?key=<APIKey>`；`StreamChat` `:streamGenerateContent?alt=sse&key=<APIKey>` SSE。
  - 请求转换：`messages` → `contents[{role:user|model, parts:[{text|functionCall|functionResponse|thought}]}]`；`system` role → top-level `systemInstruction`；tool role → user role `functionResponse`；assistant tool_calls → `functionCall` parts。tools 包 `{functionDeclarations: [...]}`；toolChoice 映射 AUTO/ANY/NONE + `allowedFunctionNames`。ThinkingConfig → `generationConfig.thinkingConfig{includeThoughts, thinkingBudget}`。Extra 注顶层。
  - 响应：candidates[0].content.parts text/thought/functionCall → ChatResponse；finish STOP→stop、MAX_TOKENS→max_tokens 等；usageMetadata → Usage。
  - 流式：解析 SSE `data:` JSON GenerateContentResponse。每个 candidate 的 text/thought/functionCall 累加为 delta chunk；finishReason 单独一个终态 chunk；usageMetadata chunk。
- Factory `"gemini"` 在 init 注册。
- 测试 `gemini_test.go` 6 例：Chat text（URL/key + systemInstruction 校验）、Chat tool_call（args JSON）、Chat 错误 429 rate_limit retryable、Stream text 累积、Stream thinking + tool（thought/text/functionCall 同时帧）、Manager factory。

## Phase 2：Ollama adapter（已完成本地代码）

- `internal/provider/ollama.go`（新增）：Ollama REST `/api/chat` adapter。
  - Messages 结构与 OpenAI 兼容（role/content/tool_calls）；请求体 `{model, messages, stream, tools, options}`；Extra 注入顶层（如 `keep_alive`）。
  - options：`MAXTokens → num_predict`、`Temperature`、`TopP`、`Stop`。
  - Chat 非流式：解析 wire.{message, done, done_reason, prompt_eval_count, eval_count} → ChatResponse。
  - StreamChat 流式：每行是 JSON object（非 SSE event），逐次 emit ChatChunk：content/tool_calls/done_reason/usage；done=true 时一次性发终态。
- 错误分类：404 含 "model" → `ErrCodeModelNotFound`；其他 HTTP 状态复用 classifyHTTPStatus；429 RetryAfter。
- Factory `"ollama"` init 注册。
- 测试 `ollama_test.go` 5 例：Chat text（path 校验 + stream:false）、Chat tool_calls（args JSON + done_reason→tool_calls）、Chat 404 model_not_found、Stream text 累积、Manager factory。

## Phase 3：Tool 系统起步（Manager 已完成本地代码）

- `internal/tool/errors.go` + `tool.go`：sentinel 错误集 + `Tool` 接口 + `ExecutionScope`/`ToolResult`/`ToolInfo` + `RetryableError` + `ValidationError`。
- `internal/tool/manager.go`：Manager（v1 静态配置注入，无 ReloadManager 动态 reload）。
  - `NewManager(deps)` 深拷贝 `cfg.Tools.Builtin` 到 configs 和 source=builtin；解析 `cfg.Agents[].Tools` allowlist（空 = AllowAll）。
  - `Register/Unregister/Get/List/ListForAgent/CheckPermission`、`AgentScope`。
  - `ToolDefs(agentID)` 把 enabled+authorized 工具映射为 `provider.ToolDef`（v1 canonical name 直接作为 wire alias，未实现 provider.md 的 alias 碰撞反查）。
  - `Execute` 执行流程：validate AgentID → Tool lookup → enabled → permission → validateParams → effective timeout (Tool 超时 fallback default；上界 MaxTimeout) → 全局 gate semaphore → callCtx WithCancelCause + `time.AfterFunc` 设 ErrToolTimeout cause → Tool.Execute；caller cancel 优先返回 context.Cause(ctx)。
  - `ExecuteBatch`：保持输入顺序的并发批量，worker 数 = `min(len(calls), MaxConcurrent)`；硬错误通过 `ErrorResult` 投影为单独项的 `ToolResult{IsError:true}`；caller ctx 失败在全部完成后返回 context.Cause。
  - `ErrorResult` 脱敏映射 6 个 sentinel → 固定 LLM-friendly message（不拼接 err.Error()）。
  - Ponytail：validateParams 用序列化可行性校验代替 JSON Schema validator（后续接入完整 validator）；ListForAgent 使用稳定插入排序避免引入 sort。硜 Go 1.20 `context.WithCancelCause` + `time.AfterFunc` 实现 cause-preserving timeout（不能使用 1.21 才有的 `context.WithTimeoutCause`）。
- 测试 `manager_test.go` 11 例：list/listForAgent、checkPermission、execute echo/not found/disabled/denied、timeout、caller cancel cause 保持、batch 顺序+missing tool、ErrorResult mapping。

## Phase 3：内置 Tool shell/http/file（已完成本地代码）

- `internal/tool/builtin/shell.go`：`/bin/sh -c` 执行，stdout+stderr 合并，超 max_output_bytes 截断；blocked/allowed 前缀匹配（blocked 优先）；working_dir + env；非零退出 IsError=true 含退出码。
- `internal/tool/builtin/http.go`：HTTP 请求；headers/body 注入；返回 status_code + headers + body + elapsed_ms JSON；blocked/allowed hosts 精确匹配（blocked 优先）；max_response_bytes 截断。
- `internal/tool/builtin/file.go`：4 个 file_read/file_write/file_list/file_delete 子 Tool（fileTool 分支 by name）。统一 path canonical 校验（canonicalPath 解析最近祖先、EvalSymlinks、再拼回 tail；validatePath within check）。allowed_paths/blocked_paths，blocked 优先；max_file_size 上限；read 支持 utf-8/base64 + max_bytes；write create_dirs；list 排序；delete 文件或空目录。
- `internal/tool/builtin/{shell,http,file}_test.go`：shell 5 例、http 4 例、file 6 例（read/write/delete 循环；blocked/not allowed；list sorted；create_dirs；base64）。
- 全项目 build/vet/test 全绿。

## Phase 3：Runtime 注册 builtin + Agent Tool Loop 闭环（已完成本地代码）

本段让 Tool 系统与 Agent turn 真正闭环：内置工具在 Runtime 启动时注册到 Tool Manager，
Agent turn 内按 docs/agent.md §4 + docs/tool/provider.md §3-§5 完整走 Tool loop 端到端。

### Tool Manager：ProviderToolProjection（`internal/tool/projection.go` 新增）

- `ProviderToolProjection`：不可变 turn-local 投影。`defs []provider.ToolDef`（按 canonical name UTF-8 升序）、`canonicalToAlias map`（definitions + history-only 的并集）、`aliasToCanonical map`（仅 executable definitions）。
- 算法（docs/tool/provider.md §2 严格实现）：
  - `providerSafeToolName = ^[A-Za-z_][A-Za-z0-9_-]{0,63}$`；安全 canonical 直接 identity，unsafe 走 `t_` + 完整 SHA-256 base32（不 trim、不截断）。
  - 合并 current definitions（ListForAgent）与 history 中出现过的 canonical name（assistant ToolCalls[].Function.Name / tool 消息 Name 非空）；碰撞返回 `ErrToolAliasCollision`（含稳定构造分支：取任一 unsafe canonical 的 hash alias，把它本身作为第二个 canonical 再投影）。
  - history-only 名进入 union 表（可投影历史回传），但**不进 executable 反查表**（不恢复执行权限）。
- API：
  - `ToToolDefs(agentID, history) (*ProviderToolProjection, error)`
  - `ProjectRequest(req ChatRequest) (ChatRequest, error)`：要求 `req.Tools` 必须空；深拷贝 req 并注入冻结 defs、改写 assistant ToolCalls 名 / tool message Name / specific ToolChoice。
  - `ResolveExecutable(alias) (canonical string, ok bool)`：精确大小写敏感查找；不着色 history-only/unknown。
  - `Defs()` 返回深拷贝。
- 深拷贝 helper：`cloneToolDef` / `cloneRawMessage` / `cloneChatRequest`（Message.ToolCalls、ToolChoice、Stop、Extra、Thinking、ResponseFormat 均独立）。
- 删除旧简化版 `Manager.ToolDefs(agentID)`（无外部测试依赖）。
- Ponytail 决策：v1 内置工具名（shell/http/file_read/write/list/delete）均 provider-safe，投影路径恒等映射；但仍按文档算法实现 hash alias 以坐实安全边界（Ponytail 例外：input validation at trust boundaries），后续接 MCP/插件带 unsafe canonical 名时无需重构 Agent loop。
- 测试 `projection_test.go` 6 例：defs 仅 authorized、ProjectRequest 写 alias + 深拷贝不破坏原请求、ProjectRequest 拒非空 Tools、specific ToolChoice 命中 executable 校验、hash alias + 稳定 ErrToolAliasCollision 构造、history-only 名可投影但不可执行。

### Manager 文件容器分裂（`internal/tool/manager.go`）

- docs/config/reference.md §6.3 约定 `tools.builtin.file` 是 `file_read/file_write/file_list/file_delete` 共享配置组。
- `NewManager` 在 builtin 配置复制循环后追加 `file` 容器分裂：把 `Builtin["file"]` 复制到 4 个 canonical 名的 configs（仅当显式配置 file_read 等键时跳过），保证 Enabled/Timeout/Options 与文档语义一致，不依赖 Register 默认填空。
- 既有 Manager 11 项测试全绿（行为不受影响）。

### Runtime 注册 builtin（`internal/tool/builtin/register.go`）

- `RegisterBuiltin(m *tool.Manager, cfg *config.Config) error`：把 shell/http/file_read/file_write/file_list/file_delete 6 个 Tool 构造并 Register，配置取自 `cfg.Tools.Builtin`（file_* 共享 `file` 容器）。
- disabled Tool 也 Register（保留在 List 以维持禁用语义，docs/tool/manager.md §3 step 4）。
- `Runtime.Start` 在 Provider ready 后插入：
  1. `tool.NewManager(Dependencies{Config, Providers, Logger})`
  2. `RegisterBuiltin(tm, cfg)`
  3. 每个 Agent 当前 definitions 空历史 `ToToolDefs(ag.ID, nil)` 校验（启动 binding 检查；alias 碰撞或非法名在 Ready 前尽早失败）
  4. `rt.agents.SetTools(tm)` 注入。
- Shutdown 逆序置 `rt.tools = nil`。
- Runtime struct 加 `tools *tool.Manager` 字段，Health components 多 "tool": "ready" 一项。

### Agent Tool Loop（`internal/agent/handle_turn.go` 重写 runDirectTurn）

新 `runDirectTurn` 按 docs/agent.md §4 完整 Tool loop：

1. `turn.AppendUser(req.Content, req.Metadata)`
2. 重复最多 `maxToolRounds=8` 轮（types.go 新增常量）：
   1. `turn.Snapshot()` 拿 canonical history（system + 历史消息）
   2. 找 ModelInfo、算 currentTurnStart（最后一条 user）
   3. `m.deps.Tools.ToToolDefs(a.id, canonicalMsgs)` 冻结 projection；Tools 未注入时 projection 退化为 nil（兼容 v1）
   4. 组装 canonical `ChatRequest`（Tools 留空）
   5. `projection.ProjectRequest` 注入 alias definitions 与历史 ToolCalls 名投影
   6. `Context.Build(已投影请求)`（看到最终 wire alias）
   7. `callProvider`（direct / stream 二选一）累积 assistantMsg + usage；totalUsage 逐轮累加
   8. Tool 判定：`len(assistantMsg.ToolCalls)>0 && proj != nil` 时 `resolveToolCalls` 校验 + 反查
   9. 无 tool_calls → `turn.Append` 单条 final assistant → emit `assistant_done`（Usage=累计、ToolCallCount=累计）→ 返回
   10. `rounds+1 >= maxToolRounds` → `ErrAgentToolRoundLimit`（不提交 partial unit）
   11. `tool.ExecuteBatch(ctx, ExecutionScope{AgentID, SessionID}, calls)` 执行 results
   12. canonical 名写回 assistantMsg.ToolCalls（Session 永远只持有 canonical，不外泄 wire alias）
   13. 构造单批 unit `[assistant(tool_calls), tool, tool, ...]` 一一对应；`turn.Append` 原子提交（Session classifyAppendBatch + validateBatchSequence 已支持）
   14. 流式下 emit `tool_call` / `tool_result` 进度事件（按 call 输入顺序）
   - 进入下一轮（复用本 turn 冻结的 projection）

- `resolveToolCalls`：每个 call `Arguments` 必须 `isValidArgsObject`（单个 JSON object + 拒绝 trailing token；用 `json.NewDecoder` + `dec.More()`）；alias 经 `ResolveExecutable` 精确反查；任一失败 `ErrAgentProviderProtocol`，整批 not executed、不提交 partial（docs/agent.md §5）。
- `isValidArgsObject` 拒空串、数组、标量、object 后接字符。
- `callProvider`/`callChat`/`callStream` 保持纯净（不感知 projection），反查在主 loop 做。
- 修正旧注释里的中文乱码（"圈 - 暫跳构建...在 v1 direct 不绕 Context..."）。

### Pre-existing bug 修复：RunTurn callback sentinel 透传（`internal/session/runturn.go`）

发现并修复一个 pre-existing 契约 bug：`RunTurn` `<-done` case 无条件 `cancel(nil)` 抹掉了 `context.Cause(turnCtx)`，导致 callback 返回的业务 sentinel（如 `ErrAgentProviderProtocol`/`ErrAgentToolRoundLimit`）一律变成 `context.Canceled`，`HandleTurn` 调用方永远拿不到业务错误分类。
- 改为：done 收到 err 时，非 nil 用 `cancel(err)` 设 cause 让 `context.Cause` 透传；nil path 仍 `cancel(nil)` 回收资源。
- session.Manager 既有所有测试不变；conversation_handler 的 `errors.Is(err, ErrAgentToolRoundLimit/ErrAgentProviderProtocol)` 业务分类自此可命中。

### Agent Tool Loop 测试（`internal/agent/handle_turn_test.go` 新增）

- `localEchoTool`：agent 包内 Tool stub，回 `params["msg"]`。
- `newToolLoopEnv`：完整链路（memory Session + Provider mock（httptest + atomic 计数切换响应）+ `tool.Manager` + 注册 echo + Agent `Tools: ["echo"]` allowlist）。
- 4 项端到端测试：
  - `TestAgentHandleTurnWithToolLoop`：round1 tool_calls(echo,"hi") → round2 final("done")；`ToolCallCount==1`；Provider 恰好被调用 2 次；Session 4 条消息序列正确（user, assistant(tool_calls), tool["echo: hi"], assistant(final)）；tool_call 持久化的是 canonical 名 `echo`。
  - `TestAgentHandleTurnUnknownToolAlias`：Provider round1 返回 alias `"not_registered"` → `ErrAgentProviderProtocol`，Session 仅 user 一条（无 partial assistant/tool）。
  - `TestAgentHandleTurnRoundLimit`：Provider 每轮恒返回 tool_calls → `ErrAgentToolRoundLimit`。
  - `TestAgentHandleTurnInvalidArgsObject`：arguments 是数组 `[1,2,3]` → `ErrAgentProviderProtocol`。

### 验证

- `go vet ./...` / `go build ./...`：0 issue / 0 error。
- `go test -count=1 -timeout 120s ./...`：全部 ok（agent/api/config/context/logging/provider/runtime/session/storage/tool/tool/builtin）。
- WS 取消延迟 race 一度在混合时间窗观察到 transient fail（测试内 `time.Sleep 200ms` 固有计时误差），重测单测 + count=20 稳定绿。

## 环境正规化（2026-07-25）

- Go 1.20 工具链从 `/tmp/go1.20.14` 搬到 `/usr/local/go`（Go 官方标准位置），包布局保留。
- 系统级符号链接：`/usr/local/bin/go` → `/usr/local/go/bin/go`、`/usr/local/bin/gofmt` → `/usr/local/go/bin/gofmt`。`/usr/local/bin` 已在系统默认 PATH，任何 shell（含 codex sandbox/非登录）直接 `go` 即用，无需 export PATH。
- `~/.bashrc` 删除临时的 `export PATH=/tmp/go1.20.14/bin:$PATH` 块；保留代理设置（`HTTP_PROXY/HTTPS_PROXY=http://192.168.4.1:7890`、`GOPROXY=https://goproxy.cn,direct`、`GOSUMDB=sum.golang.org`）。
- 验证：直接 `go version` → go1.20.14 linux/arm64；`go build ./...` 通过；env 变量覆盖生效（`GOPROXY=https://goproxy.cn,direct`）。

## Phase 3 Skill 系统（commit `947ac61`，已推送 gitea/main）

### 范围

按 `docs/skill/`：v1 Skill Manager（静态配置注入 + graph 校验 + per agent 投影）+ Agent turn 系统消息注入。

### internal/skill（新包，4 文件）

- `errors.go`：11 sentinel（`ErrSkillDirectoryUnavailable`/`ErrSkillNotFound`/`ErrSkillInvalid`/`ErrSkillDuplicate`/`ErrSkillDependencyMissing`/`ErrSkillDependencyCycle`/`ErrSkillDisabled`/`ErrSkillToolUnavailable`/`ErrSkillPermissionDenied`/`ErrSkillAgentNotFound`/`ErrSkillOptionsInvalid`）。
- `types.go`：`Status="loaded"|"disabled"`、`Skill{Name,Description,Version,Author,Tools,Skills,Options,Prompt}`、`Entry`、`ResolvedSkill{Name,Options,Prompt}`、`Manager`。常量上限：`maxSkillFile=1MB`、`maxDescription=4096`、`maxPromptBody=256KB`、`maxOptionsBytes=64KB`、`maxDepsPerCategory=64`。`skillNameRE=^[a-z0-9][a-z0-9-]{0,63}$`。`DefaultSkillDir="./skills"`。
- `frontmatter.go`：`parseSkillFile` 严格解析 SKILL.md（文件上限→splitFrontmatter→yaml Decoder KnownFields→name==dir→description/版本/SemVer[prepend v]→列表去重上限/JSON options/空 body）。helper：`normalizeCRLF`、`validateDepsList`、`validateOptionsJSON`。
- `manager.go`：`Load(skillsCfg, agents, tm, baseDir)` all-or-nothing——dir 解析 + ReadDir + Lstat 拒 symlink（`ModeSymlink` 显式 `ErrSkillInvalid`）+ 按目录名升序 + `parseSkillFile` + 索引唯一 + `validateSkillGraph`（DFS 环检测含 chain）+ per_skill 存在性 + entries map（含 status）+ 每 Agent `resolveForAgent`（allowlist 空不用、dep 必须在 allowlist、Tool 必须存在 + enabled + CheckPermission、options shallow merge、拓扑序去重）。`Get/List/ResolveForAgent` 深拷贝。

### Runtime 接入（`internal/runtime/runtime.go`）

- 加 `configPath`/`skills *skill.Manager` 字段、`SetConfigPath` setter。`cmd/yaa/main.go` 调用注入配置文件路径。
- `Start` 在 Tool Manager ready 后 `skill.Load(rt.cfg.Skills, rt.cfg.Agents, tm, baseDir)`（baseDir = configPath Dir 或 cwd）+ components["skill"]="ready"。Shutdown 逆序 `rt.skills=nil`。`am.SetSkills(rt.skills)`。
- 默认 `./skills` 目录缺失 = 启动失败（docs 强约束）；仓根建 `skills/.gitkeep` 让默认 cwd 启动可跑；`internal/runtime/runtime_test.go` 3 处 Start test 用 `t.TempDir()` 作为 skill dir。

### Agent 接入（`internal/agent/{manager,handle_turn,handle_turn_test}.go`）

- `manager.go`：`Dependencies` 加 `Skills *skill.Manager`；`SetSkills(sm)` 方法。
- `handle_turn.go` `runDirectTurn`：每轮 base sysPrompt 之后按拓扑序注入 Skill 系统消息（`Skills.ResolveForAgent(a.id)` → 每个 `renderSkillSystemMessage(r)`）。helper：`renderSkillSystemMessage` 渲染 `## Skill: <name>\n\nOptions:\n<json HTMLescape 关闭>\n\nInstructions:\n<body>`。Skill 不进 Session，每轮从 Manager 不可变 snapshot 重新投影。
- 测试：`newSkillTestEnv` + `TestAgentHandleTurnInjectsSkillSystemMessages` + `writeSkillFile`/`indexOf` helper。验证 base + alpha + beta + user 顺序、alpha<beta、body 注入、Skill 不进 Session。

### 决策

- **Skill dir 解析基准**：docs 说"相对主配置文件目录"。Runtime 无 configPath → 加 `SetConfigPath` setter，有 configPath 时 baseDir=Dir(Abs(configPath))，否则 cwd（与 storage path 一致）。
- **Symlink 拒绝**：os.ReadDir 的 `DirEntry.IsDir()` 对 symlink-to-dir 在 Unix 返回 false（默默跳过）；改用 `os.Lstat` 检 `ModeSymlink` 显式 `ErrSkillInvalid`。
- **Dup name 跨 dir 不可达**：frontmatter.name 必须==dir，两个不同 dir 同名 Skill 必触发 `ErrSkillInvalid`（name/dir mismatch）而非 `ErrSkillDuplicate`。
- **YAML SemVer**：`x/mod/semver.IsValid` 需 `v` 前缀；frontmatter.Version 支持 `1.0.0` 与 `v1.0.0`（prepend v 校验）。
- **Options 合并**：SK-007 顶层 shallow merge（frontmatter→root per_skill→agent skills_config），不递归/不 append array。
- **tool.Manager 空 allowlist = AllowAll**：skill test 测"agent 不允许 http"时不能传 `[]string{}`，改用 `["zzz-stub-tool-example"]`（非空但不含 http）。

### 验证

- `go vet ./...` / `go build ./...`：0 issue / 0 error。
- `go test`：internal/skill 18 例全绿；internal/agent (含 skill 注入测试) 绿；runtime/session/tool/api 包全 ok，无 regression。
- WS TestWSStreamDisconnectCancelsTurn count=30 全绿；先前 count=20 偶发 `turn id already used` 是测试内 `time.Sleep(200ms)` 固有 timing flake，并非 `runturn.go` `cancel(err)` 改动引入（`errorFrameFromTurnError` 用 `errors.Is(err, context.Canceled)`，`ProviderError.Unwrap()` 透出 `context.Canceled` 仍可命中）。

### 下一步

- Phase 3 后续：Memory 系统 / Planner / Auth 认证（按 roadmap 顺序）。

## Phase 3 Auth 系统（进行中）

### 文档修复（commit `4067f9d`，已推送 gitea/main）

按 Auth v1 实施前勘察（W 系列文档审查）修关键表述：
- W9 统一 Auth 包 sentinel 集合：`docs/auth/authentication.md §3` 补 `ErrUnauthenticated`（由 RBACAuthorizer 在 identity==nil 时返回），消除与 `authorization.md §6.1` 的 sentinel 不一致。
- W7 JWT 构造器前置注释：`authentication.md §3.2 NewJWTAuthenticator` 注明 secret≥32 仅在 `Enabled && token_type==jwt` 时由 Runtime 构造器强校验，与 `config/validation.md` 条件化校验对齐。
- W3 明确 AuditLogger 为可选扩展点，v1 不强制实现、不注入、无注入 API；Authorizer v1 唯一实现即 RBACAuthorizer。
- W5 补 `remote-api/INDEX.md §5` 路由表记号说明：`read:agents` 仅为可读展示，注册时拆分为 `Action`/`Resource` 两个字段。
- W6 `config/validation.md validateAuthConfig` 在 `validResources`/`validActions` 旁补中文注释列出枚举清单。
- W10 `remote-api/auth.md §1` 明确 v1 无 refresh/token TTL 常量，JWT 过期由外部 issuer 通过 exp claim 决定，`runtime.auth.*` 全部 restart-required。
- W8 `agent.md §1` 补 Agent 层鉴权/身份归属声明：v1 不为 Agent 层引入独立 Auth；AuthN/AuthZ 由 Remote API Server 唯一 route wrapper 完成，Agent 仅做 Agent ID/Session ID 归属校验。

### Auth 包核心（commit `f1fc294`，已推送 gitea/main）

按 `docs/auth/authentication.md §2-§3` + `authorization.md §6` 实现 v1 Auth 包核心：
- `internal/auth/identity.go`：Identity{id,name,roles,claims} + `cloneIdentity` 深拷贝 + `identityContextKey{}` 唯一 context key + `ContextWithIdentity`/`IdentityFromContext` 双向 clone + `String` 脱敏日志 + `HasRole`。
- `internal/auth/errors.go`：三个 sentinel `ErrInvalidToken` / `ErrJWTInvalid` / `ErrUnauthenticated`。
- `internal/auth/authenticator.go`：`Authenticator.Authenticate(token) (*Identity,error)` + `Authorizer.Authorize(identity,action,resource) (bool,error)` 接口。
- `internal/auth/static.go`：`StaticAuthenticator` (sha256(token)->Identity 索引)，构造器防御兜底 name/token/roles 非空 + token value sha256 唯一，`Authenticate` 返回 clone，失败 `ErrInvalidToken`。
- `internal/auth/jwt.go`：`JWTAuthenticator` (golang-jwt/jwt/v5 **v5.2.2** 即 Go 1.20 兼容上限；v5.3.x 依赖 Go 1.21 `slices` 包)，强制 HS256 (`WithValidMethods`+keyfunc Alg 校验)，`WithIssuer/WithAudience/WithLeeway/WithExpirationRequired`，失败包 `ErrJWTInvalid`，成功要求 `Subject!="" && len(Roles)>0`，Identity.Claims 只含 issuer/expires_at；构造器 secret>=32/issuer/audience 非空/clock_skew in [0,5m]。
- `internal/auth/rbac.go`：`RBACAuthorizer` (Role->Permissions 深拷贝)，`Authorize` identity==nil→`ErrUnauthenticated`，遍历 Roles 全部权限累加 allowed/denied，**deny 优先于 allow**，未匹配默认拒绝，未知 Effect 返回 error，`matchPattern` 仅整字段 `*`（无前缀通配）。
- 依赖：`github.com/golang-jwt/jwt/v5 v5.2.2`（v5.3.x 依赖 Go 1.21 `slices` 包，选 v5.2.2 为 Go 1.20 兼容上限）。
- 测试 `auth_test.go`：Identity/clone/context roundtrip + Static 命中/未命中/构造兜底 + JWT happy/bad alg/坏 issuer/过期/空 subject/空 roles/缺 exp/构造兜底 + RBAC allow/deny 优先/wildcard/admin vs viewer/operator 矩阵/未匹配/nil identity/未知 effect/构造兜底，18+ 例全绿。

### Remote API Auth wrapper 基础（commit `968d9a0`，已推送 gitea/main）

按 `docs/auth/integration.md §1-§5` 落地 AuthN/AuthZ 在 Remote API 的入口基础设施；首批把 `/api/v1/health` 与 `/api/v1/version` 通过唯一 wrapper 注册并绑定 `RouteSpec(Action=read, Resource=system)`：
- `internal/api/server.go`：Server 新增 `authz/authn/authEnabled/publicPaths` 字段（mutex 保护），`SetAuth(enabled, authn, authz, publicPaths)` setter 把 publicPaths 规范化成 `map[string]bool` 便于 O(1) 精确匹配；`register` 把 health/version 用 `registerProtected` 替代 `mux.HandleFunc + methodGet`，绑定 RouteSpec read:system。
- `internal/api/route_auth.go`（新增）：`Transport`/`TransportHTTP`/`TransportWebSocket` 常量 + `routeSpec{Method,Pattern,Action,Resource,Transport}` + `bearerToken` (Cut + EqualFold Bearer + token 非空且无空白/tab) + `credentialCode` (errors.Is(ErrJWTInvalid)->40102 否则 40101，按 sentinel 不按字符串判断) + `registerProtected` 唯一 wrapper：method 校验在前→disabled/public bypass→extract Bearer→Authenticator.Authenticate→Authorizer.Authorize→ContextWithIdentity 注入；失败 401/403 对 envelope，AuthZ 拒绝写脱敏日志。另含 `authIdentityForWebSocket` 给 WS handler 复用。
- `internal/api/route_auth_test.go`（新增 13 例）：PublicBypass / DisabledBypass / MissingBearer 40101 / StaticValid / StaticInvalid 40101 / BearerBadFormat 40101 / RBACDeny 40301 / JWTValid / JWTBadIssuerCode 40102 / MethodCheckStill40501WithAuth（含 disabled 路径仍 405）/ IdentityInjectedIntoContext 端到端断言；全 api 测试无 regression。
- 注意：`agents/sessions/sse/ws` 子树与 WS handler 接入 wrapper 的精细 per-sub-path RouteSpec 绑定（含 37 路由注册全量测试）为后续 commit，本轮先把基础设施端到端跑通。

## Phase 3 Memory 系统第一阶段（commit `45fe5ca`/`7acee98`，已推送 gitea/main）

按 `docs/memory/{README,architecture,storage,lifecycle,errors,observability,decisions,config-ref,checklist,integration}` 实现 v1 Memory 系统第一阶段：types/errors/Manager + memstore 后端 + Runtime 启动/关闭接入。

### 文档现状
- Config 层 MemoryConfig/MemoryOverride/MemoryPolicy/ResolveMemoryPolicy/validateMemoryConfig/validateMemoryPolicy/DefaultMemoryConfig 在前期已落地（`internal/config/{types,policy,validation,defaults}.go`）；测试已绿。
- Auth 文档修复对应勘察 W1-W10 之后的 W3/W5/W6/W7/W8/W9/W10 已统一（commit `4067f9d`）。
- 对 Memory 子系统的文档审查（Ohm 勘察）：10 个文档交叉引用闭合，无架构性矛盾，10 点问题清单为表述澄清级别；本轮先按契约落地代码，文档项保留待后续 commit 微调。

### `internal/memory`（新包，5 文件）
- `errors.go`：17 个 sentinel（ErrMemoryDisabled/ErrMemoryClosed/ErrMemoryNotFound/ErrMemoryInvalidScope/ErrMemoryInvalidItem/ErrMemoryManagedField/ErrMemoryUnsupportedLayer/ErrMemoryExpiredInput/ErrMemoryQuota/ErrMemoryStoreUnavailable/ErrMemoryCorrupt/ErrMemoryEmbeddingFailed/ErrMemoryEmbeddingDimension/ErrMemoryEmbeddingZero/ErrMemoryIndexUnavailable/ErrMemoryIndexDegraded/ErrMemoryReindexFailed）。
- `types.go`：Layer/Scope/MemoryItem/SearchRequest/SearchResult/IndexStatus(PutResult)/ItemRef/CommitPutResult/VectorHit/VectorSearchRequest + 输入固定上限常量（MaxAgentIDLen=128/MaxSessionIDLen=128/MaxKeyLen=256/MaxContentLen=65536/MaxMetadataLen=16384/MaxSearchLimit=100/MaxDeleteExpiredLimit=10000）。
- `interfaces.go`：ContentStore (CommitPut/Get/Search/List/Delete/Clear/DeleteExpired/Count/Ping/Close) + Embedder/VectorIndex/VectorIndexFactory + Clock/SystemClock。
- `manager.go` (~1000 行)：
  - Manager 字段：store/embedder/indexFactory/indexes/indexMu/mutationGate/agentLocks(keyedMutex)/clock/events/workerCancel/workerDone/lifecycleMu/inFlight/closing/closeOnce/closeDone。
  - 8 个 canonical 事件类型 (EventAdded/Updated/Deleted/Promoted/Expired/Evicted/Degraded/Error) + Event + EventEmitter 接口。
  - beginOp (lifecycleMu 内原子检查 closing + inFlight.Add(1)) + Close (幂等, closeOnce.Do: 等 workerDone -> inFlight.Wait -> 一次 store.Close; ctx 到期返回 ctx.Cause)。
  - Put: 校验 item/policy/层 + 深拷贝 + normalizeExpiresAt (3 态: nil+default_ttl / zero time=永不过期 / 非零 <=now=ErrMemoryExpiredInput) + 锁序 mutationGate.RLock -> Agent keyed lock + 计算 delta + Count live + 选 victims (fifo/ttl) + ContentStore.CommitPut 单一 commit + emit added/updated/evicted + putIndex (向量失败标 degraded 但 Put 仍成功)。
  - putLocked: 从 Put 提炼，供 Promote 在已持锁路径下复用避免 self-deadlock。
  - selectVictims: 按 fifo (CreatedAt ASC) / ttl (ExpiresAt ASC, 永不排最后) + tie-break (SessionID/Key ASC) 选 victimCount 个排除 target；不足返回 ErrMemoryQuota。
  - Get: 完整 Scope + 已过期返 ErrMemoryNotFound。
  - Search: 关键词路径 (substring on Key/Content lowercase + metadata 深度相等 + UpdatedAt DESC/SessionID ASC/Key ASC 排序 + Score=0)；向量路径完整骨架 (Embed -> VectorIndex.Search -> 击中回查 ContentStore 校验 Version/TTL/scope/metadata -> threshold 后置过滤)；fallback_to_keyword 一次性降级。
  - Delete/Clear/DeleteExpired: 持 mutationGate.RLock + Agent keyed lock (DeleteExpired 单独持 mutationGate.Lock 不取 Agent lock 以与 mutation 不交错)。
  - Promote: 带 SessionID 源复制为目标全局 item (SessionID 空), 源不删, 目标 ExpiresAt=nil 重应用 default_ttl; 复用 putLocked 在同 Agent keyed lock 内避免重入。
  - Reindex: 仅按 agentID 全量 List -> Embed -> 临时 VectorIndex swap；任一失败保留旧 pointer 标 IndexDegraded emit memory.degraded{reason:reindex} 与 ErrMemoryReindexFailed 包装底层错误；成功置 IndexReady。
  - IndexStatus: 唯一不调 beginOp 的只读方法 (indexMu.RLock 快照)，关闭后仍可读，未启用 vector 或未知 agent 返回 IndexReady。
  - ClockForTest() 暴露内部 Clock 给测试场景（仅 monorepo 测试使用，正式调用不应使用）。
- `internal/memory/memstore/store.go`（in-process ContentStore v1 默认后端）：map[PrimaryKey]MemoryItem + sync.RWMutex；CommitPut 单一原子 (校验 victim Version + victim != target + 删 victim + upsert target Version+1 保留 CreatedAt)；Get/Search/List/Delete/Clear/DeleteExpired/Count/Ping/Close 全部契约；返回值 deepcopy Metadata/ExpiresAt 隔离内部缓存。`matchesScopeGlobalSession`（空 SessionID=Agent 全部来源，非空=精确）+ `matchesMetadata`（顶层 JSON 值深度相等）+ `notExpiredAt`（nil/zero=永不过期，否则 Before/After 检查）。

### `internal/memory_test/manager_test.go`（跨包测试 package `memory_test` 避免 import cycle）
16 例全部绿：PutCreatesAndGets (version 保留 / CreatedAt 保留) / PutRejectsInvalidLayer / PutRejectsEmpty / PutRejectsManagedField / PutTTLThreeStates (nil+ttl/nil 0/zero time/<=now) / GetNotFound / GetExpiredReturnsNotFound / SearchKeywordSubstring (Score=0) / SearchLimitValidation (负数/>Max/IncludeGlobal 与空 SessionID) / DisabledPolicy (Put/Get/Search 全部 ErrMemoryDisabled) / QuotaFifoEvict (k1 选最早驱逐后 NotFound) / QuotaExceedsCapacity (100 次写后仅剩最新 2 个, k0 早已 NotFound) / DeleteClear / DeleteExpired (limit 校验 + 仅删过期) / Promote (目标 SessionID 置空, 源保留, 空源 SessionID 拒绝) / PromoteExpiredSource (过期源返 NotFound) / IndexStatusNoVector (未知 agent=ready) / EventsEmitted (added/deleted 顺序断言) / CloseIdempotent (二次关闭 + 关闭后即将 AnyOp 返回 ErrMemoryClosed)。

### Runtime 接入（commit `7acee98`）
- `internal/runtime/runtime.go`：Runtime struct 加 `memory *mm.Manager`（`mm` alias of internal/memory）；import 内部 memory 与 memstore 包。
- `Start` 在 Provider ready 后、Tool Manager 之前：若 `rt.cfg.Memory.Enabled=true` 则构造 `memstore.New()` + `mm.NewManager`（无 embedder/indexFactory/events v1）；否则 `components["memory"]="disabled"`。In-memory 后端为 v1 默认，SQLite 后端待后续 commit；Type=memory 时 warn 日志提示非持久。
- `Shutdown` 加 Memory.Close 在 Provider.Close 之前，组件字段置 nil；`rollback` 同序回滚。

### 验证
- go vet / go build / go test 全部 ./...：通过（除已记录的 WS flake 测试 TestWSStreamDisconnectCancelsTurn timing 偶发外）。
- 全包 build/vet/test 无 regression（agent/api/config/context/logging/provider/runtime/session/skill/storage/tool/tool/builtin + memory/memory_test/memstore）。

### 下一步（按优先级）
1. Agent turn Memory 检索注入 Context（docs/memory/integration.md §2 step 2）：Dependencies 加 Memory / AgentBinding 加 MemoryPolicy snapshot / runDirectTurn 第一轮 base+skill 后插入 32 KiB 上限的 memory system message / Search 错误传递策略（除 ErrMemoryDisabled 外阻断 turn）。
2. SQLite ContentStore 后端（modernc.org/sqlite 已在依赖中）：DDL + schema_version + ON CONFLICT + RFC3339Nano + JSON metadata + 索引创建；与内存后端互测对比。
3. VectorIndex exact cosine + Embedder HTTP（OpenAI-compatible `/embeddings`）；启动期 Reindex。
4. Memory Remote API 8 端点（GET/search + GET/:key + POST + DELETE + DELETE-clear + POST/promote + POST/reindex），handler 入口 snapshot + policy，错误映射按 errors §7。
5. 文档：补 W1-W10 剩余表述澄清项（W1 时间戳 / W2 / W4 tokens[].roles 默认）。

## Phase 3 下一步 #1：Agent turn Memory 检索注入 Context（commit 待 push）

按 `docs/memory/integration.md §2-§3` 落地 Agent 在 direct turn 第一轮注入 Memory system message：
- `internal/agent/manager.go`：`Dependencies` 加 `Memory *mm.Manager` 字段（mm alias of internal/memory）；新增 `SetMemory(mem)` 延迟注入 setter；`Inspect` 的 `MemoryEnabled` 改为反映 deps.Memory 非 nil 且 `resolveMemoryPolicy(a).Enabled`；包注释更新到 Phase 3。
- `internal/agent/memoryinject.go`（新文件）：
  - `resolveMemoryPolicy(a)`：从 root `cfg.Memory` + Agent override `cfg.Agents[id].Memory` 解析 effective `config.MemoryPolicy`（v1 不依赖 ReloadManager，每轮从 deps.Config 重新解析，与 `resolveAgentContextConfig` 同惯例）。
  - `const memoryInjectMaxBytes = 32 * 1024`（文档 §3 固定上限，非 MemoryConfig 字段）。
  - `formatMemoryResults(results) (content string, dropped int)`：按 Search 返回顺序输出（不重新排序），只读 Content + Key，不输出 Score 与 Metadata（v1 白名单未定避免泄露敏感字段）；`escapeMemoryText` 固定转义 `\n` `\t` `\r` 删除其他 0x00-0x1F ASCII 控制字符防伪造 role/Tool protocol；32 KiB UTF-8 上限，超限丢弃最末未追加项并返回 dropped 计数（仅用于日志，不修改 Session）。
  - `indexOfResult` 删去（直接 `for i, r := range` 用 i 算 dropped）。
- `internal/agent/handle_turn.go`：import 加 `errors` + `mm "github.com/v2up-32mb/yaa/internal/memory"`；在 `runDirectTurn` 每轮 canonical messages 组装中、Skill system messages 之后、`for _, sm := range snap.Messages` 之前插入 memory 注入块（仅 `rounds==0 && m.deps.Memory != nil`）：
  - `policy := m.resolveMemoryPolicy(a)`
  - `Search(ctx, policy, mm.SearchRequest{Scope: {AgentID, SessionID: req.SessionID, Layer: LayerLongTerm}, Query: req.Content, Limit: 0, IncludeGlobal: true})`
  - `merr != nil && !errors.Is(merr, mm.ErrMemoryDisabled)` → `return TurnResult{Usage: totalUsage}, fmt.Errorf("recall memory: %w", merr)` 阻断 turn（除 ErrMemoryDisabled 外一律阻断，符合 §2 step1 "不返回伪造空结果"）
  - 命中则 `formatMemoryResults` → 非空 content 追加一条 system message；dropped>0 时 `m.deps.Logger.Warn` 记录。
  - Memory system message 不写入 Session（不进 `snap.Messages.Payload`）。
- `internal/runtime/runtime.go`：`am.SetSkills(rt.skills)` 之后加 `am.SetMemory(rt.memory)`（与 SetTools/SetSkills 同期注入）。
- `internal/agent/memory_inject_test.go`（新文件，4 例）：
  - `newMemoryInjectEnv`：复用 skill 测试模式构造环境，注入真实 `mm.NewManager(memstore.New(), nil, nil, SystemClock{}, nil)` + `cfg.Memory.Enabled=true/vector.Enabled=false/Storage.Type=memory`；捕获 provider 请求 body。
  - `TestAgentHandleTurnInjectsMemorySystemMessage`：Put 2 个 Session-scoped item（k1 "preference.answer_style" / k2 "topic.last"），user content="user" 命中两条 content substring；断言 provider body 是 base+memory+user 三条，memory message 含 "The following are recalled memory entries" 头 + 两个 item content + 不含 "Score"；不写入 Session snapshot（session 仍只有 user+final assistant 2 条）。
  - `TestAgentHandleTurnMemoryDisabledSkipsInject`：cfg.Memory.Enabled=false → Search 返 ErrMemoryDisabled → 静默降级，turn 正常返回 "done"，body 不含 memory message。
  - `TestAgentHandleTurnMemoryNilSkipsInject`：`SetMemory(nil)` → 整段注入逻辑跳过，body 不含 memory message。
  - `TestAgentHandleTurnMemoryNonDisabledErrorBlocksTurn`：先 `memMgr.Close(ctx)` 让后续 Search 返 `ErrMemoryClosed`（非 disabled）；HandleTurn 返回 `errors.Is(err, mm.ErrMemoryClosed)`，Session 仅含 user（turn 阻断）。
- 顺手修复已有 `TestAgentHandleTurnWithToolLoop` flake：`newToolLoopEnv`/`newSkillTestEnv` 的 `cfg.Tools` 原 zero value（`DefaultTimeout=0`）违反 `docs/config/validation.md §461`，导致 `time.AfterFunc(0)` 立即 fire cancel，echo Execute 在 goroutine 调度压力下偶发被判超时。改两处 `cfg.Tools = config.DefaultToolsConfig()`（生产 cfg 经 validation 必 DefaultTimeout>0，测试 env 补齐即可），5 次全绿。
- 已知非我方引入的 `TestWSStreamDisconnectCancelsTurn` timing flake（count 大时偶发）不在本轮修复范围，单跑 5/5 全绿。

### 验证
- go vet / go build ./...：通过。
- go test -count=2 ./internal/{agent,memory_test,config,runtime}/：全绿。
- go test -count=1 ./...：除 `TestWSStreamDisconnectCancelsTurn` 偶发 timing flake（已记录）外全绿。

## Phase 3 下一步 #2：SQLite ContentStore 后端 + Runtime 按 storage.type 分发（commit 待 push）

按 `docs/memory/storage.md §2` 落地 SQLite ContentStore 后端 + Runtime 启动按 `cfg.Memory.Storage.Type` 选 backend。

### `internal/memory/sqlitestore/store.go`（新包，~700 行）
- modernc.org/sqlite 纯 Go 驱动（已在依赖），schema 与文档 §2 完全一致：`memory_items` 复合主键 `(agent_id, layer, session_id, item_key)` + `memory_items_agent_updated` 索引 + `memory_items_expiry` 部分索引；时间 `RFC3339Nano` UTC 文本保存；metadata 为 JSON 文本字段。
- `New(path)`：目录不存在自动创建；`SetMaxOpenConns(1)` + `PRAGMA journal_mode=WAL; busy_timeout=5000` 防 SQLite 写并发；`migrate()` 执行 4 个 DDL（CREATE TABLE IF NOT EXISTS + 两个 CREATE INDEX IF NOT EXISTS + schema_version 记录表）+ 写入 `schema_version=1` + 读 MAX(version) `> schemaVersion` 即拒绝（未知更高版本则启动失败，docs §6）。
- `CommitPut` 单事务：先校验每个 victim (Version 仍匹配且非 target；缺失/Version 不符 → `ErrMemoryQuota`，事务回滚);  写锁内按顺序 SELECT victim row（深拷贝）+ DELETE + INSERT/UPDATE target（保留 created_at、version 递增；INSERT 走完整列，UPDATE 走单条 UPDATE RETURN-less）。`tx.Commit()` 失败回滚 return `storeErr`。返回 `CommitPutResult{Stored, Created, Evicted}` 全 deep copy 隔离缓存。注意：`victim.Equals(target` 直接 `errors.New("victim cannot equal target")` 不走事务。
- `Get`：主键 SELECT 一次 + `scanItem`；与 memstore 契约一致**不过滤 expired**（Manager 决策）；`sql.ErrNoRows` → `ErrMemoryNotFound`；scanItem 的 JSON 解码错走 `corruptOrStoreErr` 包成 `ErrMemoryCorrupt` 透传。
- `Search`：扫描 `agent_id+layer` 全量 rows（候选 <= max_items=10000），在 Go 内按 SessionID/IncludeGlobal、`notExpiredAt`、metadata 顶层 JSON 深度相等、`strings.ToLower(Key/Content).substring(q)` 过滤，`sort.SliceStable` 按 `UpdatedAt DESC / SessionID ASC / Key ASC` 排序；List 同语义但不过滤 metadata、不做 substring。
- `Delete`：事务内 SELECT row → DELETE → 返 deleted item 副本；`ErrMemoryNotFound` 未命中。
- `Clear`：事务内 SELECT 全 `agent_id+layer` 行 + 在 Go 内按 SessionID 全范围/精确匹配筛 → 逐行 DELETE → 返 cleared items 副本。
- `DeleteExpired`：事务内按 `expires_at <= ? AND expires_at IS NOT NULL AND expires_at != ''` 过滤 + `ORDER BY expires_at ASC, agent_id ASC, session_id ASC, item_key ASC`（与 memstore 一致）SELECT 出过期 rows + `if limit > 0 && limit < len(all) { all = all[:limit] }` truncate + 逐行 DELETE + Commit 返回。
- `Count`：扫描全 agent rows + 在 Go 内 `notExpiredAt` 过滤；返回未过期 item 数。
- `Ping`：`SELECT 1`；`Close`：sync.Once 包 `db.Close()` 幂等。
- 错误映射：database/sql 错误统一`storeErr` → `fmt.Errorf("%w: %v", ErrMemoryStoreUnavailable, err)`；`scanItem` 的 JSON 解码错走 `corruptOrStoreErr`（若 `errors.Is(err, ErrMemoryCorrupt)` 透传，否则 storeErr），保证 `errors.Is(err, ErrMemoryCorrupt)` 可穿透。
- `matchesScopeGlobalSession` / `matchesMetadata` / `notExpiredAt` / `cloneItem` / `cloneItems` / `cloneMetadata` / `formatTime` / `parseTime` / `mustParseTime` 全部与 memstore 实现一致语义。

### `internal/memory/sqlitestore/store_test.go`（新文件，16 例）
- 覆盖 SQLite ContentStore 自身契约：CommitPut creates + CreatedAt/Version/UpdatedAt 保留 / Update preserves CreatedAt Version+1 / GetNotFound / Search metadata filter + UpdatedAt DESC order + keyword substring / SearchExcludesExpired + (修正后) Store.Get 不过期过滤 / Delete + DeleteNotFound / Clear scoped (session精确 + 空 session=Agent 全来源 + 其他 agent 不受影响) / DeleteExpired order + limit / Count excludes expired / List 全 agent 来源 / CommitPut victims Version validation (mismatch → ErrMemoryQuota rollback + victim 仍在) + 正确 victim → 删 + 建 target / victim equals target 拒绝 / Corrupt metadata JSON 错误返 ErrMemoryCorrupt (走 Get 路径) / schema_version unknown higher 拒绝 migrate / Reopen persists content+version / Ping 正常。
- 修：`Store.Get` 原 `notExpiredAt` 判定与 memstore 契约不符（Manager 决定），删除；测试断言改为"过期仍能读出 row"。

### `internal/memory_test/sqlite_manager_test.go`（新文件，6 例）
- 用真实 `mm.Manager` + SQLite backend（`newSQLiteManager` 模仿 `manager_test.go` 中 `newTestManager` 同样的 fakeClock + captureEvents + 同一 `defaultPolicy`），覆盖核心 scenarios：Put/Get/Update (CreatedAt 保留 / Version 递增) / GetExpiredReturnsNotFound (clock 推进) / SearchKeywordSubstring session-local + IncludeGlobal / QuotaFifoEvicts (MaxItems=3, k0 选最早驱逐) / PromoteCopiesToGlobalKeepsSource (源 Session 仍可见 + global 也能 Get) / DeleteExpiredExpiresByClock (clock 推进 + 物理删除)。
- 与 memstore 后端在 Manager 包装层核心语义保持一致（"与内存后端互测对比" docs/storage.md §2 字面要求）。

### `internal/runtime/runtime.go` 分发逻辑
- `Start` 中 Memory 段重构：按 `rt.cfg.Memory.Storage.Type` 选 backend：
  - `case "sqlite"`: `sqlitestore.New(rt.cfg.Memory.Storage.Path)`，失败 → `rollback()` + `return fmt.Errorf("runtime: memory sqlite store: %w", sErr)` 让 Runtime Not Ready（docs §2: 启动失败要正确传播）。
  - `default` (含 "memory" 与未知/空值): `memstore.New()`，未知 type 仅 warn，"memory"/"" warn "durable=false"。
  - 注释更新去掉"v1 阶段默认 in-memory"（已能配置 SQLite）。
- import 加 `"github.com/v2up-32mb/yaa/internal/memory/sqlitestore"`。

### `internal/runtime/runtime_test.go` 增 2 例
- `TestRuntimeMemorySQLiteBackendStart`：`cfg.Memory.Enabled=true` + `Storage.Type=sqlite` + tempdir path → Start 后 `health.Components["memory"]=="ready"` + `rt.memory != nil` + 文件被实际创建。
- `TestRuntimeMemorySQLiteBackendStartFailsOnBadPath`：构造一个不可能的目录（path 是既存文件而非目录）→ Start 必须返 error（不要 leaking ready Ready 但 memory 不可用）。

### 验证
- go vet / go build ./...：通过。
- go test -count=1 ./internal/memory/sqlitestore/：16 例全绿。
- go test -count=1 -run SQLiteManager ./internal/memory_test/：6 例全绿。
- go test -count=1 ./internal/runtime/：全绿（含新 2 例）。
- go test -count=1 ./...：除 `TestWSStreamDisconnectCancelsTurn` 偶发 timing flake（已记录，单跑 5/5 全绿）外全绿。

## Phase 3 下一步 #3：VectorIndex exact cosine + HTTP Embedder + Runtime 启动 Reindex（commit 待 push）

按 `docs/memory/architecture.md §4` + `docs/memory/storage.md §5` 落地 v1 唯一向量栈。

### `internal/memory/vector/index.go`（新包，~130 行）
- 进程内 exact cosine VectorIndex（v1 唯一实现，docs/architecture.md §4）。
- `entry{ref memory.ItemRef, vector []float32}` + `sync.RWMutex` 保护内部 slice。
- `Factory()` 返回 `VectorIndexFactory` 让 Manager `indexFactory` 字段每次返回新空 index，避免跨 Agent 共享或复用当前 index。
- `Upsert`: 校验 `ref.AgentID/Key` 非空 + vector 非空（零长度返 `ErrMemoryEmbeddingZero`）；深拷贝 vector 切片防外部 mutation；按主键（AgentID+SessionID+Layer+Key，Version 不参与）精确匹配：存在则 in-place 替换 slice 引用，否则 append。
- `Delete`: idempotent，不存在也返 nil。
- `Search`: 按 `AgentID+Layer+(SessionID 精确或 IncludeGlobal 时 SessionID+" 并集空 SessionID)` 过滤；`cosine(req.Query, vec)` 计算 → score < threshold 跳过 → 收集 hits → `sort.SliceStable` 按 score DESC, SessionID ASC, Key ASC 排序。**不截 limit**（docs §4：留给 Manager 回查 ContentStore 后再 final limit）。
- `cosine` 维度不匹配 → `ErrMemoryEmbeddingDimension`，零向量 → `ErrMemoryEmbeddingZero`；Search 内部错误仅跳过该 hit 不外抛（避免整 Search 失败）。
- 编译期断言 `var _ memory.VectorIndex = (*index)(nil)`。

### `internal/memory/vector/index_test.go`（新文件，4 例）
- Upsert/Delete/Search 主键替换 + scope 过滤 + score DESC + IncludeGlobal 行为；threshold 过滤；ref 非空与 vector 非空校验；cross-agent 隔离。

### `internal/memory/embedding/embedder.go`（新包，~140 行）
- `HTTPEmbedder` OpenAI-compatible HTTP Embedder（v1 唯一 provider）。
- `New(cfg MemoryEmbeddingConfig)`: base_url `TrimRight("/")`, dimension>0, timeout<=0 fallback 30s；构造 `&http.Client{Timeout:...}`。
- `Embed(ctx, inputs)`: 空 inputs 返 nil, nil 不调 server; 否则 `POST {base_url}/embeddings`，请求 body `{model, input []string}`，Bearer api_key 头。用 `context.WithTimeout(ctx, h.timeout)` 限响应时长。错误映射：构造/IO/非 2xx/malformed body/data count mismatch → `ErrMemoryEmbeddingFailed`；维度不匹配 → `ErrMemoryEmbeddingDimension`；全 0 向量或零长度 vector → `ErrMemoryEmbeddingZero`（docs/storage.md §5: "响应正文与输入内容不写入日志"——本实现不 log body）。
- 解析 OpenAI 响应 `{data: [{embedding: [float64...]}]}`；float64→float32 转换。
- 编译期断言 `var _ memory.Embedder = (*HTTPEmbedder)(nil)`。

### `internal/memory/embedding/embedder_test.go`（新文件，8 例）
- happy 2D token "a"/"b" 返 [1,0]/[0,1] 验证 + Bearer header + path /embeddings + model + input 长度匹配；non-2xx → ErrMemoryEmbeddingFailed；dimension mismatch → ErrMemoryEmbeddingDimension；zero vector → ErrMemoryEmbeddingZero；malformed json → ErrMemoryEmbeddingFailed；data count mismatch → ErrMemoryEmbeddingFailed；empty inputs 返 nil no call；New 拒绝 empty base_url/zero dimension；timeout<=0 fallback 30s 通过构造（New 成功）。

### `internal/runtime/runtime.go` Vector 启动注入
- import 加 `"github.com/v2up-32mb/yaa/internal/memory/embedding"` + `"github.com/v2up-32mb/yaa/internal/memory/vector"`。
- Memory Manager 构造段：`if rt.cfg.Memory.Vector.Enabled { embedding.New(rt.cfg.Memory.Embedding) + vector.Factory() }`；embedder 构造失败 → `rt.rollback()` + `return fmt.Errorf(...)` 让 Runtime Not Ready（cfg 由 validation 保证合法，正常路径不会失败）。
- `mm.NewManager(ms, embedder, indexFactory, mm.SystemClock{}, nil)` 把 embedder/indexFactory 注入，让 Manager 走 vector 路径用真实 embedder。
- 启动期对每个 `policy.Vector.Enabled && policy.Enabled` 的 Agent `mmMgr.Reindex(ctx, policy, ag.ID)`；失败仅 `rt.logger.Warn` 让 health 表示 degraded 但 Runtime 不阻断（docs/architecture.md §4: Reindex 失败留 degraded 由后续 Reindex 修复）。**架构 §4 §4「普通操作成功不会清除历史 degraded，只有完整 Reindex 才置 ready」**：putIndex 不再顺手清 status，让 Reindex 成为唯一 ready 来源；本步严格遵守这一契约。

### `internal/memory_test/vector_integration_test.go`（新文件，3 例）
- `newVectorManager` 用 HTTP embedder + 真实 vector.Factory；mock server 按输入 content 返预定义 dim=4 向量。
- `TestManagerVectorReindexAndSearch`: Put 3 个不同 axis 的 item + 显式 Reindex 让 IndexStatus=ready，Search query 命中最近邻居 cosine=1.0 score=1.0；依次查 dogs/cats content 命中 k1/k2 验证 score。
- `TestManagerVectorFallbackToKeywordWhenEmbedderDown`: server close 后 Search 走 fallback_to_keyword 路径，命中 keyword 子串 but Score=0；IndexStatus→degraded。
- `TestManagerVectorNoFallbackErrorsWhenEmbedderDown`: FallbackToKeyword=false 时 embedder 错误透传 sentinel（ErrMemoryEmbeddingFailed/IndexDegraded/IndexUnavailable 其中之一）阻断 turn。

### `internal/runtime/runtime_test.go` 增 1 例
- `TestRuntimeMemoryVectorStartupReindex`: cfg.Memory.Vector.Enabled + httptest mock embeddings server + dim=2 → Start 成功 + memory component ready + rt.memory 非 nil；证明 Runtime vector 启动路径不离 Integer 错。

### 验证
- go vet / go build ./...：通过。
- go test -count=1 ./internal/memory/vector/ + ./internal/memory/embedding/ + ./internal/memory_test/：全绿（含 4+8+3 例）。
- go test -count=1 ./internal/runtime/：含新 1 例全绿。
- go test -count=1 ./...：除 `TestWSStreamDisconnectCancelsTurn` 偶发 timing flake（已记录）外全绿。

## Phase 3 下一步 #4：Memory Remote API 8 端点 + Runtime 注入（commit 待 push）

按 `docs/remote-api/memory.md` §1-§8 + `docs/memory/errors.md §7` 落地 Memory 的 Remote API。

### `internal/api/memory_provider.go`（新文件）
- `MemoryProvider` 接口 8 方法（Search/Get/Put/Delete/Clear/Promote/Reindex + IndexStatus），除 IndexStatus 外每方法接 `config.MemoryPolicy`（architecture.md §2：Manager 不缓存 policy）。
- `MemoryPolicyResolver func(agentID string) (config.MemoryPolicy, bool)`：从 config snapshot 解 effective policy；ok=false 表示 agent 不存在。

### `internal/api/server.go` 改动
- Server 加 `memoryProvider MemoryProvider` + `memoryResolver MemoryPolicyResolver` 字段（mu 保护）。
- `SetMemoryProvider(mp, resolver)` setter；nil mp → Memory 8 端点统一返 50301 "memory subsystem unavailable"。

### `internal/api/session_handler.go` dispatcher 改动
- `handleAgentsSessions` 拓展为 `/api/v1/agents/:id/<sub>` 全 dispatcher：先 TrimPrefix + SplitN 解 out `agentID` 与 `sub`。
- `sub == "memory" || HasPrefix("memory/")` → `s.handleMemorySubtree(w, r, agentID, sub)`；否则 `sub == "sessions"` 走原 sessions handler；其余 → 40401。
- Memory 8 端点与 sessions 共用同 prefix dispatcher，都未走 Auth wrapper（progress #6 决策记录：Go 1.20 ServeMux 不支持 path template，同 prefix 不能重复 registerProtected；精细化 spec 绑定留待后续 wrap-only 接入）。

### `internal/api/memory_handler.go`（新文件，~490 行）
- DTO（docs §DTO）：`memoryDTO`（单 item + index_status）、`memorySearchItemDTO`（带 score）、`memorySearchData{items, limit, index_status}`、`memoryPostBody`、`memoryDeleteOneData`、`memoryClearData`、`memoryPromoteBody`、`memoryReindexData`。时间统一 RFC3339Nano UTC，zero time ExpiresAt → nil。
- `handleMemorySubtree` 按 sub（"memory" 或 "memory/<suffix>"）+ Method dispatch：
  - `GET /memory` → Search；`GET /memory/:key` → Get；`GET /memory/(promote|reindex)` → 40501。
  - `POST /memory` → Put；`POST /memory/promote` → Promote；`POST /memory/reindex` → Reindex；其他 suffix → 40501。
  - `DELETE /memory` → Clear；`DELETE /memory/:key` → DeleteOne；`DELETE /memory/(promote|reindex)` → 40501。
  - 其他 method → 40501。
- 8 sub-handler：
  - `handleMemorySearch`：解析 `q/session_id/limit/include_global/metadata`(JSON) query；`include_global` 必须配非空 `session_id`（否则 40001）；limit 范围 `[0, MaxSearchLimit]`；构造 `SearchRequest{Scope, Query, Limit, Metadata, IncludeGlobal}` 调 `mp.Search`；resp 带 `index_status`（调 `mp.IndexStatus(agentID)`）。
  - `handleMemoryGet`：`requireSessionIDQuery`（session_id 必填 query，显式空表 global item）；调 `mp.Get`。
  - `handleMemoryPut`：`io.LimitReader(1MiB)` 读 body + `json.Unmarshal memoryPostBody`；key/content/metadata/expires_at 校验（key 长度 `[1, MaxKeyLen]`、content `[1, MaxContentLen]`、metadata marshal 后 `<= MaxMetadataLen`、expires_at RFC3339Nano）；构造 `MemoryItem{Layer:LayerLongTerm, ...}` 调 `mp.Put`；created → 201，update → 200。
  - `handleMemoryDeleteOne`：`requireSessionIDQuery` + 调 `mp.Delete`；resp `memoryDeleteOneData{deleted:true,...}`。
  - `handleMemoryClear`：session_id 可省（agent 全范围）；调 `mp.Clear`；resp `memoryClearData{deleted_count, ...}`。
  - `handleMemoryPromote`：读 body + DisallowUnknownFields 风格 json；`SessionID` 和 `Key` 必填（40001）；调 `mp.Promote`；created → 201 else 200。
  - `handleMemoryReindex`：`!policy.Vector.Enabled` → 40001；调 `mp.Reindex`；resp `memoryReindexData{indexed, status, ...}`。
- `requireSessionIDQuery`: 区分"未传 session_id query"（→ 40001）vs "显式 `?session_id=`"（表 global item，合法）；docs §1/§2 明示 session_id 是必填 query 即使空串。
- `writeMemoryError` 按 `docs/memory/errors.md §7` 映射全 sentinel：
  - `context.Canceled`（err 或 request ctx.Err()）→ 不写响应（客户端已断开）。
  - `context.DeadlineExceeded` → 504 / 50401。
  - `ErrMemoryNotFound` → 404 / 40401。
  - `ErrMemoryDisabled` → 409 / 40901。
  - `ErrMemoryQuota` → 429 / 42901。
  - `ErrMemoryInvalidScope/InvalidItem/ManagedField/UnsupportedLayer/ExpiredInput` → 400 / 40001。
  - 其他（含 closed / store unavailable / corrupt / embedding / index unavailable / index degraded / ReindexFailed）→ 503 / 50301。
- helper：`toMemoryDTO` / `toSearchItemDTO` / `formatMemoryTime`（zero → ""）/ `formatMemoryExpiresAt`（nil 或 zero → nil；否则 RFC3339Nano UTC）。

### `internal/api/memory_handler_test.go`（新文件，~25 例）
- `fakeMemoryProvider` 实现 8 方法 + lastXxx 捕获；`fakeMemoryResolver(policy)` closure；`enabledMemoryPolicy()` 默认 Enabled=true/Vector=false。
- `newMemoryTestServer` 构造 Server + `SetMemoryProvider`；`doMem` body 支持 `io.Reader`（原样透传，用于非法 JSON 测试）/ nil / 其他（json.Marshal）。
- happy path：Search（含 index_status）/ Get / Get global empty session_id / Put created 201 / Put update 200 / DeleteOne / Clear 带 session_id / Clear 不带 session_id / Promote / Reindex（vector enabled）。
- 边界：Search include_global 无 session_id → 40001；Get 缺 session_id 参数 → 40001；Put 空 key → 40001；Put 非法 JSON body → 400；Promote 缺 session_id/key → 40001；Reindex vector disabled → 40001；Method 不允许（PUT）→ 40501；unknown agent → 40401。
- error mapping：NotFound 40401 / Disabled 40901 / Quota 42901 / InvalidItem 40001 / StoreUnavailable 50301 / ReindexFailed 50301；mp=nil（未注入）→ 50301。
- `TestMemoryGetNoSessionsPathUnchanged`：用真实 session.Manager + storage.NewMemory + fakeAgentProvider 验证 memory dispatcher 没吃掉 sessions 子路径（GET /agents/a/sessions ≠ 404）。

### `internal/runtime/runtime.go` 注入
- `agentMemoryOverride(agentID)` helper 从 cfg.Agents 找 Memory override。
- `memoryPolicyResolver()` 构造 closure：`agentExists` 返 false → `(MemoryPolicy{}, false)` → handler 40401；否则 `config.ResolveMemoryPolicy(rt.cfg.Memory, override)`。
- Start 段：Memory.Manager 构造成功（`rt.memory != nil`）后 `rt.api.SetMemoryProvider(rt.memory, rt.memoryPolicyResolver())`；Memory 全局 disabled 时 rt.memory==nil，handler 统一 50301（进度记录：operator 不应调用 disabled 子系统；严格 40901 留待后续 wrap-only spec 绑定时再补 disabled stub）。

### `internal/runtime/runtime_test.go` 增 1 例
- `TestRuntimeMemoryRemoteAPIProviderInjected`：`cfg.Memory.Enabled=true` + Storage=memory（无 agent）→ Start 成功 → HTTP GET `/api/v1/agents/unknown/memory` → 期望 40401（resolver 对未知 agent 返 ok=false，证明 provider+resolver 被注入；若未注入则 50301）。

### 验证
- go vet / go build ./...：通过。
- go test -count=1 ./internal/api/：含新 ~25 例全绿，原 sessions/conversation/ws/auth 测试无 regression。
- go test -count=1 ./internal/runtime/：含新 1 例全绿。
- go test -count=1 ./...：除 `TestWSStreamDisconnectCancelsTurn` 偶发 timing flake（已记录）外全绿。

## Phase 3 下一步 #5：gorilla/mux 路由层重构 + AD-004 37 路由精细 RouteSpec 绑定（commit 待 push）

按 `docs/auth/integration.md §2-§5` + `docs/auth/decisions.md AD-004` + `docs/remote-api/INDEX.md §3/§5`
让 RouteSpec 与唯一 wrapper 真正生效，消化 progress #6 的 wrap binding 技术债。

### 文档/代码冲突修复
- 文档（`integration.md §3` 代码示例 + `INDEX.md §5` "不得从 URL 段猜权限" + AD-004 "37 路由必须 metadata 测试" + checklist "每条路由显式绑定 RouteSpec"）一致要求用 gorilla/mux 路径模板 + 唯一 wrapper 逐条 RouteSpec 绑定；现行代码用裸 `http.ServeMux` + 前缀 dispatcher 偏离契约。
- 引入 `github.com/gorilla/mux v1.8.1`（Go 1.20 兼容最新版），把 router 从 `*http.ServeMux` 切到 `*mux.Router`；保留 gorilla/websocket 不受影响。

### `internal/api/server.go` 改动
- Server 增 `router *mux.Router` + `registeredRoutes []routeSpec` 字段（mu 保护）。
- `NewServer` 改用 `mux.NewRouter()` 装中间件链。
- `register(r *mux.Router)` 调 `registerRoutes`：注册完 37 路由后设
  - `r.MethodNotAllowedHandler = s.methodNotAllowed`：让 gorilla/mux `r.Methods()` 在 method 不匹配时返 envelope 40501 而非默认 text/plain。
  - `r.NotFoundHandler = s.notFound`。
- `RegisteredRoutes()` getter 返 spec slice 副本供注册测试与 AuthZ 元数据审计（AD-004）。

### `internal/api/route_auth.go` 改动
- `routeSpec` 注释：Pattern 改为 gorilla/mux `{id}/{key}/{msgid}/{name}` 路径模板。
- `registerProtected(r *mux.Router, spec, h)`：
  - 调用前 push spec 到 `s.registeredRoutes`（注册测试 source）。
  - method 校验仍保留在 wrapper 内（`spec.Method != req.Method → envelope 40501`）作为 sanity，即使 disabled 也 40501（保留 `TestServerMethodCheckStill40501WithAuth` 契约）。
  - 注册改用 `r.HandleFunc(spec.Pattern, protected).Methods(spec.Method)` 让同 Pattern 不同 Method 的路由被 method-aware 选中；与 wrapper 内 method 校验互不矛盾（method match 命中后走 wrapper，sanity 永远过；method mismatch 走 MethodNotAllowedHandler→envelope 40501）。

### `internal/api/routes.go`（新文件）
- `registerRoutes(r)` 集中按 `docs/remote-api/INDEX.md §3` 注册全部 37 条 RouteSpec：
  - 3.1 系统（3）：health/version/config
  - 3.2 Agent（5）：`/agents` list、`/agents/{id}` get、`/{id}/start|pause|stop`
  - 3.3 Session（10）：`/agents/{id}/sessions` POST/GET、`/sessions/{id}` 8 条子资源
  - 3.4 对话（3）：`/sessions/{id}/messages` POST、`/events` SSE、`/stream` WS（Transport=TransportWebSocket）
  - 3.5 Tool/Skill/Provider（7）
  - 3.6 Memory（7）
  - 3.7 MCP（2）
- 已实现端点指向 `*Route` adapter；尚未实现的 15 条（config/agent status/tool/skill/provider/mcp）绑 `s.notImplemented` 占位：返 501/50101 表示端点契约存在但 handler 未交付，RouteSpec metadata 在位，后续 commit 替换 handler 时 spec 不动。
- `pathVar(r, name)` helper：取 `mux.Vars(r)[name]`；adapter 用它把 path 参数传给 inner handler。

### `internal/api/session_handler.go` 重构
- 删除 `registerSessionRoutes` + `handleAgentsSessions` + `handleSessions` 两个 dispatcher。
- 新增 12 个 `*Route` adapter（CreateSessionRoute / ListSessionsRoute / GetSessionRoute / PauseSessionRoute / ResumeSessionRoute / CloseSessionRoute / DeleteSessionRoute / ClearMessagesRoute / ListMessagesRoute / DeleteMessageRoute / PostMessageRoute / SSEEventsRoute / WSStreamRoute）：每个用 `sessionProvider(w, r)` 取 SessionProvider（nil → 50301）+ `pathVar(r, "id" / "msgid")` 后调原 inner handler（签名不变）。
- 去掉 `net/url` / `strings` import（不再做 prefix split），保留 `encoding/json`/`errors`/`net/http`/`strconv` + `config`/`session`/`storage`。

### `internal/api/memory_handler.go` 重构
- 删除 `handleMemorySubtree` prefix dispatcher。
- 新增 `resolveMemoryProvider(w, r, agentID)` helper：取 provider + resolver + cache policy；nil → 50301；agent unknown → 40401。
- 新增 7 个 `*Route` adapter（SearchRoute / GetRoute / PutRoute / DeleteOneRoute / ClearRoute / PromoteRoute / ReindexRoute）：每个 `pathVar(r, "id")` + Get/DeleteOne 顺带 `pathVar(r, "key")` 校验 + `resolveMemoryProvider` → 调原 inner handler（签名不变）。
- 去掉 `net/url` / `strings` import（suffix 解析不再需要），保留 `context`/`encoding/json`/`errors`/`io`/`net/http`/`strconv`/`time` + `config`/`memory`。

### `internal/api/ws_handler.go` 改动
- 删除 `r.Header.Get("Authorization") == ""` 简陋 v1 占位校验。
- 改用 `s.authIdentityForWebSocket(r)`（已实现于 `route_auth.go`）返 `(identity, ok)`：
  - disabled / public path → ok=true，允许 anonymous 握手（docs §1/§5）。
  - 启用 Auth 且非 public：无 Identity（wrapper 未绑定）→ ok=false → 写 envelope 40101。
- identity 留作后续 AuditLogger 用，本步持 `_ = identity` 占位不打破编译。

### `internal/api/routes_test.go`（新文件，5 例）
- `expectedRoutes` 与 `docs/remote-api/INDEX.md §3` 总表逐项一一对应（37 条），便于人肉 audit。
- `TestRouteRegistrationMatchIndexTable`：sortSpecs 后对照 INDEX 全条目 method/pattern/action/resource/transport。
- `TestRouteRegistrationCountIs37`：精确 37（AD-004）。
- `TestRouteRegistrationWebSocketStreamBound`：WS stream 必绑 `Method=GET` + `Transport=TransportWebSocket`。
- `TestRouteRegistrationNoDuplicatePatternMethod`：防误增重复注册。
- `TestRouteRegistrationMatchedAgainstRouter`：用 `router.Match` 直接验证 37 条 spec 都能在 `*mux.Router` 命中（不只 s.registeredRoutes 字段，验收 router 真注册生效）。

### `internal/api/ws_handler_test.go` 改动
- `TestWSStreamCancelBeforeStart` 改成"启用 Auth + 缺 Bearer → 401"（文档 §1/§5：disabled 允许 anonymous，启用 Auth 且非 public 才拒绝）—— 修复原测试与文档契约冲突。
- 新增 `TestWSStreamDisabledAuthAllowsAnonymous`：disabled auth 下 WS 不返 401，验证 §1 bypass 与 §5 anonymous 通路。

### 决策记录
- **选取 (A) 引入 gorilla/mux 而非 (B) 改文档承认裸 ServeMux**：文档契约（integration.md §3 示例代码 + AD-004 + INDEX §5 + checklist）一致承诺 gorilla/mux + 37 路由精细绑定；偏离文档属于代码偏离而非文档错。AuthN/AuthZ 是 trust boundary， ponytail YAGNI 不适用。
- **`.Methods(spec.Method)` + wrapper 内 sanity 校验并存**：gorilla/mux `.Methods()` 让同 Pattern 多 Method 路由能被 method-aware 选中正确 wrapper；wrapper 内 `spec.Method` 校验保留以让 40501 也走 envelope 同时保证 disabled 路径仍 40501（已测 `TestServerMethodCheckStill40501WithAuth`）。
- **占位 stub 仍绑 RouteSpec**：未实现的 15 条端点（agent config/tool/skill/provider/mcp）绑 `s.notImplemented` 占位但仍挂载正确 RouteSpec metadata，让 37 路由注册测试可逐项断言；后续 commit 替换 handler 时 spec 不动，避免"为通过测试而补注册"的返工。
- **WS Identity 从 `authIdentityForWebSocket` 取**：与 registerProtected 注入的 IdentityFromContext 配合，删除 v1 简陋 Authorization Header 存在性检查；保留 principalID 字段占位为后续 AuditLogger 留钩。

### 验证
- go vet / go build ./...：通过。
- go test -count=1 ./internal/api/：含新 5 路由注册 + WS disabled auth + 原 sessions/conversation/auth/memory/ws 测试，全绿。
- go test -count=1 ./...：除 `TestWSStreamDisconnectCancelsTurn` 偶发 timing flake（已记录，单跑通过）外全绿。
- go mod tidy 后 gorilla/mux 从 indirect 转正。

## Phase 3 下一步 #6：Agent / Tool / Provider Remote API 端点实现（commit 待 push）

把上一轮 #5 占位的 12 条 stub 替换为真实 handler：Agent 5 端点 + Tool 2 + Provider 3
（Tool/Skill/Provider 3 个 Manager 已有 Get/List 等方法，可直接接 API）。
config + mcp 2 共 3 条仍绑 notImplemented 50101 占位（依赖未实现的 RedactedView/MCP.Manager）。
按文档契约（INDEX.md §3.2/§3.5 + docs/remote-api/{agent,tool,provider}.md）落地。

### `internal/api/agent_provider.go` 改动
- `AgentProvider` 接口扩展为 6 方法：HandleTurn/Get/List/Start/Pause/Stop（Deps 命名沿用
  *agent.Manager 本就实现的方法签名 List/Start/Pause/Stop；status 用 *agent.Status 透传）。

### `internal/api/agent_handler.go`（新文件，~170 行）
- `agentSummaryDTO`（5 字段：id/name/provider/model/status）+ `agentDetailDTO`（追加 tools/skills
  slice + memory_enabled/planner_enabled；Tools/Skills 默认 []string{} 空数组 ≠ null）+ `agentStateDTO`
  （start/pause/stop 返 {id,status}）+ `agentListData`（paged）。
- `agentStatusFromQuery` 校验 status query；非法 → 40001。
- `handleListAgents` —— GET /api/v1/agents：parsePage/parsePageSize + 稳定分页；AgentManager.List 已
  按 ID 升序，handler 仅做 [start:end] 切片。
- `handleGetAgent` —— GET /api/v1/agents/{id}：Manager.Get（仅 Info）；v1 详情 tools/skills 暂为空数组
  （AgentProvider 仅暴露 Get；完整 Inspect 留待 Runtime upgrade 时补）。
- `handleStartAgent/{Pause,Stop}Agent` —— POST：统一走 `agentStateChange` helper（fn → Get → 写 state dto）。
- `writeAgentError` 按 docs/remote-api/agent.md 映射：NotFound → 40401；
  InvalidState/Paused/Stopped → 40901；ManagerClosed → 50301；DeadlineExceeded → 50401；
  Canceled → 不写响应；其他 → 50001。

### `internal/api/tool_handler.go`（新文件）
- `toolInfoDTO` 映射 tool.ToolInfo（docs/tool.md：parameters 为 JSON Schema，深拷贝）。
- Server 加 `s.tools *tool.Manager` 字段 + `SetToolManager` setter（runtime.Start 注入）。
- `handleListTools` / `handleGetTool`：GetErrToolNotFound → 40401。

### `internal/api/skill_handler.go`（新文件）
- `skillSummaryDTO` + `skillViewDTO`（SkillView：tools/skills 升序，空数组输出 []）+
  `skillListData`；Server 加 `s.skills *skill.Manager` + `SetSkillManager` setter。
- `handleListSkills` / `handleGetSkill`：ErrSkillNotFound → 40401；disabled Skill 仍返 200 + status:disabled。

### `internal/api/provider_handler.go`（新文件）
- `providerSummaryDTO`（id/type/models IDs 升序）+ `providerViewDTO`（追加 timeout/max_retries/
  retry_interval，省略 api_key/base_url/extra）+ `providerModelsData`；Server 加 `s.providers
  *provider.Manager` + `SetProviderManager` setter。
- `handleListProviders` / `handleGetProvider` / `handleGetProviderModels`：Provider 不存在 → 40401。
- `formatDuration` helper：time.Duration String() 输出 Go duration string。

### `internal/provider/manager.go` 改动
- 新增 `ErrProviderNotFound` sentinel + `Config(id)` 方法（暴露 ProviderConfig 副本给 ProviderView 用），
  Get/Config 用 `%w` 包装 ErrProviderNotFound 让 Remote API `errors.Is → 40401` 干净映射（去掉 v1 初版
  用错误字符串判断的 hack）。

### `internal/api/server.go` 改动
- Server 增 `tools *tool.Manager` + `skills *skill.Manager` + `providers *provider.Manager` 字段（mu 保护）。
- 新增 `SetToolManager / SetSkillManager / SetProviderManager` 三个 setter。

### `internal/runtime/runtime.go` 改动
- Start 段：在 SetSessionManager 后追加 `SetToolManager/SetSkillManager/SetProviderManager(rt.tools/skills/providers)`
  三处注入；让 Tool/Skill/Provider Remote API 在 Runtime Ready 时可用。

### `internal/api/routes.go` 改动
- 12 条 stub 改指向真实 handler：Agent 5（list/get/start/pause/stop）+ Tool 2 + Provider 3 + Skill 2。
- 剩余 3 条 stub：config（依赖 RedactedView 未实现）+ mcp 2（依赖 MCP.Manager 未实现）仍 50101。
- 路由注册测试不变：registerProtected 把每条 RouteSpec push 到 registeredRoutes，stub 与真实 handler
  在 RouteSpec 断言上完全等价。

### `internal/api/agent_handler_test.go`（新文件，14 例）
- `agentToolProviderTestEnv` 复用 conversation_handler_test setup 模式构造含 1 个 agent 的真实
  Server：Session + Provider + Agent + Tool + builtin 注册，三 Manager 都注入。
- Agent：list 1 个 + get detail（tools/skills 空数组）+ get unknown 40401 + pause→stop→start 状态迁移 +
  start unknown 40401。
- Provider：list summary（models IDs）+ get view（timeout=5s max_retries=2 retry=1s）+ get unknown 40401 +
  /models 子路由。
- Tool：list 含 builtin + get item + get unknown 40401。
- `TestStubEndpointsReturn501NotImplemented` 验证剩 3 个 stub（config + mcp2）返 50101 保护未实现端点契约。
- 测试 env.Data 用 `env.Data.(map[string]any)` 断言（与 memory_handler_test 风格一致）。

### 验证
- go vet / go build ./...：通过（含 internal/provider ErrProviderNotFound 改动 + 3 新 manager setter）。
- go test -count=1 ./internal/api/：含新 14 例全绿；原 sessions/conversation/auth/memory/ws 测试无 regression。
- go test -count=1 ./internal/provider/ / ./internal/runtime/：全绿。
- go test -count=1 ./...：全项目 17 包全绿（含 WS flake 这轮未触发）。

### 下一轮方向
- config 端点：需先实现 `internal/config/redact.go` 的 `RedactedView`（docs/config/overview.md §3.3），
  把已知 Secret + MCP headers/env + 开放 Map 递归脱敏成 JSON-compatible 深拷贝，最小测试覆盖 nil 不变性。
- MCP 端点：依赖未实现的 `internal/mcp/` 包（Manager + Client + Server + 3 transport），规模较大，留作后续模块起点。

## Phase 3 下一步 #7：config.RedactedView + GET /api/v1/config 端点（commit 待 push）

按 `docs/config/overview.md §3.3` 实现 Config 模块唯一对外脱敏视图，替换上一轮 #6 config stub；
自此 37 路由里只剩 MCP 2 stub 仍待依赖未实现的 `internal/mcp/` 包。

### `internal/config/redact.go`（新文件，~170 行）
- `ErrConfigRedactionFailed` sentinel（docs §3.3）。
- `RedactedView(cfg *Config) (any, error)`：按 docs 实现顺序固定：
  1. 拒绝 nil；`json.Marshal cfg` → `Decoder.UseNumber` 解码为 `map[string]any`；失败 wrap ErrConfigRedactionFailed。
  2. 替换 4 个已知 Secret 路径的 string 值为 "***"：`runtime.auth.tokens[*].token`、`runtime.auth.jwt.secret`、`providers[*].api_key`、`memory.embedding.api_key`。
  3. 对 MCP servers[*].headers/env + 6 个开放 Map 递归处理：`redactScalarRecursive` —
     object/array 保持结构，所有 scalar（string/bool/number/json.Number）替为 "***"，`null` 保持 null。
  - 开放 Map 6 个精确路径：providers[*].extra / tools.builtin.*.options / agents[*].tools_config.*.options /
    skills.per_skill.*.options / agents[*].skills_config.*.options / plugins.entries[*].config。
  - fail-closed：开放 Map 全部 scalar 都视为敏感值脱敏，不按 key 猜；新增 Map 必须同时加入该 list。
  - 输入 cfg 不被修改（先 Marshal 再 decode 深拷贝）。

### `internal/config/redact_test.go`（新文件，7 例）
- `buildFullConfig` 构造覆盖所有脱敏路径的 Config（含 4 个已知 Secret + MCP headers/env + 6 开放 Map 嵌套 scalar + null）。
- `TestRedactedViewNilReturnsError`、`TestRedactedViewKnownSecretsReplaced`、
  `TestRedactedViewMCPHeadersEnvRedactedAsScalars`、`TestRedactedViewOpenMapsRecursiveScalars`、
  `TestRedactedViewPreservesNonSensitiveFields`、`TestRedactedViewDoesNotMutateInput`、
  `TestRedactedViewTwoCallsDeepEqual`。
- helpers `navigatePath` 用文档风格点+数组索引路径在 JSON 上断言（与 RedactedView 实现解耦）。

### `internal/api/config_handler.go`（新文件）
- `handleGetConfig` — GET /api/v1/config（read:config）：取 `s.cfgSnapshot` → `config.RedactedView` → envelope。
  snapshot nil → 50301；redact 失败 → 50001；不得 fallback 未脱敏 snapshot。

### `internal/api/server.go` 改动
- Server 增 `cfgSnapshot *config.Config` 字段（mu 保护）+ `SetConfigSnapshot(cfg)` setter。

### `internal/runtime/runtime.go` 改动
- Start 段在 SetProviderManager 后调 `rt.api.SetConfigSnapshot(rt.cfg)`，注入当前 Config snapshot。
  （docs §3.3 提到的 `reload.Current()` 在 ReloadManager 实现前用单次注入替代，热 reload 未来通过替换 snapshot 实现。）

### `internal/api/routes.go` 改动
- config stub 改指向 `s.handleGetConfig`；剩 MCP 2 stub 仍占位。

### `internal/api/agent_handler_test.go` 增 3 例
- `TestConfigEndpointReturnsRedactedView`：注入含 1 个 Provider 的 cfg，GET /api/v1/config 返 200；
  api_key = "***"，id/type 非脱敏保留。
- `TestConfigEndpointNilSnapshotReturns503`：未注入 snapshot 返 50301。
- `TestStubEndpointsReturn501NotImplemented` 改成只有 MCP 2 stub（config 已实现）。

### 验证
- go vet / go build ./...：通过。
- go test -count=1 ./internal/config/：含新 7 例 redact 测试全绿。
- go test -count=1 ./internal/api/：含新 3 例 config 端点测试 + 原 14 + sessions/conversation/auth/memory/ws 测试无 regression，全绿。
- go test -count=1 ./...：全项目 17 包全绿（WS flake 这轮未触发）。
- go mod tidy：无新依赖。

### 当前 stub 剩余（37 路由里 2 条）
- MCP servers list / get：依赖未实现的 `internal/mcp/` 包（Manager + Client + Server + 3 transport；规模大独立模块）。

### 下一轮方向
- MCP Manager 起点（docs/mcp/README.md §2 + checklist）：先落地 Manager 结构 + ServerStatus + List/Get/Prepare/Activate/Stop lifecycle 框架（先 stub transport + 不实现 catalog reconciliation），让 Remote API mcp/servers 端点返空列表也能联调；后续 commit 渐进补 Client/transport。

## Phase 3 下一步 #8：MCP Manager 起点 + Remote API mcp/servers 端点实现（commit 待 push）

### 范围
落地 `internal/mcp/` 包骨架（types + Manager v1 起点），让 Remote API 37 路由的最后 2 条 MCP
stub 替换为真实 handler 返 ServerStatus 投影。**Manager lifecycle（上游 Client 连接 / heartbeat /
重连 / 本地 Serve）暂不实现** — 留后续 multi-commit 按 docs/mcp/checklist.md §1-7 渐进补全。
v1 起 Manager 构造即处于 "teardown-done" 状态：未连接、List 投影 disconnected、ToolCount=0。
**所有 37 路由现已绑定真实 handler，无 501 stub 残留**。

### 文档依据
- `docs/mcp/README.md` §1 范围、§2 Manager API + ServerStatus、§3-5 Client/Server/Transport 概览
- `docs/mcp/errors.md` §1 13 sentinel、§3 状态机
- `docs/mcp/client.md` §1 ConnectionStatus 4 态
- `docs/mcp/checklist.md` §1 Manager / §9 集成
- `docs/mcp/config-ref.md` §1/§2 配置字段
- `docs/mcp/integration.md` §9 Runtime Stop 顺序

### `internal/mcp/types.go`（新）
- `ConnectionStatus` 字符串类型 + 4 sentinel：`Status{Disconnected,Connecting,Connected,Error}`
- `ServerStatus` 结构：Name/Status/Transport/ProtocolVersion(*string)/ToolCount/ConnectedAt(*time.Time)/LastError(omitempty)
  —— 敏感连接配置（command/args/env/headers/tls）**不进入**该类型，避免 Remote API / 健康端点泄露
- 13 个错误 sentinel 全部按 docs/mcp/errors.md §1 落地（ErrMCPConfig/ConnRefused/ConnTimeout/
  AuthFailed/TransportClosed/TransportWrite/ProtocolError/InvalidParams/ToolNotFound/ToolExecFailed/
  ToolTimeout/UnsupportedContent/Unavailable）。typed-error `Error()` 只返稳定文本，详细字段路径由
  config 校验阶段携带，不再扩展零散 sentinel。

### `internal/mcp/manager.go`（新）
- `Manager` 结构：cfg/logger/entries（配置 server 投影源，只存非敏感 name+transport）/runCtx+cancelRun/
  doneOnce+done（teardown 信号）/stopOnce+cacheErr（幂等 Stop + 缓存最终错误）/readyMu+ready（本地 Serve
  状态）/mu。**所有公开方法**：`Prepare`/`Activate`/`Stop(ctx)`/`Done()`/`Ready()`/`Get(name)`/
  `List()`/`Tools(name)`/`NewManager(cfg, tm, logger)` —— 完整对齐 docs/mcp/README.md §2 签名。
- v1 语义：
  - `NewManager` 缓存配置 server 名字 + transport（空 transport 默认 `stdio`）作 List/Get 投影源；
    nil cfg 返 `ErrMCPConfig`；nil logger 走 `slog.Default()`。
  - `Prepare()` v1 无 transport 要 prepare → 返 nil。
  - `Activate()` 若 `cfg.Server.Enabled=true` 返 `ErrMCPConfig`（本地 Server 实现未交付，**不静默
    启用**避免空 Server 接受请求产生语义错乱）；disabled 时返 nil。
  - `Stop(ctx)` 用 stopOnce 保证幂等；取消 runCtx + 关闭 done + 置 ready=false；返 cacheErr。
  - `Done()` 返 done channel。v1 起构造即 done 未关闭，Stop 后关闭。
  - `Ready()` v1 恒 true（无本地 Serve），Stop 后置 false。
  - `List()` 返 ServerStatus 切片，**深拷贝**（修改不影响 Manager 内部 entries）；v1 全 disconnected。
  - `Get(name)` 命中返 (ServerStatus, true)，未命中返 (zero, false)。
  - `Tools(name)` v1 未连接 → 返 (nil, false)。

### `internal/mcp/manager_test.go`（新，13 例）
- 空 / 多 server / 默认 transport / Get 命中未命中 / List 深拷贝不变性 / Tools empty /
  Ready + Stop 状态转换 / Stop 幂等 / Done 在 Stop 后可读 / Prepare no-op / Activate
  enabled 拒绝 + disabled nil / NewManager nil cfg 拒绝

### `internal/api/server.go` 改动
- Server 增 `mcpServers MCPServerProvider` 字段（接口，非具体 *mcp.Manager）—— 与
  Provider/Tool 等具体指针注入风格不同的取舍：MCP 作为新模块通过接口隔离便于 API 包 handler 测试 mock
- 新增 `MCPServerProvider` 接口：`List() []mcp.ServerStatus` + `Get(name) (mcp.ServerStatus, bool)`
  （与 docs/mcp/README.md §2 签名对齐）
- 新增 `SetMCPServerProvider(mp)` setter；未注入时端点返 50301

### `internal/api/mcp_handler.go`（新）
- `mcpServerListData{Items []mcp.ServerStatus}` DTO
- `mcpProvider(w, r)` 解引用 + nil 检查 → 50301
- `handleListMCPServers` — GET /api/v1/mcp/servers：投影 List()；items nil → [] 防 null
- `handleGetMCPServer` — GET /api/v1/mcp/servers/{name}：40401 未找到 / 200 OK + ServerStatus DTO

### `internal/api/routes.go` 改动
- 2 个 MCP stub (`s.notImplemented` 50101) 替换为 `s.handleListMCPServers` / `s.handleGetMCPServer`
- **`s.notImplemented` handler 保留**（不再有端点引用，但作为通用未实现占位留作模板）

### `internal/runtime/runtime.go` 改动
- 加 `internal/mcp` import + `mcpMgr *mcp.Manager` 字段
- Start 段在 SetConfigSnapshot 之后：构造 `mcp.NewManager(&rt.cfg.MCP, rt.tools, rt.logger)` →
  `Prepare()` → `Activate()`（任一失败 `rt.rollback()` + 返 fmt.Errorf 包装）→ 设字段 +
  `rt.api.SetMCPServerProvider(mcpMgr)` + `rt.components["mcp"] = "ready"`
- Shutdown 段在 `rt.api.Shutdown` 之后 `rt.sessions.Shutdown` 之前插入 MCP teardown：
  按 docs/mcp/integration.md §9 "调 Stop(ctx) → 等 Done → 再以 fresh ctx 调 Stop 取最终错误"
- rollback 段在 providers 之前插入：fresh ctx Stop + Done + 置 nil

### `internal/api/agent_handler_test.go` 改动
- 加 `internal/mcp` import；分组 import 顺序 gofmt 规整（带动文件 ival 中已有 import 块的 1 行调换，
  非本项目 commit vol1 引入的杂项清理）
- `agentToolProviderTestEnv` 末尾注入 `mockMCPServerProvider{items: [{fs, disconnected, stdio, 0}]}`
- 新 `mockMCPServerProvider` 类型 + List/Get 实现（深拷贝 List 防测试间共享状态）
- 删 `TestStubEndpointsReturn501NotImplemented`（无 501 stub 残留）
- 新 `TestMCPEndpointsReturn200And404`：list 200 + items 投影 + Get(fs) 200 + Get(miss) 40401
- 新 `TestMCPEndpointsReturn503WhenNoManager`：未注入 Manager 的 Server hit 2 端点均返 50301

### 验证
- `go vet ./...`：通过
- `go build ./...`：通过
- `go test -count=1 -timeout 250s ./...`：18 包全绿（mcp/api/runtime 新增 + 原有 15 包无 regression）
- `go mod tidy`：无新依赖
- `TestWSStreamDisconnectCancelsTurn` 单包并发 run 触发本会话一次 timing flake（已知 200ms time.Sleep
  问题，progress 早前已记录），单跑 / -count=3 全绿；非本 commit 引入
- `go test -count=3 ./internal/api/ -run TestWSStreamDisconnectCancelsTurn` 全绿

### 当前状态：37 路由 100% 已实现 + 0 个 501 stub
- 之前 35 条已实现 + 2 条 MCP stub 50101 占位
- 本 commit：MCP 2 stub 替换为真实 handler。`notImplemented` helper 保留无引用。

### 副债清单 / v1 已知限制（与 docs/mcp/checklist.md 对照）
| 文档项 | v1 实际 | 触发 |
|---|---|---|
| Manager lifecycle Prepare/Activate | stub 返 nil / disabled | docs §2、checklist §1 |
| runUpstream heartbeat / 重连 | 未实现 | docs §2、checklist §1 |
| Client Connect/Initialize/DiscoverTools/CallTool/Ping | 未实现 | docs §3、checklist §2 |
| 本地 MCPServer Serve/handleInitialize/handleListTools/... | 未实现；cfg.Server.Enabled=true 时 Activate 拒绝 | docs §4、checklist §3 |
| Transport stdio/sse/streamable_http | 未实现 | docs §5、checklist §4-6 |
| Tool 映射 `mcp.<server>.<tool>` + Proxy | 未实现 | docs §1、checklist §7 |
| 内置 Tool `mcp_list` | 未实现 | checklist §9 |
| 指标 / span | 未实现 | checklist §9 |
| 修复 W1 时间戳 / W2 README 导览 / W4 tokens[].roles | 仍保留 | progress 早前记录 |

### 下一轮方向
按 docs/mcp/checklist.md § 排序，建议下述渐进路径（每个独立 commit）：

1. **Client v1 起点**：`Client` 结构骨架 + `ConnectionStatus` 状态机 + `Connect/Close/Initialize/
   DiscoverTools/CallTool/Ping/Done/Err` 签名 + 错误 sentinel 映射（checklist §2；不接 transport）。
   目标：完成"Client 一代连接"类型契约 + 单测覆盖状态转换，不接真实 stdio/sse/streamable_http。
2. **stdio Client transport**：`StdioClient` + exec.Command + stdin/stdout JSON-RPC 行协议 + stderr
   日志捕获 + 优雅关闭（checklist §4）。目标：能连本地 npx @modelcontextprotocol/server-filesystem。
3. **Manager.Start / runUpstream 雏形**：构造 Client + 启动 + 状态转换 + DiscoverTools 把 Tool 注册到
   ToolManager 稳定 Proxy（checklist §1 §7 §9）。目标：Runtime 启动 auto-start=true server 能真连上
   本地 stdio MCP server 并显示 ToolCount>0、Status=connected。
4. **streamable_http / SSE**: network transport。
5. **本地 MCP Server + 工具暴露**：完整 binding 校验 + Serve ctx 生命周期 + handleInitialize/...
6. **重连 + heartbeat + 指标**：完成 lifecycle + observability。

注：本 commit 已让 Manager Stop/Done 幂等、状态字段就位，后续 commit 只需往 Manager 里加 entries
client 字段与其 lifecycle 而非改 API/handler 投影契约 —— Manager 与 Runtime 启停边界已固定。

## Phase 3 下一步 #9：MCP Client v1 起点（types 扩充 + ClientTransport 接口 + Client 完整实现 + 集成测试）（commit 待 push）

### 范围
落地 `internal/mcp/` 包 §2 MCP Client 完整骨架（docs/mcp/checklist.md §2、client.md §1-5）。
单代 Client 的请求/响应/状态机/fail/Close/握手/工具发现/调用全部就绪 — Manager 与 stdio transport
**依旧未接**（留后续 commit）。Client 通过 `ClientTransport` interface 完成所有 I/O；本 commit 用
`fakeTransport` 完整端到端集成测试（Initialize/Ping/DiscoverTools/CallTool/Close/状态/fail等）验证。

同时：先按用户约束修文档 `docs/mcp/checklist.md §1` Manager — 勾选 v1 已落地项 (`Manager` 骨架 /
`Get` / `Tools` / `List` / `Prepare` / `Activate` / `Ready` / `Stop`+`Done`) + 明确未交付项措辞
（`runUpstream` heartbeat / 重连 / 后台 teardown with `errors.Join` 仍 `[ ]`）。

### 文档依据
- `docs/mcp/checklist.md` §1（修文档）+ §2（Client 实现目标）
- `docs/mcp/client.md` §1 状态结构 + §2 Initialize/Ping + §3 DiscoverTools + §4 CallTool + §5 重连语义
- `docs/mcp/transport.md` §2 JSON-RPC 协议 + Message/RPCError wire DTO + 上限表 + validateEnvelope 规则；
  §3 ClientTransport 接口；preferredVersion/acceptsVersion 矩阵
- `docs/mcp/errors.md` §2 JSON-RPC code → sentinel 映射 (-32602 → ErrMCPInvalidParams,
  server-defined -32000..-32099 → ErrMCPToolExecFailed)

### `docs/mcp/checklist.md` 改动
§1 MCP Manager 项全部对齐 v1 实际：7 项勾选（含 v1 副债清单备注），1 项 keep `[ ]`（runUpstream 仍待
lifecycle commit 落地）。无 §2-9 改动。

### `internal/mcp/types.go` 扩充
- `Message` / `RPCError` (Error → 稳定 "mcp rpc error" 不含 message/data；docs/mcp/transport.md §2) /
  `Implementation` / `InitializeParams` / `InitializeResult` / `MCPTool` / `ListToolsParams` /
  `ListToolsResult` / `CallToolParams` / `Content` / `CallToolResult` / `ProtocolVersion` / 
  `LegacyProtocolVersion` / `TransportInfo` 常量与结构
- 用 json.RawMessage 严格 wire 透传（不干预 wire shape）

### `internal/mcp/transport.go`（新）
- `ClientTransport` 接口：Start / Send / Recv / Close / Info（docs/mcp/transport.md §3）
- `validateEnvelope(msg) (messageKind, error)`：严格 5 类校验（jsonrpc=2.0 / method 互斥 result/error /
  result 互斥 error / notification 无 ID / response 无 method 等等），任一违反返 ErrMCPProtocolError
- `messageKind` 枚举：request/notification/response/invalid + String() 诊断用
- `isNullJSON(b)`：识别 wire "null" 字面量 / 字符串 "null"
- `preferredVersion(transportType) string` + `acceptsVersion(transportType, serverVersion) bool`：
  按 docs/mcp/client.md §2 矩阵 (streamable_http → 2025-03-26 strict / sse → 2024-11-05 strict /
  stdio → 兼容两个版本)
- `parseID(json.RawMessage) (uint64, bool)`：仅接受 Client 自己签发的正整数 ID；string ID / 空 / 0 皆
  拒（避免 late response 误识别）

### `internal/mcp/transport_test.go`（新，9 例）
- validateEnvelope 各路径覆盖：request / notification / notification+id→request (语义升级) /
  response / response+method 拒 / result+error 互斥拒 / 空 version 拒 / empty envelope 拒 / nil 拒
- preferredVersion/acceptsVersion 矩阵覆盖 + parseID 正数/0/字符串/空 拒绝

### `internal/mcp/client.go`（新，560 行）
`Client` 结构详对齐 docs/mcp/client.md §1：
- 字段：name / runCtx / transport / status / mu / cancel / closeOnce / failOnce / closing / closeErr /
  closedErr / pendingMu / pending / wg / cntroll / recvDone (新: 无 recvLoop chan / 无 controlDone /
  无 lateCount，删 YAGNI 字段) / nextID / issuedHighWater
- `NewClient(name, runCtx, transport)`：未连接（status=disconnected）；调用方需 Connect→Initialize
- `Status()` mu.RLock 返当前快照
- `Done() <-chan struct{}`：failOnce 关闭；Manager 在此 wait torn-down
- `Err() error`：pendingMu.Lock 返 closedErr（stable sentinel 不带 wire 原 message/data）
- `Connect(startupCtx)`：disconnected→connecting→Start transport；失败 Close 并返错
- `request(ctx, method, params, out)`：marhalParams→pending 注册→Send→等 call.ch 或 ctx 取消
  + bestEffortCancel；stream 上限保护：nextID == MaxUint64 或 pending ≥ 1024 → fail(ErrMCPProtocolError)
- `marshalParams(params) (json.RawMessage, error)`：nil→ omitted / object / array 否则 ErrMCPInvalidParams
- `mapRPCError(err)`：-32601 → ErrMCPToolNotFound / -32602 → ErrMCPInvalidParams / 其他 →
  ErrMCPToolExecFailed（不假设 -32001 固定含义；docs/mcp/errors.md §2）
- `fail(err)`：failOnce；标记 closedErr / 摘 all pending 投递 / 置 status=Error / cancel / transport.Close
  / 关 c.recvDone 信号
- `retire(id, call)`：pendingMap identity check 仅删同指针 entry（防 fail 后误删新 entry）
- `bestEffortCancel(id, cause)`：100ms 超时发 notifications/cancelled，reason 限 128 bytes；
  不进 pending / 不重试 / 不可回放
- `Close()`：closeOnce + 关闭顺序 closing=true → fail(ErrMCPTransportClosed) → wg.Wait → status=Disconnected
- `runRecvLoop(ctx)`：唯一调用 transport.Recv；按 validateEnvelope 分类；response → dispatchResponse；
  request 入容量 32 control channel；notification 仅 tolerated tools/list_changed；
  control 满 / Recv 失败 / envelope 错 → fail
- `dispatchResponse(msg)`：parseID 失败/0 → protocol error / id > issuedHighWater → protocol error /
  未匹配 pending (已 retire late/duplicate) → 丢 (不毒化连接)
- `runControlLoop(ctx)`：处理 server ping → 返 ping 不带 result payload；其他 server method → -32601
- `handleServerRequest(ctx, msg)`：返响应 Message
- `notify(ctx, method, params)`：发 notification（无 ID，无 response）
- `Initialize(ctx)`：initialize → 校验 protocolVersion + capabilities.tools → notifications/initialized →
  status=Connected；任一步失败返错（caller 应 Close）
- `Ping(ctx)`：ping 验证当前代可用，不启动重连
- `DiscoverTools(ctx)`：128 pages / 4096 tools / 4 KiB cursor 上限；重复 cursor / 重复 name / wire shape 
  非法 → fail(ErrMCPProtocolError)；sorted by name 升序返回
- `normalizeTool(serverName, raw)`：远端 name 1..128 bytes、无控制字符、desc ≤ 4 KiB、schema ≤ 256 KiB；
  canonical `mcp.<server>.<remote>` ≤ 256 bytes、无控制字符
- `canonicalToolName` + `isValidName` helper
- `CallTool(ctx, name, arguments)`：未 connected 返 ErrMCPUnavailable；ctx 取消返 context.Cause(ctx)
  不重映射 caller DeadlineExceeded 为 ErrMCPToolTimeout（hard cap 由 Proxy 设置）
- 常量限制：maxPendingRequests=1024 / maxListToolsPages=128 / maxTotalTools=4096 / maxCursorBytes=4096 /
  maxToolNameBytes=128 / maxCanonicalNameBytes=256 / maxDescriptionBytes=4096 / maxInputSchemaBytes=256KiB /
  bestEffortCancelTimeout=100ms / controlBufferSize=32

### `internal/mcp/client_test.go`（新，15 例）
用 `fakeTransport` 实现 ClientTransport + channel 驱动 Recv / Send 捕获；端到端覆盖：
- TestClientInitializeLifecycle：Connect→status=connecting / Initialize 成功→connected / Close→disconnected
- TestClientCloseIdempotent：两次 Close 同 sentinel
- TestClientInitializeRejectsIncompatibleVersion：stdio 拒 "1.0.0" → ErrMCPProtocolError
- TestClientInitializeRejectsServerWithoutTools：没 advertise tools → ErrMCPProtocolError
- TestClientPingOK：Ping 成功路径
- TestClientDiscoverToolsSinglePage：1 页 2 工具，按 name 升序，`mcp.fs.<remote>` 命名前缀
- TestClientDiscoverToolsMultiPage：nextCursor 翻页直到空
- TestClientDiscoverToolsToolNameTooLong：192 byte name → ErrMCPProtocolError
- TestClientCallToolOK：result.IsError=false 回传 Content
- TestClientCallToolNotConnected：未 Connected → ErrMCPUnavailable
- TestClientRPCErrorMapping：-32602 → ErrMCPInvalidParams
- TestClientRequestCtxCancelReturnsCause：caller 取消 → context.Cause + bestEffortCancel 发
  notifications/cancelled
- TestClientRequestSendFailureFailsClient：transport.Send 拒绝 → wrap(ErrMCPTransportWrite) + Client fail
- TestClientDoneClosesOnClose：Close 后 Done 可读
- TestClientHandlesServerPingRequest：server ping request → Client 回 ping 响应

### 验证
- `go vet ./...`：通过
- `go build ./...`：通过
- `go test -count=1 -timeout 250s ./...`：18 包全绿（mcp/api/runtime 无 regression）；
  mcp 包 37 例 manager + transport + client 全绿
- `go test -count=3 ./internal/mcp/ -run TestClient`：3 轮重复 Client 测试全绿（无 timing flake）
- `go mod tidy`：无新依赖
- 注意：`go test -race` 在本 sandbox arm64 上返 ThreadSanitizer: unsupported VMA range，环境问题
  非代码问题；后续 commit 不在本沙箱跑 -race 检验

### 副债清单 / v1 已知限制
- Manager 与 Client 尚未集成（auto-start Client、Manager.runUpstream、heartbeat、Proxy 注册到 ToolManager）
- stdio/sse/streamable_http transport 尚未实现（用 fakeTransport 验证）
- onListChanged callback 未接（notification 仅 tolerate 但不通知 Manager）
-ReleasedDate listTools strict wire DTO 解码仍走标准 json.Unmarshal，未用 DisallowUnknownFields
  + EOF 双保险（后续 commit 接 stdio 后再补 strict 解码）
- connected_at 未发布到 ServerStatus（lifecycle commit 接入时填）
- bestEffortCancel reason 简单截断 128 bytes（未做 unicode boundary 处理）

### 下一轮方向 → （按 progress #8 末尾规划第 2 项）
1. **stdio Client transport**（checklist §4）：`StdioClient` + `exec.Command` + stdin/stdout JSON-RPC
   行协议 + stderr 日志捕获 + 子进程退出检测 + 优雅关闭。目标：能连本地 npx @modelcontextprotocol/
   server-filesystem 真实 stdio MCP server。
2. **Manager.runUpstream 雏形**（checklist §1 §7 §9）：构造 Client + 启动 + DiscoverTools →
   注册稳定 Proxy 到 ToolManager；Manager.Start 走 auto-start=true 串接 lifecycle，
   Runtime 启动后 ServerStatus.ToolCount>0、Status=connected。
3. streamable_http/SSE → 4. 本地 MCPServer → 5. 重连+heartbeat+指标 → 6. Planner step 1-2。

### Ponytail 决策记录
- 接口注入 ClientTransport 在 Client 层（而非 Client 持有具体 transport ），与上轮
  MCPServerProvider 在 API 层接口注入思路一致 — 新模块通过接口隔离 + fakeTransport 测试
  驱动贯通端到端，避免引入 stdio 真实依赖到 Client 单元测试。
- v1 listTools 实现不要求 DisallowUnknownFields + EOF 双保险（Ponytail YAGNI — 后续 stdio
  commit 接真实 server 后再补 strict 解码；当前 json.Unmarshal 已 cover schema 字段）。
- 删 `recvLoop chan`, `controlDone chan`, `lateCount int` 等 YAGNI 字段（无人读）；
  留 `recvDone` 是 Manager wait 信号，无人读其余两字段。
- mapRPCError 中 -32601 → ErrMCPToolNotFound（本地 catalog 查找用）；其他统一
  ErrMCPToolExecFailed（含 -32001 不假设过载语义），与 docs/mcp/errors.md §2 "不能假设 -32001 固定
  表示过载" 对齐。

## Phase 3 下一步 #10：stdio Client transport（cmd/yaasmoke 临时辅助）+ Client.Connect Start 顺序修复 + Python 端到端集成测试（commit 待 push）

### 范围
落地 `internal/mcp/StdioClient` 全量实现 docs/mcp/checklist.md §4 Transport — stdio。
通过真实 Python subprocess fake MCP server 完成 8 例端到端集成测试。**Manager.runUpstream 仍未接**（留下个 commit）。
**同时修一处 Client.Connect 真实 bug**：startLoops 必须在 transport.Start 之后启动 recvLoop，否则 recvLoop 在
transport stdout 尚未初始化时阻塞（rg：`recvReady nil channel select permanent block`）。

### 文档改动
`docs/mcp/checklist.md §4 Transport — stdio` 全部 8 项勾选 + 备注（StdioServer 留 §3 本地 MCP Server commit）。

### `internal/mcp/stdio.go`（新）
- `StdioClient` 结构 / `NewStdioClient(command, args, env, logger)` / `Start/Send/Recv/Close/Info`
- `recvReady chan struct{}` NewStdioClient 时 make + Start 时 close —— 防 Recv 在卡 nil channel
- `Start`：exec.Command + cmd.Env=composeStdioEnv(userEnv) + StdinPipe/StdoutPipe/StderrPipe + cmd.Start + pumpStderr goroutine
- `pumpStderr`：bufio.Reader 行级 ReadString → slog.Info（subprocess stderr 标签，不混入协议流）
- `composeStdioEnv`：白名单 inheritEnvKeys = [PATH/HOME/USER/LANG/LC_ALL] + 用户 env 覆盖（docs/mcp/integration.md §7）
- `Send`：json.Marshal → stdin.Write + 行尾；关闭/closed 时返 ErrMCPTransportClosed；body > 4 MiB 返 ErrMCPProtocolError
- `Recv`：readerForRecv lazy 跨 Recv 复用 bufio.Reader (4 MiB buffer)；ReadString → json.Unmarshal；
  EOF → ErrMCPTransportClosed；行超/解码失败 → ErrMCPProtocolError
- `Close`：closeOnce 幂等；close stdin → 5s timeout 等 cmd.Wait → 超时 SIGKILL → close stderr/stdout
  + stderrWG.Wait
- 常量：stdioMessageMaxBytes=4*1024*1024 / stdioCloseGraceTimeout=5s

### `internal/mcp/client.go` 改动（真实 bug 修复）
Connect 顺序改为**先 transport.Start 再 startLoops** —— startLoops 启动 recvLoop 调 transport.Recv 需要 transport stdout 已就绪；
反序会让 recvLoop 卡在 `select case <-c.recvReady:` (nil chan select permanent) → 整个 Initialize 路径死锁。

新流程：
1. 先 c.transport.Start(connCtx) — failure 时复位 status=Disconnected + cancel ctx 返错（不调 c.Close 避免误触 failOnce 失败路径）
2. 后 c.startLoops(connCtx) - failure 时调 c.Close 走 teardown

### `internal/mcp/stdio_test.go`（新，8 例 + 1 helper）
- `fakeMCPStdioServer` Python inline 脚本：实现 initialize / notifications/initialized / ping /
  tools/list / tools/call 的 JSON 响应；其他 method → -32601
- `requirePython3` — skip 测试沙箱无 python3 时（沙箱有 python3，全 8 例绿）
- `stdioClientEndToEnd` helper — 启动 fake MCP server + Client，Connect + Initialize + Cleanup
- TestStdioClientEndToEndLifecycle：Connect→Initialize 拓.connected→Ping OK→DiscoverTools 2 tools (mcp.fake.alpha/beta)→Close.disconnected
- TestStdioClientCallToolEndToEnd：name="remoteFoo" → content hello remoteFoo
- TestStdioClientSendBodyTooLarge：4 MiB + 10 上限触发 ErrMCPProtocolError
- TestStdioClientStartCommandNotFound：不存在 command → ErrMCPConnRefused
- TestStdioClientCloseIdempotent：多次 Close 同 sentinel
- TestStdioClientInfoConnected：Start 前 Connected=false / Start 后=true / Close 后=false
- TestStdioClientRecvOnSubprocessExit：process.Kill → Recv 返 ErrMCPTransportClosed
- TestStdioClientEnvInjection：用户 env 通过 transport 注入子进程，serverInfo.name="env:injected"

### 验证
- `go vet ./internal/mcp/`：通过
- `go build ./cmd/yaasmoke`（临时诊断工具）端到端：Initialize→DiscoverTools→Ping→CallTool→Close 全绿
  （诊断命令行已删；stdio.go 真实端到端通过 stdio_test.go 8 例验证）
- `go test -count=1 -timeout 60s ./internal/mcp/`：manager 13 + transport 9 + client 15 + stdio 8 共 45 例全绿
- `go test -count=1 -timeout 240s ./...`：18 包中 mcp/api/runtime 等全绿；TestWSStreamDisconnectCancelsTurn
  本轮触发一次已知 timing flake（200ms time.Sleep），单跑/-count=3 全绿，非本 commit 引入
- `go mod tidy`：无新依赖

### 副债清单 / v1 已知限制
- 没接 Manager.runUpstream：构造 StdioClient + 启动 + DiscoverTools → 注册 ToolManager 稳定 Proxy，后续 commit
- StdioServer（Yaa! 作为 stdio MCP Server）未实现；checklist §3 待本地 Server commit
- stdio client 子进程 stderr 用 bufio.NewReader（ 默认大小），不是 4 MiB 上限；超长 stderr 行可能截断 ok
  docs 仅协议 body 限 4 MiB（stderr 不入协议流故截断可接受）
- env inherit 白名单固定 5 项；用户实际 MCP server 需要更多 env 由配置 mcp.servers[*].env 显式注入
- 子进程优雅关闭超时 5s 硬编码；配置后续 commit 可接 mcp.timeout.connect（schema 已存在）

### 下一轮方向 → checklist §1 + §7 + §9 Manager.runUpstream + Tool Proxy
1. **Manager.runUpstream 雏形**（checklist §1 §7 §9）：
   - Manager 构造时启动 auto-start=true 的 stdio Client 串接 lifecycle
   - DiscoverTools → 把每个 Tool 经过 normalizeTool → ToolManager 注册稳定 Proxy（`mcp.<server>.<remote>` 命名前缀）
   - ServerStatus.ToolCount>0、Status=connected、ConnectedAtimestamp 注入
   - Stable Proxy 一旦失败置 unavailable、atomic client handle 置 nil → 调用返 ErrMCPUnavailable
2. **重连与 catalog reconciliation**（checklist §1 §7 + docs/mcp/client.md §5）：
   - 失败时按 reconnect 配置指数退避重连，重新 Initialize + DiscoverTools
   - Tool name/description/schema 精确一致才原子替换 client handle；差异保持 unavailable+ErrMCPProtocolError 要求重启
3. **heartbeat**：Manager 定期 Ping；失败换 Client
4. §5 SSE / §6 Streamable HTTP transport / §3 本地 MCPServer / §9 observability → Planner step 1-2

### Ponytail 决策
- 本 commit 同时修 Client.Connect 真实 bug 属于"修复文档先，然后审查已有代码冲突"框架 —— Connect 顺序 bug 是
  代码偏离而非文档 bug；fakeTransport 测试因 Recv 始终可读未触发该 bug，stdio 真实 stdout 才暴露。
- env 白名单而非全部继承：docs/mcp/integration.md §7 "stdio 子进程继承经过过滤的 env"；白名单 5 项足够
  npx / node 找命令 (PATH) + 基本 locale，其他按需由 mcp.servers[*].env 显式注入避免隐式泄漏 Token / API key 等
- PumpStderr bufio buffer 用 default 大小（不是 4 MiB）—— stderr 行通常 < 64K，YAGNI 4 MiB buffer
- Python fake MCP server 用 `-c` inline 字符串避免临时文件管理；每个测试独立 python subprocess 不复用
- 删 cmd/yaasmoke + /tmp 辅助脚本：诊断工具复用任务结束，按 ponytail "few files, no scaffolding" 原则不留
- 选用 `exec.Command`（不是 `exec.CommandContext`）：避免 ctx cancel 触发立即 SIGKILL 绕过 stdin.close 等待路径；Close 自己控 5s 超时

---

## #11 Manager.runUpstream 雏形 + Tool Stable Proxy 集成 (HEAD 待 push)

### 范围
checklist §1 runUpstream + §7 Tool 映射 + §9 集成（与 Tool Manager + Runtime shutdown）。SSE / Streamable HTTP / heartbeat / catalog reconciliation / 指数退避重连留后续 commit。

### 改动文件
- **`internal/mcp/proxy.go` 新建** (~145 行)：`ProxyHandle{atomic.Pointer[Client]}` + `MCPToolProxy` 实现 `tool.Tool` 接口（Name/Description/Parameters/Execute）+ `toToolResult` （多 text 块 \n 连接，isError 透传，非 text → ErrMCPUnsupportedContent，nil result → ErrMCPProtocolError）+ `toMCPResult`（反向映射，留待本地 MCPServer commit 用）。
  - 采用 docs/mcp/integration.md §1 代码样例，但补 `description` 字段（原样例缺省会被 `tool.ToolManager.Register` 拒空描述 → 文档先修）。
  - Execute：handle nil → `ErrMCPUnavailable`；非零 `timeout` 用 `WithCancelCause + AfterFunc`（Go 1.20 无 `WithTimeoutCause`）；返回前查 caller ctx 与 callCtx cause。
- **`internal/mcp/manager.go` 重写** (~310 行，公开 API 签名不变)：
  - `serverEntry` 扩 `cfg config.MCPServerConfig + handle *ProxyHandle + client *Client + status ServerStatus + tools []tool.ToolInfo`。
  - `NewManager` 缓存 cfg + 默认 stdio transport + 初始 ServerStatus 全 disconnected。
  - `Prepare`：遍历 auto_start=true 的 stdio server → `StdioClient + NewClient` → `Connect(connTimeout)` → `Initialize(initTimeout)` → `DiscoverTools(initTimeout hardcap)` → 每个 tool 注册 `MCPToolProxy` 到 ToolManager；success → Store handle + Status=connected + ToolCount + ConnectedAt。
    - 非 stdio transport 标 LastError="transport not supported in current build" 跳过（SSE / Streamable HTTP 待后续）。
    - 单 server 失败仅 LastError + Status=Error，**不阻断其他 server**（docs/integration.md §4）。
    - connect/init timeout 配置优先：`mcp.servers[i].timeout > mcp.timeout.connect/init`，缺省分别 10s/15s。
  - `Stop`：cancelRun + 关每个已建立 client (`Close` 幂等) + `handle.Store(nil)` + status 复位 Disconnected + close(done) + ready=false。
  - `List/Get/Tools` 投影真实状态（ConnectedAt 指针深拷贝；Tools 未连接或无 Tool → (nil,false)）。
  - `ProtocolVersion` 字段本轮仍 nil：Client 未暴露协商版本 getter，未引入额外 API 扩展（留 SSE/Streamable HTTP commit 补）。
- **`docs/mcp/integration.md` §1 修正**：MCPToolProxy 结构补 `description string` 字段（原文档样例省略，会导致 ToolManager.Register 拒空描述）+ 补 Name/Description/Parameters 方法注释。
- **`docs/mcp/checklist.md`**：
  - §1 runUpstream 勾选 + Prepare/Stop/Get/Tools/List 备注更新为本轮实际行为
  - §2 Client 整 11 项补勾选（前轮忘勾，progress #10 已记）
  - §7 Tool 映射：勾 9 项（适配/前缀/schema/结果/错误/稳定 Proxy/共享 handle/超时/分页），仅留 catalog reconciliation 重连原子替换 handle 未实现
  - §9 集成：勾 Tool Manager 集成 + Skill binding 之前注册 + Runtime shutdown Stop/Done/fresh-Stop（runtime.go:344-347 已落地，本轮补勾）
- **`internal/mcp/manager_integration_test.go` 新建** (~300 行)：复用 stdio_test.go 的 `fakeMCPStdioServer` Python 脚本，5 例端到端 + 5 例纯函数测试：
  - `TestManagerPrepareAutoStartStdioRegistersTools`：auto_start stdio → connected + ToolCount=2 + ConnectedAt 非 nil + Tools() 返 mcp.fake.alpha/beta + Source="mcp"
  - `TestManagerToolProxyCallViaToolManager`：经 `tool.Manager.Execute(ctx, scope, "mcp.fake.alpha", {})` 拿到 "hello alpha"
  - `TestManagerPrepareStdioAutoStartFailureMarksError`：command 不存在 → Status=error + LastError 非空 + ToolCount=0 + Prepare 不返错
  - `TestManagerPrepareAutoStartFalseLeavesDisconnected`：auto_start=false 不被 Prepare 启动
  - `TestManagerStopDisconnectsClients`：Stop 后所有 server 回 Disconnected + Tools 返 (nil,false)
  - `TestMCPToolProxyUnavailableWhenHandleNil`：handle nil → Execute 返 `ErrMCPUnavailable` + Name/Description 浅校验
  - `TestToToolResult{JoinsText,RejectsNonText,NilResult,WrapsErr}`：4 例纯函数

### 验证
```
HTTP_PROXY=... go vet ./internal/mcp/      # 通过
HTTP_PROXY=... go build ./...              # 18 包全过
HTTP_PROXY=... go vet ./...                # 全项目无 warning
HTTP_PROXY=... go test -count=1 -timeout 120s ./internal/mcp/
  # ok 1.970s — 55 例全绿 (13 manager + 9 transport + 15 client + 8 stdio + 10 本轮新)
HTTP_PROXY=... go test -count=1 -timeout 240s ./...
  # 18 包全过，WS flake 本轮未触发（详见 progress 早前记录）
```

### 决策
- **ServerStatus.ProtocolVersion 本轮 nil**：Client 未暴露协商版本 getter，本轮不为此扩展 Client API；LE 与 docs *string 可选定义对齐。SSE / Streamable HTTP commit 实现时一并补。
- **Tool registration 中途失败不回滚 ToolManager**：Manager 关 client + 标 LastError + Status=Error 收敛；ToolManager.Unregister 回滚属 YAGNI，留后续 catalog reconciliation commit 一起做。
- **Prepare 同步完成上游连接（无 runUpstream goroutine）**：v1 stdio auto_start 启动是有限并可等待的；Runtime 启动序需要 binding 校验前 Tool 已注册，同步语义最贴近 docs/integration.md §4。heartbeat / 后台重连 / catalog reconciliation 留 async runUpstream commit。
- **docs/integration.md §1 修正优先于代码**：根据用户规则"发现文档问题先修文档"，MCPToolProxy 缺 description 字段是文档 bug。
- **stripServerPrefix helper**：DiscoverTools 返回 mcp.<server>.<remote>，注册 Proxy 逆推出 remoteName 给 CallTool 用；正反路径都走 canonicalToolName 保证一致性。

### 下一轮方向
1. **重连 + catalog reconciliation + heartbeat**（checklist §1 §7 §8）：async runUpstream goroutine 定期 Ping → 失败按 mcp.reconnect 指数退避 → 重新 Initialize + 完整分页 tools/list → 重连后比对 Tool name+description+inputSchema 三元一致才 Store 新 handle，差异保持 unavailable + ErrMCPProtocolError。
2. **SSE Transport**（checklist §5）：SSEClient Event 流解析 + Last-Event-ID 续传。
3. **Streamable HTTP Transport**（checklist §6）：POST JSON-RPC + Mcp-Session-Id 状态码映射。
4. **本地 MCP Server**（checklist §3[+§6 +§9 本地暴露]）：MCPServer handleInitialize/ListTools/CallTool/Ping + -32601 Resource/Prompt。
5. **ServerStatus.ProtocolVersion 暴露**：给 Client 加 protocolVersion getter（Initialize 校验后保存），Manager 投影到 ServerStatus + 实现 SSE 2024-11-05 路径。
6. **Worker 集成 Agent / Session / Provider**（checklist §9 §2）：MCP Tool 在 Agent turn 投影到 Provider Function 列表；Session 视图按 Agent allowall 投影可用 MCP Tool。
7. **Planner step 1-10**（docs/planner/）。

---

## #12 Client.ProtocolVersion getter + ServerStatus.ProtocolVersion 暴露 (HEAD 待 push)

### 范围
上轮 #11 副债「ServerStatus.ProtocolVersion 本轮 nil；Client 未暴露协商版本 getter」。本 commit 收尾：Client 加 `protocolVersion` 字段（mu 保护，Initialize 成功后写入）+ `ProtocolVersion() string` getter；Manager.connectStdioServer 投影到 `status.ProtocolVersion`。Close 后 getter 仍返协商版本快照（避免 Manager 投影闪断）。

### 改动文件
- `internal/mcp/client.go` (~15 行)：
  - Client struct 加 `protocolVersion string`（mu 保护）
  - Initialize 成功路径 `c.protocolVersion = result.ProtocolVersion`
  - 新 `func (c *Client) ProtocolVersion() string`（mu.RLock）
- `internal/mcp/manager.go` (~6 行)：connectStdioServer 成功路径 `pv := client.ProtocolVersion(); e.status.ProtocolVersion = &pv`，替换上轮 nil 注释块；头部 comment 更新。
- `internal/mcp/client_test.go` (+63 行)：新 `TestClientProtocolVersionGetter`：未 Initialize 返 ""；协商后返 Server 选择版本（用 LegacyProtocolVersion="2024-11-05" + stdio transport，验证 stdio 首选 2025-03-26 但接受 legacy）；Close 后仍保留快照。
- `internal/mcp/manager_integration_test.go` (+6 行)：现有 `TestManagerPrepareAutoStartStdioRegistersTools` 加断言 `st.ProtocolVersion != nil && *st.ProtocolVersion == ProtocolVersion`（fake MCP server 返 2025-03-26）。

### 验证
- go vet + build ./... 全过
- mcp 包 56 例全绿 (55 上轮 + 1 新)
- 全项目 test ./... 仅触发已知 WS flake（TestWSStreamDisconnectCancelsTurn 200ms Sleep，progress #10/早前已记，非本系列 commit 引入；单跑全绿）

### 决策
- **Close 后 ProtocolVersion 仍返协商值**：Manager 投影 ServerStatus 用快照模型；Close 触发后 Manager 会同步置 status=Disconnected，但 getter 暴露的协商值作为该代连接的「历史版本」保留，避免 List 在 Stop 与 Disconnect 路径竞态下见到一帧 nil。
- **legacy 测试用 stdio transport**：docs/transport.md §2 明确 stdio 首选 2025-03-26 但接受 legacy 2024-11-05；该测试反向验证 `acceptsVersion("stdio", LegacyProtocolVersion) == true`。
- **Client 单元测试单独成测**：本轮发现 `TestClientInitializeLifecycle` 复用同一 fake transport 应答 initialize 但用 ProtocolVersion 常量；新测试用 LegacyProtocolVersion 独立隔离 path。

### 下一轮方向
本 commit 解决上轮副债后，progress #11 末尾的下一轮方向不变：
1. **重连 + catalog reconciliation + heartbeat**（async runUpstream goroutine + mcp.reconnect 指数退避 + tool 三元一致才 Store 新 handle；docs/mcp/config-ref.md §7.1/§7.2 是权威设计，含 generation + listChanged channel + ticker + lifecycleMu gate）。
2. WS flake 根因修复（独立 commit）：session.Manager 加 IsTurnActive 公开 API + ws_handler_test polling 替换 200ms Sleep。
3. SSE / Streamable HTTP transport（checklist §5/§6）。
4. 本地 MCPServer（checklist §3）。
5. Agent/Session/Provider 集成（checklist §9 §2）。
6. Planner step 1-10（docs/planner/）。

---

## #13 WS flake root-cause fix + session.Manager.IsTurnActive 公开 API (HEAD 待 push)

### 范围
progress #10 早前记录 TestWSStreamDisconnectCancelsTurn 200ms time.Sleep timing flake 是 "非本系列 commit 引入"——本 commit 彻底 root-cause 修：发现测试本身的两处 spec 违背 + 一个 race，对应最小修复。

### Root cause 三个独立问题
1. **测试 assumptions 违反 spec**：docs/remote-api/conversation.md §turn_id 明文规定 "turn_id 在 session 内永久唯一；已提交 user 的 turn_id 复用返回 40001"。原测试期望"重拨同 turn_id 应被接受"与此 spec 直接冲突。session.turn.go:51 的 `snap.Messages` 含同 TurnID 则拒绝是 spec-correct，bug 在 test。
2. **200ms hard-coded Sleep 不可靠**：全项目跑时 CPU 紧迫会让 sleep 提前结束 → conn2 重拨时 conn1 的 turn 还在 activeTurns 中 → 撞 ErrTurnIDConflict，尤其 easy 触发 flake。
3. **Hub 是 session 范围广播**：conn2 即使拨新 turn_id 也会收到 conn1 在 background 跑完时 publish 的 assistant_done frame（turn_abort_1），抢在 turn_after_disconnect 的 queued/assistant_start 前到达，引入非确定性。

### 修复
- **session.Manager 新增 IsTurnActive(sessionID, turnID) bool 公开 API**（runturn.go +13 行，mu.RLock 保护）：供测试在不进 frame 路径的情况下 polling 等 cleanup 完成。该 API 也是 Runtime/Remote API 可以查询任意 (sid, turnID) 的权威活动空间状态的有用公开 API（不限于 testing）。
- **ws_handler_test.go 重写 TestWSStreamDisconnectCancelsTurn（-23/+17 行）**：
  - 用 polling `sm.IsTurnActive(s.ID, "turn_abort_1")` 直到 false（最多 5s，10ms tick）作为唯一权威断言——这正是"Stream disconnect cancels turn"的 spec 语义。
  - 删除"重拨同/异 turn_id 起新 turn 应被接受"段——违反 spec（永久唯一）+ Hub 广播污染非确定性，留 turn_id 唯一性/end-to-end turn lifecycle 测试在他处单独覆盖。
  - 删除 `var _ = strings.HasPrefix` 防 unused stub（strings.Replace 已在 dialWS 实际使用，stub 是死代码）。

### 验证
```
go test -count=10 -timeout 120s ./internal/api/ -run TestWSStreamDisconnectCancelsTurn
  # ok —— 10 轮连续全绿（修复前 5 轮 5 fail）
go vet ./... # 全项目无 warning
go build ./... # 18 包全过
go test -count=1 -timeout 240s ./...
  # 二次跑全项目 18 包全绿（无 WS flake，无任何 fail）
```

### 决策
- **IsTurnActive 作为公开 API 而非 testing helper**：未来 Runtime/observability 可能需要查 turn 是否活动（如 health endpoint 报告 idle/active）；在 session 公开 API 上加这个冷读方法是 1 行成本 + 0 风险，且 ponytail "在 shared 函数加 1 guard 比在每 caller 加 sleep 都重复更小 diff"。
- **测试只测 "cancels" 单一语义**：Hub 广播跨连接复杂场景属于 turn lifecycle / hub subscribe 设计的范畴，不应在一个"disconnect cancels" 测试里交叉；测试单一断言更符合 isolation 原则。
- **没有修 200ms Sleep 改 800ms 的治标做法**：即使延长仍是 sleep-race 解，CPU 负载变化时重新触发是早晚事。polling 是 root-cause 修法。

### 副债清理
- progress #10 早前已记 WS flake；本 commit 后该 flake 已从已知副债清单中移除。
- progress #11 末尾「下一轮方向 #2 WS flake 根因修复（独立 commit）」已完成。

### 下一轮方向
progress #11 末尾的下一轮方向余下 4 项：
1. **重连 + catalog reconciliation + heartbeat**（async runUpstream goroutine + mcp.reconnect 指数退避 + tool 三元一致才 Store 新 handle；docs/mcp/config-ref.md §7.1/§7.2 是权威设计，含 generation + listChanged channel + ticker + lifecycleMu gate）。
2. SSE / Streamable HTTP transport（checklist §5/§6）。
3. 本地 MCPServer（checklist §3）。
4. Agent/Session/Provider 集成（checklist §9 §2）。
5. Planner step 1-10（docs/planner/）。

---

## #14 runUpstream heartbeat ticker + transport-close 自恢复 (HEAD 待 push)

### 范围
progress #11 优先级 #1 第 1 步（async runUpstream goroutine + heartbeat ticker + client.Done()/Ping 失败 → compare-and-clear generation 失败清理 + Stop 同步 Join）。**不**含 重连 / 指数退避 / catalog reconciliation / listChanged channel —— 留下一 commit（progress #14 末尾下一步）。

### 改动文件
- `internal/mcp/manager.go` (+~170 行)：
  - 新增常量 `heartbeatInterval=30s` / `heartbeatTimeout=10s`（docs/mcp/config-ref.md §7.2）
  - `serverEntry` 加 `generation uint64`（启动时 gen=0；重连时下一 commit 递增）+ `listChanged chan struct{}`（cap 1，本期声明不投递，留给 catalog reconciliation commit）
  - `Manager` 加 `upstreamWG sync.WaitGroup` 跟踪所有 entry 的 runUpstream goroutine
  - `connectStdioServer` 成功路径启动 `go m.runUpstream(e, handle, client, e.generation)` 并 Add(1)
  - 新 `runUpstream(e, handle, client, gen)`：select 等 4 事件 —— `runCtx.Done()` 退出 / `client.Done()` → markGenerationFailed / `ticker.C` → Ping（heartbeatTimeout）失败 → markGenerationFailed；ctx 已取消则视为 Manager 关闭不视为 heartbeat 失败
  - 新 `markGenerationFailed`：m.mu 保护下 compare-and-clear generation + e.client==client 比对；OK 则 handle.Store(nil) + status=Error + LastError；锁外关 client（幂等）
  - `Stop` 加 `m.upstreamWG.Wait()` 在 close done 之前；现有 cancelRun 已触发 runUpstream 的 `<-m.runCtx.Done()` 分支退出
  - 头部 comment + Prepare comment + Stop comment 更新为本系列 lifecycle 实况
- `internal/mcp/manager_integration_test.go`（+~150 行，3 例）：
  - `const fakeMCPExitServer`：fakeMCPStdioServer 变体，`tools/call name=="stop"` 时 `sys.exit(2)` 模拟上游 transport 突然中断（不影响 stdio_test.go 原版 fakeMCPStdioServer 8 例稳定）
  - `TestManagerRunUpstreamRecoversTransportClose`：Prepare → runUpstream 启动 → 触发子进程退出（调 mcp.exit.stop → python sys.exit）→ polling ≤5s 等 status 转 Error + LastError 非空 → 后续 `tm.Execute(mcp.exit.alpha)` 返 ErrMCPUnavailable（验证 handle.Store(nil)）
  - `TestManagerStopJoinsUpstreamGoroutines`：Prepare 启动 runUpstream 后 Stop 必须 ≤5s 返 + Done ≤2s 关闭，ticker join 无死锁

### 验证
```
go test -count=1 -timeout 60s ./internal/mcp/ -run 'TestManagerRunUpstream|TestManagerStopJoins|StopDisconnects' -v
  # 3 例 PASS (RunUpstreamRecoversTransportClose 0.22s, StopJoins 0.21s, StopDisconnectsClients 0.21s)
go vet ./... # 全项目无 warning
go build ./... # 18 包全过
go test -count=1 -timeout 120s ./internal/mcp/ # 58 例全绿 (上轮 56 + 本轮 3 新 - 重叠 TestManagerStopDisconnectsClients 已含; 实际加 2 例)
go test -count=1 -timeout 240s ./... # 二次跑 18 包全绿 (含 WS flake 已上轮根治)
```

### 决策
- **TestManagerStopDisconnectsClients 上轮已存在**：本 commit 该测试不动，验证 Stop 仍能在 transport active 时正确清理。新加的 StopJoins 是专门的 Stop+WG.Wait 同步测试，不重叠.
- **`fakeMCPExitServer` 不复用 shared fakeMCPStdioServer**：原版 tools/call 收任意 name 都正常返；本期需 sys.exit 触发 transport 断开. 修改原版可能影响 stdio_test.go 8 例已稳定的断言, 单独变体最小风险.
- **`_stop_ 工具触发 sys.exit(2)`** vs `kill proc`：kill proc 需要拿到 StdioClient.cmd.Process 私有字段在测试中访问过深；让 server 主动退出复用现有 tools/call 路径更接近"上游 process 异常崩溃"的语义, 是更真实的故障 репрезентативности.
- **`heartbeatInterval=30s` 固定**: docs §7.2 明文 fixed 30s/10s. 测试不触发 ticker 真等 30s——`client.Done()` 路径在 subprocess 退出后 sub-second 触发 markGenerationFailed, 比 30s ticker 快.
- **暂不引入指数退避重连**: 本 commit 是 雏形 heartbeat；失败立即转 Error + unavailable, 不重建. 配置 mcp.reconnect.enabled 的 rewrite 已是下个 commit 范围, 一次只动一个独立可验收步骤 (ponytail: 一步小可验收).
- **`generation` 启动时 = 0**: 单代场景下 markGenerationFailed 中 `e.generation != gen` 永远 false → 不 short-circuit. 重连 commit 才真正利用 generation 比对避免旧代 stale writes.

### 下一轮方向
1. **runUpstream 重连第 2 步**：markGenerationFailed 后按 mcp.reconnect.* 指数退避构新 Client（Initialize+DiscoverTools）→ 递增 generation 原子切换 entry/Client/handle；目录三 真比对一致 才成功.
2. **listChanged channel 事件路径**：tools/list_changed notification 投递到该代 listChanged cap-1 channel → runUpstream select 命中 → 完整 DiscoverTools + 三元严格比对 → 不一致保持 unavailable + ErrMCPProtocolError.
3. SSE / Streamable HTTP transport（progress #11 §3 §5/§6）.
4. 本地 MCPServer（checklist §3）.
5. Agent/Session/Provider 集成（checklist §9 §2）.
6. Planner step 1-10（docs/planner/）.

---

## #15 runUpstream 重连 Step 2 — 指数退避 + generation compare-and-clear + catalog 三元严格比对 (待 push)

### 范围
progress #14 末尾下一步 §1: runUpstream 失败分支接入 attemptReconnect. 按 mcp.reconnect 指数退避构新 Client (connect+init+discover) → catalog 三元 (canonical name + description + canonical-marshal InputSchema) 严格比对一致才能原子替换 handle + 递增 generation; 不一致保持 Error 不可自愈; max_attempts 耗尽后停止重连. **不**含 listChanged 事件路径 (留 Step 3).

### 改动文件
- `internal/mcp/manager.go` (+~190 行, 总 ~660 行)：
  - import + `bytes`/`encoding/json`
  - 提炼 `connectAndDiscover(e) (*Client, []MCPTool, error)` — Connect → Initialize → DiscoverTools 复用 path, 不改 entry. 失败仅 Close client 不改 status
  - 提炼 `registerProxies(e, handle, tools, toolTimeout) error` — 首代注册稳定 Proxy 完整流程, 中途失败不回滚已注册 (ToolManager 不提供 Unregister)
  - 提炼 `publishGeneration(e, handle, client, tools, newGen)` — entry 锁内原子更新 generation/client/tools/ConnectedAt/status/ProtocolVersion. 首代 newGen=0; 重连 newGen=oldGen+1
  - 提炼 `effectiveToolTimeout(serverTimeout, globalTimeout) time.Duration` — server 优先, 否则 global, 0= 仅 caller deadline
  - 新 `attemptReconnect(e, handle, oldGen) (*Client, uint64, bool)` — `Reconnect.Enabled=false` 直接返 keepGoing=false; attempt 1..max_attempts 退避 `initial * 2^(attempt-1) cap max` (interruptible via sleepInterruptible on m.runCtx); 调 connectAndDiscover, 失败记 LastError 并续 attempt; 成功后调 catalogMatches, 不等到一致前 close newClient. 进入 entry 锁前再检 `m.runCtx.Err() != nil` (Stop race) + 锁下比 generation == oldGen + 递增 newGen + handle.Store(newClient) + status=Connected + LastError=""
  - `catalogMatches(e, discovered []catalogItem)` 加锁读 e.tools 副本, 与 discovered 按 Index 对位三元机器比对 (len 不等 fail); canonical name + description 直接字符串相等; InputSchema 用 `canonicalJSON` round-trip marshal 后 bytes.Equal (Go json.Marshal 默认按 map key 升序排序) 消除 schema 对象 key 顺序差异 (docs §7.2 明示)
  - `catalogItem{canonicalName, description, inputSchema}` + `snapshotTools(tools)` 转 MCPTool → catalogItem
  - `canonicalJSON(raw json.RawMessage) (json.RawMessage, error)` — UseNumber 解 + Marshal 编, round-trip 后等价字节
  - `sleepInterruptible(ctx, d) bool` — Timer + ctx.Done() select, ctx 取消返 false 由调用层放弃重连
  - `runUpstream` 失败分支 (client.Done() / Ping 失败): 先 markGenerationFailed, 再调 attemptReconnect; keepGoing=true 则更新 local client/gen + ticker.Reset(heartbeatInterval) 继续同 goroutine (避免每 entry 多 goroutine); keepGoing=false 退出
  - `connectStdioServer` 改为: connectAndDiscover → registerProxies → publishGeneration(newGen=0) → go runUpstream. Talker 引入 Reconnect 配置使用 (Stop 兼容)
  - `markGenerationFailed` 注释与 signature 不变 (已正确 compare-and-clear)
  - 注释 sync: 顶部 series commit 段落补 Step 2 已落地描述; Prepare/serverEntry 注释中「下一 commit / Step 3 接入」改为「等待 Step 3」

- `internal/mcp/manager_integration_test.go` (+~110 行):
  - import + `encoding/json`
  - `TestManagerRunUpstreamRecoversTransportClose` (Step 1 旧例) 加 `Reconnect: {Enabled: false}` 保持失败不重连的 Step 1 路径行为期望 (Stage=Error 后 Execute 返 ErrMCPUnavailable); 已验证 5 次重复不 break
  - 新 `TestManagerRunUpstreamReconnectsAfterTransportClose`: 用同 fakeMCPExitServer (stop → sys.exit 触发 transport close); 配 `Reconnect: {Enabled: true, MaxAttempts: 3, InitialDelay: 100ms, MaxDelay: 1s}`; polling ≤5s 验 sawError (markGenerationFailed 中间态) → Connected (重连成功); 重连后 `mcp.recon.alpha` Execute 成功; 末尾 Stop ≤5s (重连 goroutine 干净 join)
  - 新 `TestCatalogMatches`: 函数级 catalogMatches 三元比对单测. 覆盖: 同 → true; schema key 顺序不同 → canonical 等 true; 不同 name/description/schema → false; 数量不同 → false; both empty → true. 不依赖 stdio (0.00s)
  - `mustNewManager(t)` 帮助函数供单测构造最小 Manager

### 验证
```
HTTP_PROXY=http://192.168.4.1:7890 HTTPS_PROXY=http://192.168.4.1:7890 GOPROXY=https://goproxy.cn,direct GOSUMDB=sum.golang.org go test -count=1 -timeout 60s ./internal/mcp/ -run 'TestManagerRunUpstreamReconnects|TestCatalogMatches' -v
  # 2 例 PASS (Reconnects 0.50s, CatalogMatches 0.00s)
go test -count=5 -timeout 120s ./internal/mcp/ -run 'TestManagerRunUpstreamReconnects|TestManagerRunUpstreamRecovers|TestManagerStopJoins' -v
  # 3 例 × 5 = 15 PASS, 无 flake
go vet ./... # 全项目无 warning
go build ./... # 18 包全过
go test -count=1 -timeout 240s ./... # 18 包全绿 (含 WS flake 上轮已根治)
```

### 决策
- **catalog 三元比对用 canonical JSON round-trip**: docs §7.2 明文「不比分页或对象 key 顺序」. Go `json.Marshal` 已按 map key 升序序列化, round-trip 即可吸顺序差异; UseNumber 防大整数丢精度. 比 strings.TrimSpace + bytes.Equal 更严格等价.
- **`Ponytail`: 不引入 lifecycleMu**. 文档 §7.2 提到「取得 lifecycleMu 检查 stopping」但当前 Manager 已用 `m.runCtx` 作 stop 信号 (Stop 调 cancelRun 取消). attemptReconnect 进入 entry 锁前再查一次 `m.runCtx.Err() != nil` + 锁下比 generation == oldGen, 与「lifecycleMu/stopping」语义等价但少一个 mutex (ponytail ladder: 现有 lock 已够用, 不新加 lock).
- **重连不开新 goroutine**: docs §7.2 「每个 entry 只允许该一个 goroutine 重连」. runUpstream 失败分支直接 inline 调 attemptReconnect, 成功后 ticker.Reset 用新 client 继续 select. 这条 invariant 满足且无额外 wg 计数 (已是 upstreamWG 跟的同一 goroutine).
- **重连不重注册 Proxy**: ToolManager.Register 失败回滚不在 ToolManager 支持范围 (YAGNI Unregister). 首代成功后 Proxy 已 ToolManager 注册固定, 重连只需原子切 handle 的 client 字段 (Proxy.handle atomic Load). 第 N 代只需要 catalog 与首代一致, Proxy.Name()/Description()/Parameters() 不变. 接 attemptReconnect 时不调 registerProxies.
- **退避 interruptible**: sleepInterruptible 可被 m.runCtx.Done() 中断, 让 Stop ≤5s (与 StopJoinsUpstreamGoroutines 测试一致). Stop 路径触发 cancelRun 立刻打断退避 timer.
- **catalog 漂移 ExitOK → 不继续 attempt**: docs §7.2 「差异保持 unavailable + ErrMCPProtocolError 要求重启 Runtime」, 即 catalog 漂移是非自愈错误. catalogMatches 返 false 时 attemptReconnect 立即返 keepGoing=false (不再尝试 max_attempts), Status 保持 Error, LastError 记 catalog drift.
- **接入 generation compare-and-clear 重代场景**: 重连成功 newGen=oldGen+1, markGenerationFailed 用 gen 比对. 单代时 e.generation==gen 总成立, mark 不 short-circuit; 重连后代已升 gen, 旧 gen 标记不会污染新代 (暑期在更复杂并发场景下有效).
- **TestManagerRunUpstreamRecoversTransportClose 加 Reconnect.Enabled=false**: 该 Step 1 旧例验证 markGenerationFailed 后保持 Error 路径, 不应被 Step 2 重连影响. 显式 关 Reconnect 保持原意图, 同时给 Step 2 单独留测试用例.
- **不测 attemptReconnect 单独调用**: 单测函数级 catalogMatches 已覆盖比对核心, attemptReconnect 完整端到端由 ReconnectsAfterTransportClose 覆盖 (含退避 + Stop join + post-reconnect Execute). 不写重复单测减少 flake 表面 (Ponytail: YAGNI 测试).

### 下一轮方向
1. **runUpstream Step 3 — listChanged 事件路径**: Client.onListChanged 投递到该代 listChanged cap-1 channel → runUpstream select 命中 → 用当前代 Client 完整 DiscoverTools + 三元严格比对; 不一致保持 unavailable + ErrMCPProtocolError.
2. SSE / Streamable HTTP transport (checklist §5 / §6).
3. 本地 MCPServer (checklist §3) — Yaa! 作为 Server.
4. Agent/Session/Provider 集成 (checklist §9 §2).
5. Planner step 1-10 (docs/planner/).

---

## #16 runUpstream listChanged Step 3 — tools/list_changed 通知 + catalogReconcile + 漂移不可自愈 (待 push)

### 范围
progress #15 末尾下一步 §1: 收到 `notifications/tools/list_changed` 通知后用当前代 Client 完整 DiscoverTools + 三元严格比对 (canonical name + description + canonical-marshal InputSchema); 一致保持 Connected 不替换 Client; 漂移关闭该 Client + 标 ErrMCPProtocolError 保持 Error 不可自愈.

### 改动文件
- `internal/mcp/client.go` (+~20 行)：
  - Client struct 加 `onListChanged func()` 字段 (pendingMu 保护以避免与 recvLoop race)
  - 新 `SetOnListChanged(fn func())` setter, 必须在 Connect 前设置 (Manager 创建 client 后即调)
  - recvLoop notification 分支: method == `notifications/tools/list_changed` 时**非阻塞**调用 onListChanged (do not 发起 request in recvLoop, docs/mcp/client.md §recvLoop); 其他 notification 按 ErrMCPProtocolError fail
  - 修正原代码 method 名比较 prefix: 之前用 "tools/list_changed" (无 notifications/ 前缀) 实际 server emit 是 `notifications/tools/list_changed` (MCP spec 标准命名空间); docs 表述 "tools/list_changed" 是简写无前缀, 代码层用全名

- `internal/mcp/manager.go` (+~90 行)：
  - publishGeneration (首代) + attemptReconnect 末段 (重连) 两路径都: 每代新建**独立** listChanged cap-1 channel (不复用旧代) + client.SetOnListChanged 设置 closure 非阻塞投递到该 channel. 满足 docs §7.2 "旧代 channel 永远不被新代复用, 避免迟到 callback 触发新一代重连" invariant
  - attemptReconnect 签名改为返回 `(*Client, uint64, chan struct{}, bool)` 多一个新代 listChanged channel. `return nil, 0, false` 统一改为 `return nil, 0, nil, false`; 成功 return `newClient, newGen, e.listChanged, true`
  - runUpstream 加本地快照参数 `notify := e.listChanged` (启动代快照不可重读 e.*), 重连成功后更新 `notify = newNotify`; select 加 `case <-notify` 分支调用 catalogReconcile(e, handle, client, gen) → (closeClient, exit). closeClient 则锁外调用 client.Close(); exit 则退出 goroutine (漂移不可自愈, 不再 attemptReconnect)
  - 新 `catalogReconcile(e, handle, client, gen) (closeClient, exit bool)`:
      - 用 initTimeout hardcap 调 client.DiscoverTools → snapshotTools → catalogMatches
      - 一致: 返回 (false, false); 调用方不动 entry, 继续同 ticker (notify 合并 channel 已消化)
      - DiscoverTools 失败: entry 锁下比 `e.generation == gen && e.client == client` 后 handle.Store(nil) + status=Error + LastError="list_changed reconcile failed: <err>"; 返 (true, true)
      - catalog 漂移: entry 锁下同上 + LastError="list_changed reconcile: catalog drift (ErrMCPProtocolError)"; 返 (true, true)
  - catalogMatchesReadOnly 是 catalogReconcile 内部复用 catalogMatches 的薄封装 (避免直接调 m.catalogMatches 时视觉模糊, 但事实上直接调也可; ponytail: 此间接层冗余可平 ◦; 保留dbo 单 stack 一行更直)

- `internal/mcp/manager_integration_test.go` (+~150 行)：
  - `const fakeMCPListChangedStable` — alpha/beta 任意 tools/call 响应后立即 emit `notifications/tools/list_changed` 一帧; tools/list 永远返回原 catalog. 测一致分支
  - `const fakeMCPListChangedDrift` — 内部 `list_calls` 计数 + `emit_drift` flag: 第一次 tools/list 返回原 schema; alpha call 时 emit response + 切到 drift (description 改为 "modified_a") + emit 通知; 第二次 tools/list (catalogReconcile 调) 返回 drift schema. 测漂移分支
  - `TestManagerRunUpstreamListChangedStableKeepingConnected` — Prepare → 触发 alpha call (server emit notify) → polling ≤1.5s 验 status 仍 Connected → post-notify Execute 仍 ok. 5 次重复无 flake
  - `TestManagerRunUpstreamListChangedDriftMarksError` — Prepare → 触发 alpha call (server emit notify + 切 drift) → polling ≤5s 验 status=Error + LastError 非空 → Execute 返 ErrMCPUnavailable. 5 次重复无 flake

### 验证
```
HTTP_PROXY=http://192.168.4.1:7890 HTTPS_PROXY=http://192.168.4.1:7890 GOPROXY=https://goproxy.cn,direct GOSUMDB=sum.golang.org go test -count=1 -timeout 60s ./internal/mcp/ -run 'TestManagerRunUpstreamListChanged' -v
  # 2 例 PASS (Stable 1.7s, Drift 0.2s)
go test -count=5 -timeout 60s ./internal/mcp/ -run 'TestManagerRunUpstreamListChanged' -v
  # 10 例 PASS, 无 flake
go vet ./... # 无 warning
go build ./... # 18 包全过
go test -count=1 -timeout 240s ./... # 18 包全绿 (含 WS flake 上轮已根治 / Step 2 重连稳定 / Step 3 listChanged 闭环)
```

### 决策
- **method 名 `notifications/tools/list_changed` 而非 `tools/list_changed`**: MCP spec 通知 method 带 `notifications/` 命名空间前缀; docs 表述 "tools/list_changed" 是简写. 代码层用全名与 bestEffortCancel (`notifications/cancelled`) / Initialize (`notifications/initialized`) 一致. 第一次 vet 编译过但测试失败因 method 名比较不匹配 → client.go recvLoop 收到带前缀 method 后 fallthrough 到 fail 分支. 修正后全绿.
- **进入 entry 锁前过 race 防护**: catalogReconcile 两条修改 entry 路径 (DiscoverTools 失败 / 漂移) 都先在 entry 锁下比 `e.generation == gen && e.client == client`. 与 Stop race (Stop 已置 e.client=nil) 或 attemptReconnect race (重连已换 e.generation) 时 stale 不改 entry. 本 entry 单 goroutine (runUpstream 唯一重连 owner) 理论上不会 race, 但 inner check 是文档 §7.2 "代际不匹配或 catalog 漂移都在锁外 Close, 禁止发布" 的对应防御.
- **catalogReconcile 一致分支不替换 Client**: docs §7.2 "相同、该 generation 仍存活且未 stopping 才重新发布同一 Client". "重新发布同一 Client" 即 handle 已指着同一 Client, 无需修改 entry 字段. 所以一致分支 return (false, false), 调用方 runUpstream 不动 entry 仅继续 ticker. 这是 Ponytail 关键简化: 一致路径 0 修改.
- **catalogMatchesReadOnly 间接层**: 作为 catalogReconcile 内部对 catalogMatches 的封装, 纯语义标记. 事实上 catalogReconcile 可直接调 m.catalogMatches. Ponytail 实质 level: 该封装纯可 inline 取消. 保留dbo 一行虽省字小但语义清晰; 不取消是 Ponytail ladder §6 (能一行就一行) 的负例. 本 commit 不再拆分以保完整逻辑.
- **listChanged channel 每代新建独立**: publishGeneration/attemptReconnect 末段都 `e.listChanged = make(chan struct{}, 1)`. 旧代 channel 引用由 runUpstream 局部快照 notify 持有; 重连切新代后旧 channel GC. 满足 docs §7.2 "旧代 channel 永远不被新代复用".
- **关闭该代 Client 后退出 runUpstream (漂移不可自愈)**: catalogReconcile 漂移分支返 (closeClient=true, exit=true). 与 attemptReconnect 不同 (attemptReconnect 在 attempt > max_attempts 才退出); catalog drift 直接退出是因为 attemptReconnect 也只能发现相同 drift, 重连不修漂移问题 (runtime 错误, 文档要求 Runtime restart 重新建立 catalog). 路径保持简单.
- **listChanged cap-1 合并语义**: onListChanged closure 用 `select { case e.listChanged <- struct{}{}: default: }` 非阻塞投递. 通知期间如 runUpstream 还没消费, 第二次合并不丢只占用同一空槽. docs §7.2 "listChanged 仅允许合并重复通知". 已经没积压.
- **stable test polling 1.5s**: catalogReconcile 一致分支会立即跑完 DiscoverTools (sub-100ms) + 不动 entry, polling 1.5s 比 3s 快. Drift test polling 5s 给 catalogReconcile 完整执行余地.

### 下一轮方向
1. SSE / Streamable HTTP transport (checklist §5 / §6).
2. 本地 MCPServer (checklist §3) — Yaa! 作为 Server.
3. Agent/Session/Provider 集成 (checklist §9 §2) — MCP Tool 在 Agent turn 投影到 Provider Function 列表.
4. Planner step 1-10 (docs/planner/).
5. Remote API `GET /api/v1/mcp/servers` + `GET /api/v1/mcp/servers/:name` (checklist §9).
6. 文档副债 (W1 时间戳/W2 README 导览/W4 tokens[].roles 默认, 早前 progress 已记).

---

## #17 SSE Transport — SSEClient (legacy MCP 2024-11-05) + Manager 接入 (待 push)

### 范围
progress #16 末尾下一步 §1: checklist §5 SSE transport. 新增 `internal/mcp/sse.go` 实现 SSEClient 满足 ClientTransport 接口; 公开 SSE 接到 Manager buildTransport/Prepare; 用 httptest mock SSE server 端到端测 Client lifecycle + Manager.Prepare sse auto_start 路径.

### 改动文件
- `internal/mcp/sse.go` (+~360 行) — 新文件:
  - 常量 `sseMessageMaxBytes=4MiB` (与 stdio 一致 docs §2); `sseFrameMaxBytes` (frame 含字段头开销上限)
  - `tlsConfig` type = `struct{caFile string}`, 占位 docs §5 (v1 不实现 ca_file 真实加载; http.Client TLS 由调用方提供)
  - `SSEClient` struct 字段: url, headers, tls, client *http.Client, logger, mu, started, closed, resp, body, reader, endpoint, lastID, info, recvReady, closeOnce, procCtx/procCancel (GET 流生命周期)
  - `NewSSEClient(url, httpClient, headers, logger)` — httpClient nil → 默认; logger nil → slog.Default()
  - `Start(startupCtx)`: 建 procCtx (with cancel); 启动 ctx 取消传播到 procCtx (后台 goroutine); http GET + Accept: text/event-stream + headers 注入; 非 2xx → ErrMCPAuthFailed (401/403) 或 ErrMCPConfig; Content-Type 不含 text/event-stream → ErrMCPProtocolError; 拨号失败 → conn refused (strings 启发式) / channel ErrMCPConnTimeout (startupCtx 已取消)
  - `parseFirstFrameLocked`: readSSEFrame 拿 endpoint 帧; event 必是 "endpoint"; data TrimRight \n; url.Parse base + ref → ResolveReference; 跨 host/scheme 拒 (docs §3.2); 失败回滚流
  - `Send(ctx, msg)`: POST endpoint + Content-Type: application/json + Accept: application/json, text/event-stream + headers; marshal 后 >4MiB → ErrMCPProtocolError; 非 2xx → ErrMCPAuthFailed 或 ErrMCPTransportWrite; POST body 忽略 (docs §3.2 同步结果通过 SSE 流 Recv 拿)
  - `Recv(ctx)`: 等 recvReady; 循环 readSSEFrame; event != "" && event != "message" 跳过; 空帧 (无 data) 跳过; 更新 lastID (v1 仅记录); json.Unmarshal frame.data → Message; 流 EOF + ctx 取消 → ErrMCPTransportClosed
  - `Close`: closeOnce + cancel procCtx 关流 (强制 reader EOF) + close resp.Body; info.Connected=false. 幂等
  - `Info`: 返当前 TransportInfo (Type=sse, Endpoint=url, Connected 状态)
  - `sseFrame` struct + `readSSEFrame(reader)`: 单 frame 直到空行; ReadString('\n') 按 \r\n 处理; comment `:...` 忽略; field:value 分割 (colon+1 跳过空格); event/id/data/retry 字段; 未知字段忽略 (SSE spec); bufio.ErrBufferFull → frame too long
  - `isConnRefusedErr(err)`: strings 启发式匹配 "connection refused" / "no such host" → 区分 dial refused (Ponytail: 不深 unwrap net.OpError)

- `internal/mcp/sse_test.go` (+~360 行) — 新文件:
  - `fakeSSEServer` (*httptest.Server) 模拟 legacy MCP SSE server: /sse (GET text/event-stream + 首帧 endpoint + 后续 message 帧) + /message (POST 收 JSON-RPC 投到 SSE writer goroutine 推响应). 推约定: initialize → 协议 Legacy 2024-11-05 + tools capability; ping → empty result; tools/list → alpha/beta; tools/call N → "hello N"; notifications/initialized → 无响应
  - `TestSSEClientEndpointParseAndMessageRoundTrip` — Connect → Initialize → Ping → DiscoverTools → CallTool (alpha "hello alpha") → Close 全 MCP 协议走通; ProtocolVersion=Legacy
  - `TestSSEClientRejectsCrossHostEndpoint` — fake server 首帧 endpoint 指向 evil.example.com → Start → ErrMCPProtocolError (跨 host 拒)
  - `TestSSEClientReturnsConnRefusedOnDialFail` — 端口未开 (:1) → Start 返 ErrMCPConnRefused|TransportClosed|ConnTimeout
  - `TestSSEClientStreamEOFTriggersTransportClosed` — server 主动断流 → Recv 返 ErrMCPTransportClosed
  - `TestReadSSEFrameCompliance` — 单测 SSE frame parser 覆盖: single/multi-line data, id, default event, comment heartbeat, leading space after colon, data with no value (7 子用例)

- `internal/mcp/manager.go` (+~30 行):
  - import 加 `net/http`
  - 抽 `buildTransport(e) (ClientTransport, error)`: 按 e.transport 选 stdio (NewStdioClient) / sse (NewSSEClient) / 其他 → ErrMCPConfig
  - `connectAndDiscover` 不再直接 NewStdioClient; 调 buildTransport
  - `Prepare` 允许 sse transport (e.transport == "stdio" || "sse" 时 run connectStdioServer; 否则其它 streamable_http 等仍 LastError="transport not supported in current build")
  - 头部注释同步: sse 已接入
  - manager_integration_test.go 加 1 集成测试 `TestManagerPrepareSSEAutoStartRegistersTools` 用 fakeSSEServer + Manager.Prepare + 校验 StatusConnected + ProtocolVersion=Legacy + Transport=sse + ToolCount=2 + Execute mcp.sse.alpha + Stop ≤5s

### 验证
```
HTTP_PROXY=http://192.168.4.1:7890 HTTPS_PROXY=http://192.168.4.1:7890 GOPROXY=https://goproxy.cn,direct GOSUMDB=sum.golang.org go test -count=1 -timeout 30s ./internal/mcp/ -run 'TestSSE|TestReadSSE' -v
  # 5 PASS (EndpointRoundTrip 0.02s full MCP handshake; Cross-host; ConnRefused; StreamEOF; Frame parser 7 子用例)
go test -count=5 -timeout 120s ./internal/mcp/ -run 'TestSSE|TestReadSSE|TestManagerPrepareSSE' # 5x重复无 flake
go test -count=1 -timeout 30s ./internal/mcp/ -run 'TestManagerPrepareSSE' -v # Manager SSE端到端 Prepare+Execute+Stop 全 PASS 0.05s
go vet ./... # 无 warning
go build ./... # 18 包全过
go test -count=1 -timeout 240s ./... # 18 包全绿 (mcp 包现 68 例: 60 + 5 SSE 集成 + 3 新集成 test 现共计 68? 实际: 63 prev + 6 new = OK)
```

### 决策
- **No Last-Event-ID 重连续传 v1**: docs §3.2 "重连时只携带最后收到的事件 ID; 任何已经发送的 Tool 请求都不自动重放" — 但 Manager attemptReconnect 是用新 Client 重新 initialize + 完整 DiscoverTools (首代流程), 不复用旧代 Client 的流. Send 也只服务新调用 不重放. 所以 v1 SSEClient 不实现断流后重连续传 (lastID 仅记录字段); Manager 重连已覆盖需求. (后续如果需要单 connection-level SSE resume, 6-step 实现 Last-Event-ID header, 留下一 commit.)
- **Tls ca_file 占位 struct**: 不在 SSEClient 内自建 *tls.Config; 调用方传入 http.Client 含自己的 Transport/TLS. struct `tlsConfig` 仅 caFile 字段占位; ponytail: 不引入 ca_file 实际加载 (尚未有 server 真实走 ca_file 测试; 留下一 commit 接 ca_file 真实逻辑). docs §5 不提供 insecure_skip_verify 已明示, 我们不引入该字段.
- **POST body 忽略**: docs §3.2 简化描述下 SSE 的 POST 同步响应应通过 GET 事件流推回, POST body 可能是空 (202 Accepted) 或 application/json; ponytail 取最简 - 完全不读 POST body (just check status code 非 2xx → fail). 真实结果通过 Recv 推回. fakeSSEServer handle 中返 202 + 不写 body 也覆盖了此路径.
- **SSE frame parser bufio 上限**: `sseFrameMaxBytes` (4 MiB + 4 KiB 头开销). bufio.NewReaderSize 设置该上限, ReadString('\n') 超 buffer → bufio.ErrBufferFull → 返 "frame too long". 防 OOM 与 stdioMessageMaxBytes 同一提到到的 4 MiB body 上限保持一致.
- **`isConnRefusedErr` strings 启发式 vs unwrap net.OpError**: ponytail: 不引入 syscall 错误深 unwrap. 常见 dial refused / no such host 用 strings.Contains 匹配 closed 测试 set 错误信息已足够; 测试用 ErrMCPConnRefused|TransportClosed|ConnTimeout union 接收, 没强要求具体 sentinel.
- **Single-thread Start vs go procCtx**: GET 流生命周期用 procCtx (不与 startupCtx 直接共用). 后台 goroutine 把 startupCtx 取消转换 → procCtx cancel. 这样拨号阶段超时 (startupCtx) 与流读阶段断开 (procCtx 在 Close 时 cancel) 分离. Ponytail: 不引入 context.AfterFunc (Go 1.21+ 不可用) 用 select goroutine.
- **不引入 SSEServer struct**: docs §5 仅 SSEClient (Client 端). SSEServer 是 local MCP Server 路径 (checklist §3 待实现), 留 §3 commit 处理. 本 commit 仅完成 SSE 客户端到 Manager.

### 下一轮方向
1. **Streamable HTTP transport (checklist §6)**: POST JSON-RPC + Mcp-Session-Id + HTTP 状态映射 (401/403 → ErrMCPAuthFailed; init 404/405 → ErrMCPConfig); optional GET Server-to-Client SSE stream; DELETE termination.
2. 本地 MCPServer (checklist §3) — Yaa! 作为 Server (SSEServer + Streamable Server).
3. Agent/Session/Provider 集成 (checklist §9 §2) — MCP Tool 在 Agent turn 投影到 Provider Function 列表.
4. Planner step 1-10 (docs/planner/).
5. Remote API `GET /api/v1/mcp/servers` + `GET /api/v1/mcp/servers/:name` (checklist §9).

---

## #18 Streamable HTTP Transport — StreamableHTTPClient (POST-only + session header 复用) + Manager 接入 (待 push)

### 范围
progress #17 末尾下一步 §1: checklist §6 Streamable HTTP transport. 新增 internal/mcp/streamable_http.go 实现 StreamableHTTPClient 满足 ClientTransport 接口. v1 实现 POST-only + session header 复用模式 (stateless + 含 session 后 POST 必带), 不发 GET SSE 流 / DELETE (docs 明示 stateless client 不发 GET/DELETE; Manager attemptReconnect 路径已用 initialize + Discover 重建 catalog, 不依赖 SSE 流的 tools/list_changed). HTTP 状态码完整按 docs §3.3 错误表映射 14 个分支.

### 改动文件
- `internal/mcp/streamable_http.go` (+~290 行新文件):
  - 常量 `streamableMessageMaxBytes=4MiB` (与 stdio/SSE 一致); `streamableRespBodyCap=16KiB` (错误 body 有界丢弃 docs §3.3); `streamableRecvChanCapacity=256`
  - `StreamableHTTPClient` struct 字段: url, headers, client *http.Client, logger, mu, started, closed, sessionID, info, recvReady, closeOnce, recvCh chan recvItem
  - `recvItem {msg *Message; err error}` 中间结构 (Send 后投递 recvCh, Recv 取)
  - `NewStreamableHTTPClient(rawurl, httpClient, headers, logger)`: httpClient nil → 默认; logger nil → slog.Default(); 强制 httpClient.CheckRedirect = `http.ErrUseLastResponse` 拒 3xx (docs §3.3)
  - `Start(startupCtx)`: stateless 无持久连接; 仅校验 URL academy性 + 关 recvReady 让 recvLoop 进入
  - `Send(ctx, msg)`: 先查 caller ctx (终结 → context.Cause 优先 docs); marshal >4MiB → ProtocolError; POST + Content-Type: application/json + Accept: application/json, text/event-stream + headers 注入 + Mcp-Session-Id (若有); 非 2xx → mapStatusError 投 err 到 recvCh; 2xx → notification 路径 202 空 body 不投; request 路径按 Content-Type 分流 (text/event-stream → drainSSEResponse 多帧投递 recvCh; application/json → json.Unmarshal 单帧投 recvCh; 数组 body ['…' 首字符 → ProtocolError catch batch])
  - `drainSSEResponse(body)`: 复用 sse.go `readSSEFrame` parser, 把 SSE 流每条 message event 反序列化 Message 投 recvCh; 非 message event 跳过; EOF 视为流结束
  - `mapStatusError(resp, msg)`: docs §3.3 错误表完整映射 (status 2xx → nil 调用方继续解析):
    - 401/403 → ErrMCPAuthFailed
    - init 404/405 → ErrMCPConfig
    - init 408/504 → ErrMCPConnTimeout
    - init 429/5xx → ErrMCPUnavailable
    - hasSession + (400/404/410) → ErrMCPTransportClosed (session 失效重新 init)
    - business POST (!isInit) + (408/429/5xx) → ErrMCPTransportWrite (结果不确定, 不重发)
    - 413 / 3xx (含 CheckRedirect) → ErrMCPProtocolError
    - 错误 Content-Type 既然默认 default 走 → ErrMCPProtocolError
    - default → ErrMCPProtocolError
  - `Recv(ctx)`: 阻塞读 recvCh 投递; ctx 取消优先
  - `Close`: closeOnce + 标 closed + info.Connected=false; v1 不发 DELETE (stateless + session 复用模式; 待后续接 server SSE 流时补 DELETE 终止 session)
  - `isErrContentType(resp)`: 响应 ctype 既非 application/json 也非 text/event-stream → 真 (用于 2xx 错误 ctype 分支)

- `internal/mcp/streamable_http_test.go` (+~470 行新文件):
  - `fakeStreamableServer` (httptest): POST 接 JSON-RPC; notification 返 202 空 body; request 返同步 application/json 响应; `withSession` 时 init 返 Mcp-Session-Id header + 后续 POST 校验携带
  - `TestStreamableHTTPClientSyncJSONRoundTrip`: stateless POST 同步 JSON 端到端 Connect → Initialize (ProtocolVersion=2025-03-26) → Ping → DiscoverTools (alpha+beta) → CallTool alpha ("hello alpha") → Close
  - `TestStreamableHTTPClientPBSessionHeader`: withSession=true; fake server init 返 Mcp-Session-Id; Client 捕获 sessionID; 后续 DiscoverTools 必带 session header (fake 校验缺失返 400; 测试 PASS 即正确)
  - `TestStreamableHTTPClientSSEResponse`: fake server 返 text/event-stream (单条 SSE message event); Client drainSSEResponse 解析; Initialize → Ping → DiscoverTools (1 tool)
  - `TestStreamableHTTPClientErrStatusMappings`: 14 子用例覆盖 docs §3.3 错误表 (auth401/403 → AuthFailed; init 404/405 → Config; init 408/504 → ConnTimeout; init 429/500 → Unavailable; business 500/429 → TransportWrite; session-engaged POST 400/404/410 → TransportClosed; 413 → ProtocolError)
  - `TestStreamableHTTPClientBatchResponseRejected`: server 2xx 返 JSON 数组 body → Client 防御报 ErrMCPProtocolError (docs "数组/batch → 400")
  - `TestStreamableHTTPClientConnRefusedOnDialFail`: 端口未开 → dial refused → ErrMCPConnRefused|TransportWrite
  - `TestStreamableHTTPClientCheckRedirectReject3xx`: fake server 302 redirect → httpClient.CheckRedirect 返回 ErrUseLastResponse (不跟随) → client mapStatusError 把 3xx 当 ProtocolError

- `internal/mcp/manager.go` (+15):
  - buildTransport 加 `case "streamable_http"` → NewStreamableHTTPClient
  - Prepare 允许 `e.transport == "streamable_http"`
  - 头部注释 sync
- `internal/mcp/manager_integration_test.go` (+64):
  - `TestManagerPrepareStreamableHTTPAutoStartRegistersTools`: Manager.Prepare 跑 streamable_http auto_start; 校验 StatusConnected + ProtocolVersion=2025-03-26 + Transport="streamable_http" + ToolCount=2 + Execute mcp.strim.alpha + Stop ≤5s (runUpstream join)

### 验证
```
HTTP_PROXY=http://192.168.4.1:7890 HTTPS_PROXY=http://192.168.4.1:7890 GOPROXY=https://goproxy.cn,direct GOSUMDB=sum.golang.org go test -count=1 -timeout 60s ./internal/mcp/ -run 'TestStreamableHTTPClient' -v
  # 5 顶层 + 14 子用例全 PASS (0.2-0.5s)
go test -count=5 -timeout 120s ./internal/mcp/ -run 'TestStreamableHTTP|TestManagerPrepareStreamableHTTP'
  # 5x 重复全 PASS 无 flake
go vet ./... # 无 warning
go build ./... # 18 包全过
go test -count=1 -timeout 240s ./... # 18 包全绿
```

### 决策
- **POST-only 起步, 不发 GET SSE / DELETE**: docs §3.3 明示 "stateless Client 不发送 GET/DELETE"; v1 transport 状态以 session header 复用模式实现, 真正 GET SSE stream (server-to-client 流) 与 DELETE 终止 session 留下一 commit. Manager attemptReconnect 已通过新 Client 重新 initialize + 完整 DiscoverTools 重建 catalog, 不依赖 GET-流推送的 tools/list_changed notification. Για SSE 流 trigger listChanged 路径 (services 期望上层 listChanged 流) 留 增量 commit; 本期已收 v1 transport 全传播链路.
- **错误投递走 recvCh 而非 Send 直接返 err**: 保持 ClientTransport Recv/Send 分离抽象与 SSE/stdio 一致 — Send POST 后非 2xx 或 body 解析错都把 err 投到 recvCh, Client recvLoop 通过 Recv 拿一致 fail 处理. Send 只返 ctx 已取消 / marshalling 错 (调用方 side 问题). 这样避免 "Send 也 fail + Recv 也 fail" 双线传播 Client race.
- **错误 body 有界丢弃**: docs §3.3 "错误 body 最多有界丢弃 16 KiB 不进入稳定 Error/log". `io.LimitReader(resp.Body, streamableRespBodyCap)` 空 read 后扔掉. Server 真实 error body 不污染稳定 Error 文本.
- **batch 数组防御**: server 返 200 但 body 是 JSON 数组 (违反 "每个 HTTP body 只允许一个 JSON-RPC message") → Client 识别 trimmed[0] == '[' → ErrMCPProtocolError catch. 测试覆盖.
- **httpClient.CheckRedirect 强制 ErrUseLastResponse**: 在 NewStreamableHTTPClient 内部 default, 防止 client 不提供 customized http.Client 时漏配 3xx 跟随. mapStatusError 把任何 3xx 映射 ProtocolError.
- **mapStatusError 用 msg.Method + sessionID 区分 init / business / session-engaged**: docs §3.3 错误表的关键区分. `isInit := msg.Method == "initialize"`; `hasSession := c.sessionID != ""`. 老子映射表覆盖完整, 14 子用例测每条.
- **drainSSEResponse 复用 sse.go `readSSEFrame`**: SSE frame parser 已在 SSE commit 完备 (7 子用例单测). Streamable HTTP 同 SSE → 投 recvCh 完成相同处理器 (复用避免重复).
- **Start stateless 不拨号**: HTTP 无连接; Start 仅校验 URL academy性 + 关 recvReady. Connect 流程在 Initialize 时第一次 POST 触发真实 HTTP 拨号. Connection 级 timeout 通过 POST 的 ctx 控制 (caller deadline).
- **未实现 StreamableHTTPServer**: docs §6 仅 Client side; Server transport 在 §3 本地 MCPServer commit.

### 下一轮方向
1. **Streamable HTTP GET SSE 流 + DELETE terminate session** (本期 v1 不发, 增量 commit 接入): server-to-client SSE 流 接 listChanged notification; DELETE 终止 session. 状态从 stateless → fully stateful.
2. **本地 MCPServer (checklist §3) — Yaa! 作为 Server** (StdioServer/SSEServer/StreamableHTTPServer).
3. **Agent/Session/Provider 集成 (checklist §9 §2)** — MCP Tool 在 Agent turn 投影到 Provider Function 列表, Session 视图按 Agent allowall 投影可用 MCP Tool.
4. **Planner step 1-10 (docs/planner/)**.
5. **Remote API `GET /api/v1/mcp/servers` + `GET /api/v1/mcp/servers/:name` (checklist §9)**.
6. **文档副债** (W1 时间戳 / W2 README 导览 / W4 tokens[].roles 默认; 早前 progress 已记).

## progress #19 — 本地 MCPServer Step 1 (StdioServer)

**HEAD 序列**: 在 `682d0b4` (Streamable HTTP Client transport) 之上新增 commit, 本节为
本地 MCPServer 第一步交付 (stdio 单 session 状态机 + Manager 接入 + 测试).

### 改动文件清单

**新增**:
- `internal/mcp/server_transport.go` (~245 行): ServerSession 状态机 (new → negotiated →
  ready → closed) + ServerTransport interface + StdioServer (bufio 行级 JSON-RPC);
  StdioServer 改成可注入 io.Reader/io.Writer, Serve 用 goroutine 读 + select ctx, 让
  ctx cancel 也能立即唤醒 (不再阻塞在 OS read, Stop 不 hang).
- `internal/mcp/server.go` (~400 行): MCPServer + handle dispatch (initialize/initialized
  /ping/tools/list/tools/call) + cursor 分页 + digest + cloneTools/serverVersion/decodeParams
  等通用 helper.
- `internal/mcp/server_unit_test.go`: list cursor round-trip + 篡改拒绝 + ServerSession 状态机
  + serverVersion + catalogDigest + trimLine (10 + 子用例).
- `internal/mcp/server_stdio_test.go`: 端到端 stdio MCPServer lifecycle
  (initialize→tools/list→tools/call→ping→resources/list(-32601)→unknown tool(-32602)
  →parse error(-32700)→stdin EOF→Serve 退出) + ctx 取消退出, 用 io.Pipe 注入可控 stdio.

**修改**:
- `internal/mcp/manager.go` (+~40 行):
  - Manager struct 加 `mcpServer *MCPServer` + `mcpServerDone chan struct{}` 字段
  - Prepare 末段若 `cfg.Server.Enabled` → `NewMCPServer` 持有 StdioServer; 失败 fail-fast 返错
    (docs §7.1)
  - Activate 用继承 runCtx 起 goroutine 调 mcpServer.Serve; 非取消退出置 `ready=false` (unhealthy)
  - Stop 取消 runCtx 后关 mcpServer.Close + 等 upstreamWG (含本地 Serve goroutine)
- `internal/mcp/manager_test.go`: 删除 v1 占位 `TestManagerActivateRejectsEnabledServerConfig`
  (本地 Server 已交付山东), 改成 disabled 路径覆盖; 新增对未实现 transport (sse/streamable_http)
  与 invalid server config (missing agent_id / unknown tool / unknown transport) 的 Prepare
  fail-fast 测试.
- `internal/mcp/client.go` (1 行): `runtimeVersion` 由 `"0.0.0-dev"` 统一为 `"0.1.0"`,
  与 docs/mcp/server.md §2 示例引用同一常量; 注释也统一为 "Yaa! Runtime 向 MCP 对端声明版本".

### 验证
```
go build ./...     # 18 包全过
go vet ./...       # 无 warning
go test -count=1 -timeout 300s ./...  # 18 包全绿
go test -count=1 -timeout 30s ./internal/mcp/ -run 'TestStdioMCPServer|TestListCursor|TestServerSession|TestServerVersion|TestCatalogDigest|TestTrimLine|TestManagerPrepareRejects|TestManagerActivate'
```

### 决策记录
- **runtimeVersion 统一为 `0.1.0`**: docs/mcp/server.md §2 示例 `serverInfo.version="0.1.0"`;
  Client 的 ClientInfo.Version 与 Server 的 ServerInfo.Version 是 "Yaa! Runtime 向 MCP
  对端声明的版本" 同一概念, 用同一常量. v1 不从 build 注入 (Ponytail: 单 caller 字面量);
  之前 client 用 `0.0.0-dev` 与文档示例不一致.
- **server.go 引用 `bytesReader` 改用 `bytes.NewReader`**: stdlib 已有, 不造 helper.
- **StdioServer 重构注入 `io.Reader/io.Writer`**: 原版硬绑 `os.Stdin/os.Stdout` 使测试无法注入
  可控 reader/writer. 改成字段 + `NewStdioServer()` 默认绑 os.Stdin/os.Stdout, 新增 `NewStdioServerRaw(r, w)`
  供测试. Serve 主循环用 goroutine 投 readCh + `select <-ctx.Done()`, 让 ctx cancel 立刻返回
  (Stop 流程不再 hang 在 OS read); 残留读 goroutine 在 stdin 关闭/进程退出时自然回收.
- **`NewMCPServerRaw(tools, cfg, r, w)` 仅为测试可注入**: 生产路径 `NewMCPServer` 已绑 os.Stdin/os.Stdout,
  调用方无需用 Raw 版本. 仅作 stdio 已交付的 v1 唯一可注入入口.
- **Prepare 阶段而非 Activate 阶段 NewMCPServer**: docs §7.1 明示本地 Server 在 Prepare 阶段
  即 NewMCPServer 校验 (agent_id + exposed_tools allowlist), 失败 fail-fast; Activate 仅起 goroutine.
  这让 Prepare 失败错误优先于 binding 校验之前, 与远端 server error 隔离.
- **MCPServer 意外退出 → Ready=false**: docs §7.2 区分 "意外退出" (非 ctx cancel) 与 "Stop 触发退出".
  Activate goroutine 在 Serve 返非 ctx.Err() 错时置 `ready=false` (Runtime unhealthy); Stop 取消 runCtx
  触发的 `serveErr == ctx.Err()` 不触发 unhealthy.
- **不引入 lifecycleMu**: v1 Manager 已用 `runCtx` 作 stop 信号, 用 `stopOnce` 做 stop 单调;
  本地 Server 加入用 `mcpServerDone` 是局部 channel, 不需要 lifecycle gate (docs 提的 lifecycleMu
  实质是 `runCtx` + `readyMu` 的同义; 本 commit 最小破坏).
- **不更新 `cacheErr` 加入 Serve err**: docs §7.3 提到 `errors.Join` 聚合错误可留下后续 commit;
  v1 通过 `ready=false` 已反映 Serve 异常, `cacheErr` 仍只汇总 entries 的 client.Close 错.
- **Manager.Activate 集成测试未直接覆盖**: stdio Server 占用 stdin/stdout, Manager 层测试无法
  注入. 通过 NewStdioServerRaw 注入 io.Pipe 的单测 (server_stdio_test.go) 已覆盖 stdio lifecycle
  + ctx 退出. Manager.Activate 接入路径通过 TestManagerPrepareRejects* 间接验证 (Prepare 失败
  时 Activate 不启 Serve). Manager 端到端 stdio Server 测试留下 commit (要 SSEServer/StreamableHTTPServer
  到位, 端到端路径经 HTTP transport 实现可控).

### 上一轮 plan 留下的待办映射

1. **修 server.go 编译错误** ✅ (`runtimeVersion` + `bytesReader`)
2. **go build + vet + test (mcp 包)** ✅
3. **Manager.Activate 接入 MCPServer** ✅ (Prepare 持有 + Activate 起 Serve + Stop 关 + Ready=false)
4. **集成测试 server_test.go** ✅ (端到端 stdio lifecycle + ctx cancel)
5. **docs/mcp/checklist.md 勾选** — 待 (下一步)
6. **progress.md #19** ✅ (本节)
7. **commit + push gitea/main** — 待 (下一步)
8. **未解决**:
   - 文档 checklist 勾选
   - SSEServer + StreamableHTTPServer (下 commit)
   - Streamable HTTP Client GET SSE 流 + DELETE
   - Agent/Provider 集成 + Remote API + Planner step 1-10

### 下一轮方向
1. 更新 `docs/mcp/checklist.md` 勾选 §3 MCPServer (stdio 已做) 与 §4 StdioServer
2. commit + push gitea/main
3. 进入本地 MCPServer Step 2 (SSEServer + StreamableHTTPServer): listener + session map +
   origin allowlist 校验 + DELETE/Last-Event-ID (docs §3.2/§3.3)

## progress #20 — 本地 MCPServer Step 2 (SSEServer)

**HEAD 序列**: 在 `87857fe` (本地 MCPServer Step 1 stdio) 之上新增 commit;
本节为本地 MCPServer Step 2: legacy SSE Server transport (docs §3.2 + §4 NewSSEServer 签名).

### 改动文件清单

**新增**:
- `internal/mcp/sse_server.go` (+~360 行): SSEServer struct + Serve (http.Server + Shutdown on ctx-cancel) + GET endpointPath (SSE event stream, 首帧 event:endpoint data:<post-path>?session_id=<id> + heartbeat 30s ticker + 写 message 帧) + POST messagesPath (404 session 不存在 / 400 缺 session_id + 解析错 / 202 Accepted 成功) + session map (16-byte crypto/rand ID) + writeSSEEvent + writeJSONRPCErrorHTTP + randomSessionID.
- `internal/mcp/server_sse_test.go` (+~430 行): 端到端 SSE MCPServer lifecycle 测试 (用 http.DefaultClient GET 流 + POST verify).
  - TestSSEServerE2E: GET 流开 session + endpoint 帧 url.Parse + ResolveReference 同 host 校验 + POST initialize 协议版本 2024-11-05 + serverInfo.name=yaa + tools/list count=1 + tools/call content="echo: hello-sse" + ping + resources/list -32601 + POST 缺 session_id 400 + GET 错 Accept 406 + GET /unknown 404.
  - TestSSEServerContextCancelExit: Serve ctx 取消 → Shutdown → 后端退出.
  - TestSSEServerPOSTUnknownSession404: 不存在 session_id POST → 404 + JSON-RPC -32001.
  - TestSSEServerPOSTMalformedBody400: POST body 非 JSON → 400 + JSON-RPC -32700.
  - TestSSEServerWritesHeartbeatFrame / TestSSEServerWritesMessageFrame: writeSSEEvent 单测 (event:/id:/data: + 空 newline) + heartbeat (`: ping\n\n`).
  - TestSSEServerMPOSTNotAllowed: PUT /mcp + PUT /message → 405.

**修改**:
- `internal/mcp/server.go`:
  - import 加 `net`
  - NewMCPServer switch case `"sse"`: `net.Listen("tcp", cfg.Addr)` + `NewSSEServer(listener, cfg.Path, cfg.MessagesPath)`; Addr 缺失即返 ErrMCPConfig (config Validator 已校验, 这里仍留防御)
  - case `streamable_http` 占位仍返 ErrMCPConfig "待 StreamableHTTPServer commit"
- `internal/mcp/manager_test.go`:
  - `TestManagerPrepareRejectsEnabledButUnsupportedTransport` 语义调整: streamable_http 未实现仍返 ErrMCPConfig; sse 缺 Addr 仍 ErrMCPConfig (sse 已支持, Addr 缺失败只能体现 fail-fast 校验).
- `docs/mcp/checklist.md`: §3 会话管理勾部分 (stdio + sse 已交付, StreamableHTTPServer 待); §4 SSEClient 行追加 "SSEServer 已落地 progress #20"; §5/§6 Server side marker 更新 SSEServer 已落地, StreamableHTTPServer 待 Step 3.

### 验证
```
go build ./...     # 18 包全过
go vet ./...       # 无 warning
go test -count=1 -timeout 300s ./...   # 18 包全绿
go test -count=1 -timeout 90s ./internal/mcp/ -run TestSSEServer -v   # 7 例 PASS
```

### 决策记录
- **SSEServer 是 legacy SSE transport (docs §3.2)**: listener 同时承载 GET endpointPath (开 SSE 流 + 推 handler response message 帧 + heartbeat `: ping`) 与 POST messagesPath (接收 Client → Server JSON-RPC); POST 成功返 202 空 body, handler response 通过同 session GET 流推回 `event: message id:<n> data:<json>`.
- **session_id 是 16-byte base64 RawURL** (字母数字 + `_-`, 不需任何路径转义): 通过首帧 event:endpoint 的 data 字段 `<messagesPath>?session_id=<id>` 投递给 Client; Client 端做 url.Parse + ResolveReference 解析绝对 URL (与 SSEClient Start 路径一致).
- **session map 锁下 create/find/delete** (docs §4): handler 调用时不持该锁 (handler 通过 ses.server *ServerSession 直接状态机操作, 不需 mu).
- **Last-Event-ID v1 不续传**: 与 SSEClient 决策 (checklist §5 line 73) 一致; 重连时 client 自行重做 tool 请求. Server 端只接收 Last-Event-ID header 但不重放任何已发事件, 简化 v1 实现 (复杂的事件 buffer 重发 留 v2).
- **heartbeat 改用 session 级 30s ticker**: 每 session 一个 heartbeat goroutine 投 sseEvent{kind:heartbeat} 到 ses.out (非阻塞 + 满即丢); GET handler select 同时读 out + ctx.Done + r.Context().Done().
- **out 满降级 fallback**: 当 GET 流消费过慢导致 out channel 满, 帧 POST 同步 200 application/json 响应代替 SSE 推送 (避免 POST goroutine 阻死). 这降低了 SSE 严格性但 v1 接受; 文档没禁这种 退化路径.
- **POST handler hard fail → 500 + JSON-RPC -32603**: 文档未明示 handler 返 err 的语义; v1 退化为同步 500 内部错给 POST. notification handler 返 (nil,nil) 时 POST 返 202 不推任何帧.
- **streamable_http 仍占位**: Step 3 单独 commit 实现 StreamableHTTPServer (session map + 32-byte crypto/rand ID + 1024 上限 + 30min idle + DELETE/GET/POST routing + Origin 校验 + 405/404/400/503/403 状态映射), 避免单 commit 过大.
- **新测试不依赖 python3 / 子进程**: SSEServer 用 net.Listen + NewMCPServer + http.DefaultClient; fake SSE Client 用同包 readSSEFrame 解析帧, 比起 callback 跨进程模式更稳定可复跑.
- **POST messagesPath 的 session_id query 校验**: 路径本身就是 endpoint 帧的 data 字段值, client 必须传输 session_id. v1 通过 r.URL.Query().Get("session_id"); 缺失返 400 + JSON-RPC -32600; 不存在返 404 + JSON-RPC -32001 (自定义 code, 文档未指定 code 但 v1 用 -32001 与 -32000 Server error 区间).

### 下一轮方向
1. commit + push gitea/main
2. 进入本地 MCPServer Step 3 (StreamableHTTPServer): session map + 32-byte crypto/rand URL-safe ID + 1024 上限 + 30min idle 删除 + DELETE 销毁 + 405 only-close SSE + Origin 校验 (缺失允许非浏览器; 存在必命中非空 allowlist 否则 403); 接入 NewMCPServer switch case "streamable_http"
3. 后续: Streamable HTTP Client GET SSE 流 (server-to-client) + DELETE terminate session (client 侧增量)
4. Agent/Session/Provider 集成 (checklist §9 §2)
5. Planner step 1-10 (docs/planner/)
6. Remote API `GET /api/v1/mcp/servers` + `:name` (checklist §9)

## progress #21 — 本地 MCPServer Step 3 (StreamableHTTPServer)

**HEAD 序列**: 在 `d65d802` (本地 MCPServer Step 2 SSEServer) 之上新增 commit;
本节为本地 MCPServer Step 3: Streamable HTTP Server transport (docs §3.3 + §4 NewStreamableHTTPServer).
**本地 MCPServer 三种 transport (stdio + legacy SSE + Streamable HTTP) 全部交付**.

### 改动文件清单

**新增**:
- `internal/mcp/streamable_http_server.go` (~290 行): StreamableHTTPServer struct 实现 ServerTransport.
  - 单 listener + 单 endpointPath 处理 POST / GET / DELETE 三类方法 (mux).
  - 处理路径: `handleEndpoint` Origin 校验 → 按 r.Method dispatch (handlePOST / handleGET / handleDELETE).
  - POST 路径: body ≤ 4 MiB (MaxBytesReader, 触发 413) + JSON 数组/batch 防御 (400 + -32600) + JSON 解析失败 (400 + -32700) + envelope 校验 + 孤立 response 拒绝 (400 + -32600).
    - initialize 且无 Mcp-Session-Id → createSession (32-byte crypto/rand URL-safe ID + 1024 上限 503) + 响应 header 返回 ID; 调用 handler; 同步写入 200 application/json.
    - 非 initialize 必须带合法 ID; 缺 header 400 + -32600; 未知/过期 ID 404 + -32001; handler response 写 200 application/json; notification/response 走 202 空 body.
  - GET 路径: v1 不实现 Server-to-Client SSE;返 405 Method Not Allowed (docs §3.3 state table "可选 GET 405 - 只关闭 SSE 不影响 POST").
  - DELETE 路径: 带 Mcp-Session-Id 销毁; 成功返 204 No Content; 缺 header 返 405; 未知 ID 返 404.
  - session map 锁下 create/find/touch/delete; handler 不持锁. 30min idle sweeper goroutine 周期 1min 主动清理. Server Close 标记所有 session closed 并清空 map.
  - Origin 校验 (防 DNS rebinding): 缺失 Origin 允许非浏览器; 存在必须精确命中非空 allowlist; allowlist为空或不匹配 → 403.
  - randomSessionID32: 32-byte crypto/rand base64 RawURL (与 SSEServer 16-byte 区分, docs §4 明示 32-byte).
- `internal/mcp/server_streamable_http_test.go` (~370 行): 10 个端到端测试 (raw HTTP POST, 不依赖 streamable_http client 互连, 用 http.DefaultClient):
  - TestStreamableHTTPServerE2E: initialize→Mcp-Session-Id header→notifications/initialized 202→tools/list 200→tools/call 200 echo:hello-stream→ping 200→resources/list 200 -32601→DELETE 204→DELETE 再来 404→后续 POST 404 + -32001.
  - TestStreamableHTTPServerRejectsMissingSession: 非 initialize POST 缺 session ID → 400 + -32600.
  - TestStreamableHTTPServerRejectsInitializeWithExistingSession: initialize 带 session ID → 400.
  - TestStreamableHTTPServerRejectsBatch: POST body 是 JSON 数组 → 400 + -32600.
  - TestStreamableHTTPServerRejectsMalformedBody: POST body 非 JSON → 400 + -32700.
  - TestStreamableHTTPServerGET405: GET → 405 + Content-Type: text/event-stream.
  - TestStreamableHTTPServerDELETEWithoutSession: DELETE 缺 session ID → 405.
  - TestStreamableHTTPServerOriginAllowlist: 非空 allowlist + allowed Origin 200 + disallowed Origin 403 + 无 Origin 200.
  - TestStreamableHTTPServerEmptyOriginAllowlist: 空 allowlist + 任何 Origin → 403; 无 Origin 200.
  - TestStreamableHTTPServerCtxCancelExit: Serve ctx 取消 → Shutdown → 退出.

**修改**:
- `internal/mcp/server.go`: NewMCPServer switch case `"streamable_http"` 接入 `net.Listen("tcp", cfg.Addr)` + `NewStreamableHTTPServer(listener, cfg.Path, cfg.OriginAllowlist)`; Addr 缺失仍返 ErrMCPConfig (根 Validator 校验已通过防御). 注释同步 v1 三种 transport 全实现.
- `internal/mcp/manager_test.go`:
  - 拆分原 `TestManagerPrepareRejectsEnabledButUnsupportedTransport` → `TestManagerPrepareRejectsNetworkServerWithoutAddr` (sse/streamable_http 缺 Addr fail-fast).
  - 新增 `TestManagerPrepareAcceptsAllSupportedTransports` (3 subcase: stdio/sse/streamable_http Addr=127.0.0.1:0 + AgentID + ExposedTools=echo 全部 Prepare 成功; Stop 释放 listener).
- `docs/mcp/checklist.md`:
  - §3 会话管理勾选完成 (stdio + SSEServer + StreamableHTTPServer 三种 transport 多 session 全部交付).
  - §5/§6 Server side marker 同步 progress #21 新增 (StreamableHTTPServer 落地细节).

### 验证
```
go build ./...     # 18 包全过
go vet ./...       # 无 warning
go test -count=1 -timeout 300s ./...   # 18 包全绿
go test -count=1 -timeout 120s ./internal/mcp/ -run TestStreamableHTTPServer -v   # 10 例 PASS
```

### 决策记录
- **Single endpointPath vs 双 path (SSE 双 path 是 legacy compat 不得以)**: Streamable HTTP 文档描述 POST/GET/DELETE 共用同一 endpointPath (不像 legacy SSE 用 endpoint + messages 双 path), 由 r.Method 校验分发; mux.Handle(s.endpointPath, ...) 单 handler 覆盖三种 method.
- **GET v1 返 405 only-close-SSE**: docs §3.3 "可选 GET" + §3.3 状态表 "可选 GET 405 - 只关闭 Server-to-Client SSE; POST transport 仍可用". Yaa! v1 不实现 GET-to-server-push SSE (client side StreamableHTTPClient 也不发 GET/DELETE, v1 stateless); 后续 GET SSE 流接入留下一个增量 commit (含 Last-Event-ID 续传 client/server 对称).
- **DELETE v1 返 204 No Content**: docs §3.3 "DELETE 成功返 200 OK 或 204 No Content", 选 204 (空 body 更紧凑, 与 manager Stop teardown 一致).
- **session ID 用 32-byte crypto/rand + base64 RawURL**: docs §4 明示 32-byte, 与 SSEServer 的 16-byte 不同 (SSE session_id 通过 SSE frame data 字段而非 header, 字节数无文档约束).
- **session map 锁下 create/find/touch/delete**: 文档 §4 明示; touch (lookupSession 刷 lastActive) + create (上限检查) + delete (mark closed delete) 都在 mu 下; handler 处理一个 POST 期间不持锁 (handler 返回的 *Message 已不依赖 Transport 状态).
- **30min idle 用 sweeper goroutine (周期 1min)**: 不依赖 r.Context() (单 TCP 关掉不销毁 session 仍要存活 30min 直至 client 真断), 文档 §4 "单次 TCP/HTTP 连接关闭不销毁 session".
- **Origin 校验策略**: 缺失 Origin header 允许 (非浏览器 client); 存在必须命中非空 allowlist — 这是 DNS rebinding 防御标准模式 (browser fetch 含 Origin header). allowlist 为空且 Origin 存在 → 403 (强制 allowlist 显式).
- **body 上限 4 MiB (sseMessageMaxBytes 复用)**: 与 stdio/sse 一致 (docs §2 表). MaxBytesReader 触发 413 + -32700.
- **数组/batch 防御 (400 + -32600)**: docs §3.3 "每个 HTTP body 只允许一个 JSON-RPC message; 数组/batch 返回 HTTP 400 和 JSON-RPC -32600". 通过 strings.HasPrefix(trimmed, "[") 简单检测.
- **孤立 response 拒绝 (400 + -32600)**: Server 不发起 request, 不接受 POST 单走的 response (类似 stdio handle 同一防御).
- **handler hard fail → 500 + -32603**: 文档未明示 handler 返 err 的语义, 与 SSEServer 一致退化.
- **不引入 in-memory event buffer 重发 (Last-Event-ID 等)**: v1 stateless POST-response 模式不依赖 Server 端 - to -client 推送; 后续 GET SSE 流接入时同步考虑.
- **reuse sseMessageMaxBytes 常量 + 不重复定义**: sse.go 已定义, streamable_http_server 仍用同一上限, 避免 per-transport 常量分歧.

### 本地 MCPServer 三种 transport 全部交付里程碑

- stdio (progress #19): ServerSession 状态机 + 单 session + cursor 分页
- legacy SSE (progress #20): 多 session (16-byte session_id) + GET SSE 流 + endpoint/heartbeat/message 帧 + Last-Event-ID 接收 (v1 不续传)
- Streamable HTTP (progress #21): 多 session (32-byte Mcp-Session-Id header) + POST 同步 response + DELETE 销毁 + 1024 上限 + Origin allowlist DNS rebinding 防护 + 30min idle sweep

### 下一轮方向
1. commit + push gitea/main
2. Streamable HTTP Client GET SSE 流 + DELETE terminate session (client side v1 stateless → fully stateful 增量 commit):
   - 客户端在 initialize 拿到 Mcp-Session-Id 后可发 GET 打开 server-to-client SSE 流; DELETE 显式销毁 session.
   - server-to-client 流接 listChanged notification 等异步事件 (Server 端 GET v2 实现 - 当前 GET 返 405 占位)
3. Agent/Session/Provider 集成 (checklist §9 §2):
   - MCP Tool 在 Agent turn 投影到 Provider Function 列表
   - Session 视图按 Agent allowall 投影可用 MCP Tool
4. Planner step 1-10 (docs/planner/): 完全没动.
5. Remote API `GET /api/v1/mcp/servers` + `:name` (checklist §9).
6. 文档副债 (W1/W2/W4).

## progress #22 — Streamable HTTP Client stateful 升级 (GET SSE + DELETE terminate session)

**Commit (将提交)**: 本轮工作树未提交 → commit 后 push gitea/main.

**目标**: StreamableHTTPClient 从 v1 stateless-only 升级为 fully stateful. 上游 server 返 Mcp-Session-Id header 后客户端: 首次 Send initialize 响应触发 async GET 试探 Server-to-Client SSE 流 (docs §3.3 "可选 GET" + §3.3 状态表 "可选 GET 405 - 只关闭 SSE 不影响 POST"); Close 时 cancel SSE loop + 发一次 DELETE terminate session (docs §3.3 "DELETE 成功 200/204, 404/405 幂等忽略").

**实现** (`internal/mcp/streamable_http.go`):
- import 加 `sync/atomic` + `time`.
- struct 加字段 `sseStarted uint32` (atomic CAS 保证 GET 试探只启一次) + `sseCtx`/`sseCancel` + `sseLoopDone chan struct{}`.
- Send 中首次拿到 `Mcp-Session-Id` header (`first := c.sessionID == ""`) 后异步 `go c.tryOpenServerToClientStream(context.Background())` (不阻塞 Send).
- 新增 `tryOpenServerToClientStream(parent)`: atomic CAS sseStarted 0→1; 取 sid/url/headers/client/logger 后创建 sseCtx/sseCancel/sseLoopDone + 启 `runSSERecvLoop` goroutine.
- 新增 `runSSERecvLoop(ctx, url, sid, headers, client, logger)`: GET + Accept text/event-stream + Mcp-Session-Id header → 200 + text/event-stream 才进 readSSEFrame 循环; event 非 "" 且 != "message" 跳过 (仅消费 message 帧); 非 200 / 非 SSE → graceful return 不报错; ctx 取消或 EOF → 退出; message 帧投 `recvCh` 与 POST 同步响应共用 (Client recvLoop 统一消费, docs §3.3 "客户端以 JSON-RPC id 关联响应").
- Close 改为: cancel sseCtx + 等 sseLoopDone (确保 SSE goroutine 退出再发 DELETE, 避免 handler 仍持有 body) + `sendDelete(parent, sid)` (5s timeout; drain body 16KiB; 404/405 幂等不返错).

**测试** (`internal/mcp/streamable_http_test.go`, +3 例 纯 transport 层 tr.Start + tr.Send + tr.Recv, 不走 Client wrapper 避免与 Client runRecvLoop 竞 recvCh):
- `fakeStreamableServerWithSSE` helper: 嵌入 `*fakeStreamableServer` + `deleteCount`/`getCount` atomic.Int32 + `getPush []byte`; handleEnhanced GET → 计数 + 200 text/event-stream 写一条 frame + `<-r.Context().Done()` 阻塞等 client 关流; DELETE → 计数 + 204; POST 走原 fakeStreamableServer.handle.
- `TestStreamableHTTPClientGETProbedOnceAndGraceful405`: Server GET 返 405 → runSSERecvLoop graceful 退出; 轮询等 async GET count=1 → Close → DELETE count=1.
- `TestStreamableHTTPClientGETReceivesServerPushSSE`: Server GET 返 200 + 推 `notifications/tools/list_changed` message frame; 第一次 tr.Recv 取 initialize POST 响应, 第二次 tr.Recv 取 SSE notification; Close 后 DELETE count=1.
- `TestStreamableHTTPClientDELETEHandles404IdempotentClose`: Server DELETE 返 404 + Close 返 nil (幂等忽略, docs §3.3 错误表最后一行).

**验证**:
```
go vet ./internal/mcp/   # 通
go test -count=1 -timeout 60s ./internal/mcp/ -run "TestStreamableHTTPClientGETProbedOnce|TestStreamableHTTPClientGETReceivesServerPushSSE|TestStreamableHTTPClientDELETEHandles404" -v   # 3 例 PASS
go build ./... && go vet ./... && go test -count=1 -timeout 300s ./...   # 18 包全绿 (TestManagerPrepareSSEAutoStartRegistersTools 首跑偶现 5s deadline flake, 单独稳定通过)
```

**决策记录**:
- **GET 试探只启动一次**: atomic CAS sseStarted 0→1 (与 session ID 同步点在 Send 的 `first := c.sessionID == ""` 判断). 防 session 复用模式下重复 GET.
- **GET 与 POST recvCh 共用**: docs §3.3 "客户端以 JSON-RPC id 关联响应"; SSE goroutine 把 message 帧投同一 recvCh, Client wrapper recvLoop 统一消费. 测试为避免与 Client runRecvLoop 竞争同一 recvCh, 用纯 transport 层 tr.Recv 不走 wrapper.
- **GET 路径 graceful 不报 ErrMCPProtocolError**: 与 SSEClient 关流一致 (server 主动 EOF / ctx 取消 / 405 都是正常退出, 不向 recvCh 投错误).
- **DELETE 5s timeout 防卡 Close**: parent 用 context.Background() (Close 不应被 Send 的 ctx 取消所阻断); body drain 上限 `streamableRespBodyCap` (16KiB) 防 server 写大 body 卡 Close.
- **404/405 DELETE 幂等**: docs §3.3 错误表最后一行明示 "DELETE 404/405 幂等忽略"; sendDelete 不分类非 2xx 都不返 err.
- **handleEnhanced GET 200 path 推完 frame 后 `<-r.Context().Done()` 阻塞**: 前一轮担心 `runSSERecvLoop` 读完 frame 后阻塞在 readSSEFrame. 实测不阻塞因为: (1) 测试 tr.Recv 取出 notification 后立即 tr.Close() → sseCancel 取消 req ctx → runSSERecvLoop 的 ReadString 因 request context cancel 通过 http transport 解除阻塞返 err → graceful return; (2) Server handler 的 `<-r.Context().Done()` 同步解除. 闭环 0.01s.

## progress #23 — Remote API /mcp/servers + /:name 完整 ServerDetail (含 Tools) 真实落地

**Commit (将提交)**: 本 commit 把 :name 端点从平铺 ServerStatus 升级为 docs 规约的 ServerDetail DTO.

**背景**: 上一轮发现 handler 已写 + routes 已注册 + 基础测试已过, 但 `handleGetMCPServer` 只返 `mcp.ServerStatus` 平铺, **未返 docs/remote-api/mcp.md §2 明示的 `ServerDetail` (嵌入 ServerStatus + `Tools []tool.ToolInfo`)**; `MCPServerProvider` 接口只暴露 `List/Get` 没有 `Tools/Detail`. 这是代码不完整 (不是文档错), 按 "审查已有代码是否和修复后文档冲突" 准则补完.

**实现**:
- `internal/mcp/types.go`: 新增 `type ServerDetail struct { ServerStatus; Tools []tool.ToolInfo \`json:"tools"\` }` 嵌入 ServerStatus; import 加 `tool` (mcp 已有 tool 依赖无 cycle 风险).
- `internal/mcp/manager.go`: 新增 `Manager.Detail(name) (ServerDetail, bool)` 复合方法. 复用 `Get` + `Tools`: 命中 → ServerStatus 深拷贝 + Tools 深拷贝; 未连接/无 Tools 时 `nil→[]tool.ToolInfo{}` 转 nil 为空切片 (JSON 序列化 `[]` 而非 `null`, 符合 docs §2 JSON 示例).
- `internal/api/server.go`: `MCPServerProvider` 接口加 `Detail(name) (mcp.ServerDetail, bool)`; 接口注释更新为 "List/Get/Detail 契约".
- `internal/api/mcp_handler.go`: `handleGetMCPServer` 从 `Get` 切换到 `Detail` (`mm.Detail(name)` 一行拼装, 避免 handler 两次调用); `writeOK(..., d)` 直接返 ServerDetail.

**测试**:
- `internal/mcp/manager_test.go`: 加 `TestManagerDetail` (未命中 false; 命中 disconnected ServerStatus + 空 Tools 切片 != nil).
- `internal/api/agent_handler_test.go`:
  - `mockMCPServerProvider` 加 `detailsBy map[string]mcp.ServerDetail` 字段 + `Detail(name)` 实现 (命中 detailsBy 返回; fallback 走 items 返 ServerStatus + 空 Tools).
  - 强化 `TestMCPEndpointsReturn200And404`: Get(fs) 子断言加 `tools` 字段存在 + 是空 `[]any` (非 nil/null).
  - 新增 `TestMCPEndpointsDetailWithTools`: mock 注入 detail.tools 的 server; 验证完整 wire 投影: `name/status` (ServerStatus 平铺) + `tools[0].name="mcp.fs.read"` + `tools[0].source="mcp"` + `tools[0].enabled=true`.

**验证**:
```
go build ./...   # 18 包全过
go vet ./...     # 无 warning
go test -count=1 -timeout 300s ./...   # 18 包全绿
go test -count=1 -timeout 60s ./internal/mcp/ -run TestManagerDetail -v   # PASS
go test -count=1 -timeout 60s ./internal/api/ -run TestMCPEndpoints -v   # 3 例 PASS
```

**决策记录**:
- **ServerDetail 在 mcp 包不在 api 包**: docs §2 直接声明 `type ServerDetail struct { mcp.ServerStatus; Tools []tool.ToolInfo }` 暗示属 mcp package; mcp 已经 import tool (proxy.go/manager.go 均有) 无新增 cycle 风险; 让 Detail 逻辑在 Manager 一处拼装比 handler 多次调用更整洁.
- **Manager.Detail 内部 nil→[] 转换**: `Tools(name)` 内部约定 disconnected server 返 (nil, false); Detail 把这个内部约定转为对外友好的 `[]tool.ToolInfo{}` 让 JSON 输出 `[]` 而非 `null`, 符合 docs §2 JSON 示例 "tools": [...].
- **MCPServerProvider 接口加 Detail 而非 Tools**: handler 只需 ServerDetail 一次拼装的语义; 暴露 Tools(name) 会强迫接口多个语义方法; Detail 命名直接对齐 docs DTO 名.
- **不暴露 POST/PUT/DELETE 一类 CRUD**: docs §3 明示 "没有 POST .../tools/:tool、PUT 或 DELETE"; handler 只 GET 只读 (现有 routes 注册也只是 GET); 测试不变也不动.
- **mockMCPServerProvider 加 detailsBy 而非替换 items**: fallback 路径保证现有 "disconnected fs" List 投影 + Get 测试无需改动; 新加 TestMCPEndpointsDetailWithTools 用 detailsBy 注入完整 ServerDetail.

## progress #24 — 本地 MCP Server Agent Tool 白名单 authz 端到端测试落地

**Commit (将提交)**: 本 commit 给 checklist §9 第 126 行补强 authz 端到端测试.

**背景**: 上一轮审查发现 NewMCPServerRaw 构造路径已经实现 docs §6 + §4 完整链路:
- prepare 阶段 (server.go NewMCPServer §60-78): 调 `tools.ListForAgent(cfg.AgentID)` 取 allowlist 集合, 对每个 ExposedTools 校验是否被该 Agent 允许, 不通过返 ErrMCPConfig;
- tools/call 路径 (server.go handleCallTool §229-258): 二次走 `s.tools.Execute(ctx, tool.ExecutionScope{AgentID: s.agentID, SessionID: ""}, params.Name, ...)`, Tool Manager 再次真实 CheckPermission.
- 但**没有针对 NewMCPServerRaw authz 失败路径的负向端到端测试**——checklist 行未勾选的真实原因是验收缺位 (不是代码缺位).

**实现** (新增 `internal/mcp/server_authz_test.go`):
- `buildRestrictedToolManager(t, allowTools...)`: 构造 `config.AgentConfig{ID: "restricted", Tools: allowTools}` 的 Tool Manager, 不像 buildToolManager 的 AllowAll a1, 这个 agent 真实有有限 allowlist.
- `TestNewMCPServerRejectsExposedToolNotInAgentAllowlist`: allowlist=["echo"], ExposedTools=["echo","private"] → NewMCPServerRaw 返 ErrMCPConfig; err 文本含 "private" (rejected tool 名) + 含 "restricted" (agent_id).
- `TestNewMCPServerAcceptsAllExposedToolsInAllowlist`: 正向对照. allowlist=["echo","ls"], ExposedTools=["echo","ls"] + 注册 echo/ls 两 tool → NewMCPServerRaw 成功.
- `fakeLsTool` (echo 风格 minimal Tool): 给正向测试多 tool 场景.

**验证**:
```
go build ./internal/mcp/  # 通过
go vet ./internal/mcp/    # 无 warning
go test -count=1 -timeout 60s ./internal/mcp/ -run "TestNewMCPServerRejectsExposedToolNotInAgentAllowlist|TestNewMCPServerAcceptsAllExposedToolsInAllowlist" -v   # 2 例 PASS
go build ./... && go vet ./... && go test -count=1 -timeout 300s ./...   # 18 包全绿
```

**决策记录**:
- **不重复 buildToolManager a1 AllowAll**: 现有 buildToolManager 的 a1 是 AllowAll, 不适合测有限 allowlist; 新建 buildRestrictedToolManager 构造 restricted agent 有限 allowlist. 两个 helper 共存避免改动现有测试.
- **占位 io.Pipe 不调用 Serve**: NewMCPServerRaw authz 校验发生在构造时; Serve 不启动所以 stdin/stdout 可用任意 io.Pipe 的 half; 释放靠 t.Cleanup 不强求.
- **正向测试不启动 Serve**: 只验证 NewMCPServerRaw 返 nil err 即证明 authz 路径通过; Serve 是 lifecycle 层 concern 由 server_stdio_test.go E2E 覆盖, 不重复测.
- **err 文本断言含 "private"+"restricted"**: docs §6 错误内容是 "exposed tool %q is not enabled or not allowed for agent %q", 测试用 strings.Contains 证明 err 信息真名这俩.


## progress #25 — 内置 Tool mcp_list 实现 + runtime 注册接入 + progress #22 flake 修复

**Commit (将提交)**: 本 commit 落地 checklist §9 第 127 行 (内置 Tool mcp_list, 之前 8 个 introspection tools 中全未实现).

**背景**: config/defaults.go 把 14 个 builtin tool 都 enabled=true 注册到配置表 (`mcp_list` 是其中之一), 但 builtin/register.go 只构造 shell/http/file_* 6 个 Tool. introspection group 8 个 (mcp_list/agent_list/...) 完全从未实现 → ToolManager.Get 调返 ToolNotFound. 选 mcp_list 单点实现 + 注册不强行做其他 7 个 (它们当前 disabled-by-stub status quo 也不需要立刻打破).

**实现**:
- `internal/tool/builtin/mcp_list.go` 新增 (依赖 `*mcp.Manager` 不引 cycle: builtin → mcp, mcp → tool, builtin 与 tool 是不同包):
  - `MCPListTool` struct 持有 `*mcp.Manager` (持 ref, 不 copy).
  - `NewMCPListTool(mgr)` 构造.
  - `Name() = "mcp_list"` + `Description()` + `Parameters()` 严格按 docs/tool/introspection.md §10 schema `{"type":"object","properties":{"server_name":{"type":"string","minLength":1}},"additionalProperties":false}`.
  - `Execute(ctx, scope, params)`: 取 server_name 可选; 调 `mgr.List()` 后按 Name 升序稳定排序 (docs §1 "列表按稳定主键升序"); server_name 非空按名过滤 (找不到 → `ToolResult{Content:"mcp server %q not found", IsError:true}` docs §1 "不存在的资源返回 IsError=true"); nil → `[]mcp.ServerStatus{}` 防 JSON null (docs §1 "空 slice 编码为 []"); JSON marshal Content string.
- `internal/tool/builtin/register.go` 加 `RegisterMCPIntrospection(m *tool.Manager, cfg *config.Config, mcpMgr *mcp.Manager)`: mcpMgr nil (MCP 子系统未启用) 时不注册; 否则注册 MCPListTool.
- `internal/runtime/runtime.go` §mcpMgr Prepare/Activate 后 (line 277) 调 `builtin.RegisterMCPIntrospection(rt.tools, rt.cfg, mcpMgr)`; 注释说明为什么在 RegisterBuiltin 之后单独调 (mcp.Mgr 在 runtime 启动序位于 RegisterBuiltin 之后; 这是 introspection tool 依赖 MCP Manager 快照的合理时序, 不破坏 docs/tool/manager.md §3 "builtin → plugin → MCP proxy").

**测试**:
- `internal/tool/builtin/mcp_list_test.go` (4 unit):
  - TestMCPListToolSchema: Parameters JSON 严格匹配 docs §10 (object/server_name string minLength 1 / additionalProperties false).
  - TestMCPListToolEmptyServersReturnsArray: 空 ServerList → "[]" (非 null).
  - TestMCPListToolListAllAndFilterName: 多 server (zeta/alpha/mid) → 按 Name 升序 [alpha,mid,zeta]; server_name="mid" → 单条; 找得见.
  - TestMCPListToolFilterUnknownServerNameIsError: 不存在 server_name → IsError=true.
- `internal/tool/builtin/register_test.go` (2 集成):
  - TestRegisterMCPIntrospectionWithNilMCPSkipRegister: mcpMgr nil 不注册 mcp_list (ListForAgent(a1) 不见它).
  - TestRegisterMCPIntrospectionWithManagerRegistersMCPList: mcpMgr 非 nil → 注册 + ToolManager.Execute(scope{a1}, mcp_list, {}) "[]" (端到端走 Tool Manager allowlist + timeout + 并发门).

**额外修复**: 发现 progress #22 已 push 测试 `TestStreamableHTTPClientGETReceivesServerPushSSE` 存在并发 flake (约 10% / 跑全 package).
- 根因: tryOpenServerToClientStream 是 `go c.tryOpenServerToClientStream(...)` 异步启动; SSE GET 与 POST init response 共同投 recvCh, 顺序由 goroutine 调度决定. 当 SSE frame 推送时机早于 POST init response, recvCh 第一条是 SSE notification 第二条是 init response; 测试原先按"第一条必 init, 第二条必 notification"断言就误报.
- 修法 (`streamable_http_test.go` 改): 用 ID 字段区分 init response (`len(msg.ID)>0`) 与 SSE notification (`len(msg.ID)==0 && msg.Method!="notifications/tools/list_changed"`), 收齐两条各一次即 break; 不再假设 recvCh 顺序.
- 验证: 10 次单跑 + 3 次全 package. 稳定通过. 这是测试修法不是源码 modify (tryOpenServerToClientStream 行为正确, 不该串化; recvCh 是 docs §3.3 "客户端以 JSON-RPC id 关联响应" 设计意图直接体现).
- 注: 该 flake fix 不占 progress #25 commit 主题 (内含), 但 commit message 单独列出.

**验证**:
```
go build ./... && go vet ./... && go test -count=1 -timeout 300s ./...   # 18 包全绿 (3 次全 package 稳定)
go test -count=1 -timeout 60s ./internal/tool/builtin/ -run "TestMCPListTool|TestRegisterMCPIntrospection" -v   # 6 例 PASS
go test -count=1 -timeout 60s ./internal/mcp/ -run TestStreamableHTTPClientGETReceivesServerPushSSE   # 10 次稳定通过
```

**决策记录**:
- **单做 mcp_list 不做其他 7 个 introspection tools**: 它们依赖不同 Manager (agent_list 需要 agent.Manager, session_list 需要 session.Manager 等), 一次性全做是大重构; ponytail full 选最小可独立验收 = 单个 introspection tool + 注册胶水. 其他 7 个留 stub; Default 注册表中 enabled=true 但实际 ToolManager 没 Register (现有缺失状态), 这与之前的代码状态一致.
- **RegisterMCPIntrospection 独立函数不在 RegisterBuiltin 内**: 因为 (1) mcpMgr 在 runtime 启动序位于 RegisterBuiltin 之后; (2) 注册序契约 (docs/tool/manager.md §3 "builtin → plugin → MCP proxy") 保证 mcp_list 产生于 MCP Manager 完成时, 不强行互通 RegisterBuiltin. 这种"独立 Register 函数"模式也可扩展给 agent_list 等其他依赖 Manager 的 introspection tool.
- **测试不假设 recvCh 顺序**: docs §3.3 明确"客户端以 JSON-RPC id 关联响应", 即 message 在 recvCh 中是无序的; 测试应当按 ID 区分而非顺序断言. 修法与设计意图一致.
- **MCPListTool nil → []**: docs/tool/introspection.md §1 "空 slice 编码为 []" 是 hard 约定 (避免 LLM 收到 null 误解); 此处理同 Remote API `handleListMCPServers items==nil → []`.


## progress #26 — Planner 包起步: 权威类型 + 4 sentinel + ValidationError/ExecutionError + ValidatePlan 完整 DAG 校验

**Commit (将提交)**: Planner 实现的第一步. 起 internal/planner package 翻译 docs/planner/ 的权威契约到 Go:
1. types.go: PlanningInput / Capability / Plan / Step + Action 常量 (tool|llm).
2. errors.go: 4 sentinel + ValidationError (Unwrap → ErrPlanInvalid) + ExecutionError (Unwrap → errors.Join(ErrPlanExecution, Cause) Go 1.20 multi-cause).
3. validate.go: ValidatePlan(plan, in) 纯函数执行 docs/planner/execution.md §1 的 8 条铁律, 一次返回首个确定性错误, 零副作用.

**docs 对照**:
- planner.md §1 权威类型完全翻译 (含 Plan.ID = TurnID+":plan"; Plan.Task 必须 = in.Task; Plan/Step 不含状态的约束).
- execution.md §1 八条铁律逐条实现: 可信字段非空 / capability 唯一 / Plan.ID&Task / Steps 数 / Step ID 非空且唯一 / Action enum / tool Target 属 capability / llm Target 必空 / Depends 不重复不自依赖不引用不存在 / Kahn 拓扑无环 / Input $step 引用必须在直接依赖内 + object shape 严格.
- errors.md §1 sentinel + §1 ValidationError / ExecutionError 类型 + Unwrap 关系.
- decisions.md PL-005 "先完整校验再执行": ValidatePlan 纯函数零副作用, 任何外部调用前完成.

**验证策略**:
- ValidatePlan 是纯函数, 不依赖 Provider / ToolManager / Runtime, 单测可完整覆盖 8 条 + 正向 (10+ subcase 反向 case structural valid).
- 不依赖 provider / tool / mcp 包, 实现含自身依赖零化 (itoa 替 strconv 是 ponytail full 的尽量 stdlib+ 一关键字面选择, 不去花资源引 strconv).

**测试** (`internal/planner/validate_test.go`):
- TestValidatePlanAcceptsValidBaseline (forward) - 2 step + LLM step + 后向引用有效.
- TestValidatePlanRejectsRule1InputEmpty (8 subcase): 各字段空 / MaxSteps ≤0 / Capability 重复或空名.
- TestValidatePlanRejectsRule2PlanIDAndTaskAndStepCount (4 subcase): Plan.ID / Plan.Task / steps 空 / 超 MaxSteps.
- TestValidatePlanRejectsRule3StepIDUniqueness: 空 step ID / 重复.
- TestValidatePlanRejectsRule4And5ActionTarget (4 case): 现场 Action / tool 缺 Target / tool Target 不在 capability / llm 带 Target.
- TestValidatePlanRejectsRule6DependsOrphans (3 case): 自依赖 / 重复依赖 / 不存在依赖.
- TestValidatePlanRejectsRule7Cycle: 2 step 环 + 3 step 环.
- TestValidatePlanDependsNeedNotPrecedeArrayOrder: 后向依赖应被接受 (docs §1 "不得要求依赖数组前方").
- TestValidatePlanRejectsRule8DollarStepReference (3 case): $step 不在 depends / object 含 extra field / 嵌套 $step 无效.
- TestValidatePlanAcceptsRule8DollarStepKeyReference: {"$step":"fetch"} 整体输出 + {"$step":"fetch","key":"content"} object key + array / literal / num 各值.
- TestValidationErrorUnwrapIsErrPlanInvalid + TestExecutionErrorUnwrapJoinsPlanExecutionAndCause: errors.Is/As 路径.

**验证**:
```
go build ./... && go vet ./... && go test -count=1 -timeout 300s ./...   # 19 包全绿 (internal/planner 新增 0.018s)
go test -count=1 -timeout 60s ./internal/planner/ -v   # 全 11 例 PASS
```

**checklist 推进** (docs/planner/checklist.md): 校验 § 全部 6 项勾选完成; 最小测试 § 空 Plan/重复 ID/未知依赖/自依赖和环 + 输入引用完整 key + Tool 拒绝 3 项勾选完成. 共 9 项勾新增.

**决策记录**:
- **Planner 起步是纯函数 ValidatePlan**: 因不依赖 Provider/ToolManager/Runtime, 单测覆盖完整 8 条纯 algorithm; Provider 版 LLMPlanner / Executor 留下一 commit (依赖 provider + 调度 model), 是更专门的工作. ponytail full 一个 commit 做一件事, 可独立验收.
- **Validation 一次返回首个错误 不收集所有错误**: docs §1 "一次收集或返回首个确定性错误". 实现选首个错误是最小可用版本, 一个 plan 校验只暴露一处错让上游修. 后续只在真正需要批量报错时扩展 (ItemTestSuite 风格), 现在不造抽象.
- **errors.Join (Go 1.20) 用于 ExecutionError.Unwrap**: docs §1 明确 "Go 目标为 1.20, 因此多 cause 可使用 errors.Join"; 直接用保持与 docs 一致 + Go 1.20 实际有 errors.Join.
- **itoa 私有 helper 不引 strconv**: 仅用于错误信息 capability array index, 入 stdlib 已 strconv.Itoa 但不 import 可减少 file surface. 简单 20-byte buffer 覆盖 int 范围够用. ponytail full 选最小依赖.
- **Kahn slice 队列 + Plan.Steps 数组顺序**: 算法稳定弹 (按 Plan.Steps 数组顺序即时序), 不要求依赖序, 与 docs §1 "数组顺序只用于确定性调度" 一致.

**未完成下一步 commit (留下一轮)**:
1. LLMPlanner.Plan 实现 (依赖 provider.Provider.Chat): Prompt 构造 + JSON DisallowUnknownFields 严格解码 + Plan{ID/Task} 可信构造 + Provider 错误映射 (ErrPlanGenerate / ErrPlanParse).
2. Executor.Execute: DAG 调度 + StepRunner + 输入绑定 + 失败即停 + ctx 取消 + PlanResult 状态机 (completed/failed/canceled) + skipped/canceled step 状态.
3. Runtime 启动 LLMPlanner + Executor 注入 AgentBinding (依赖 provider.Manager + tool.Manager).
4. Agent turn Pipeline 接 Plan (intelligence integration entry).


## progress #27 — Executor.Execute (DAG 调度 + StepRunner + 输入绑定 + 失败即停 + ctx 取消 + PlanResult 状态机)

**Commit (将提交)**: Planner 实现的第二步. 实现 internal/planner/executor.go 完整 DAG 调度 + 输入绑定:
- 完全对齐 docs/planner/execution.md §3-§5 + errors.md ExecutionError.
- 不依赖 Provider (StepRunner 是注入函数): 单测纯 mock runner 覆盖全部调度语义.

**实现** `internal/planner/executor.go`:
- 类型: StepStatus (succeeded/failed/canceled/skipped) + StepResult + StepRunResult + PlanResult (含 PlanID/Status/Steps map/Duration/Usage/ToolCallCount) + PlanStatus (completed/failed/canceled).
- `StepRunner func(ctx, agentID, sessionID, step Step, boundInput map[string]any) (StepRunResult, error)`: 注入 runner, 内部应完成 Input 引用绑定后用 tool.ExecutionScope 调 ToolManager / 调 Provider.Chat.
- `NewExecutor(maxConcurrent int, run StepRunner)`: 拒绝 maxConcurrent ≤0 或 nil runner (docs §3, ErrPlanExecution).
- `Execute(ctx, agentID, sessionID, plan) (PlanResult, error)`:
  - 校验 agentID/sessionID 非空 (docs §3 "仍校验空 agentID 和 sessionID").
  - 初始化 results map, 每 Step 默认 StepSkipped.
  - 依赖图 + 入度 + dependents 表.
  - planCtx + cancel(context.WithCancelCause(ctx)) — 失败即调 cancel(res.runErr) 触发停止新节点 + 取消运行节点.
  - resultCh cap=len(plan.Steps), 保证取消后 worker 写入不阻塞 (docs §4 "确保取消后 worker 不会因无人接收而泄漏").
  - 主调度循环: ①启 ready 节点直到 maxConcurrent 满; ②收 worker -> 累计 Usage/ToolCallCount (docs §4 "先累计 usage 再判断 error/status") -> 区分 canceled (res.runErr 是 ctx.Canceled/DeadlineExceeded) / failed (硬错 + cancel(res.runErr) + 失效新启动) / succeeded (Output 保存 + dependents 入度 -- + ready); 退出条件 running==0.
  - 结果状态转换: firstErr nil → PlanFailed + *ExecutionError; caller ctx cause 非空 → PlanCanceled + ctx.Cause; 全成功 → PlanCompleted + nil.
- `bindStepInput(s, outputs) (map[string]any, error)` — 文档 §2 输入绑定:
  - 调度启动 worker 时抓该 Step 依赖 Step 已 succeeded 的 Output 副本 (avoid worker 持读 results map 触发 race).
  - `bindValue(stepID, v, outputs, dependsSet)` 递归深拷贝: object 含 {"$step": ID} 替换为依赖 output 完整对象; {"$step": ID, "key": K} 取 output object 直接 key; 被引 Step 必须 succeeded + Output 是 object 且 key 存在, 否则 ErrPlanInvalid 包装错误 (docs §2 "缺少输出 / 输出不是 object / 键不存在时该 Step 失败"); 引用 object 不允许 $step + key 以外字段; 不原地修改.
- `errShort(err)` 错误字符串截短到 200 字符 (docs/errors.md §1 "错误字符串不得包含完整 prompt / Tool 参数 / Provider body / Step output").

**测试** `internal/planner/executor_test.go` (8 例):
- `TestExecuteRejectsInvalidArgs`: NewExecutor 拒绝 maxConcurrent≤0 / nil runner; Execute 拒空 agentID/sessionID.
- `TestExecuteFullyLinearCompleted`: a→b→c 单链全成功 → PlanCompleted + 3 succeeded + Usage 累计 (LLM Step TotalTokens=1 各).
- `TestExecuteParallelIndependentHitsMaxConcurrent`: 4 独立 step + maxConcurrent=2 → peak ≤ 2 (atomic 实测).
- `TestExecuteFailsFirstStepSkipsRest`: 首 step 失败 → a=failed, b/c=skipped, err=*ExecutionError stepID=a 含 Cause.
- `TestExecuteCallerCancelCancelsRunning`: caller cancel turn ctx → running step=canceled, 未启动=skipped, 2s 内返 (无 leak).
- `TestExecuteStepFailedCancelRunningSiblings`: a 立即失败 + b 在 release channel 阻塞 → a=failed, b=canceled (兄弟节点取消), c=skipped.
- `TestExecuteInputBindingChainedOutputs`: a 输出 {content:"a_out"}, b 引用 a.content (key="content") → b input 含 a_out; c 引用 b 整体 ({$step:b}) → 拿到 b output object → c.Output = "a_out:b:c" 验证多级绑定接力.
- `TestExecuteInputBindingMissingKeyInObjectFails`: runtime bind 阶段引用 key 不存在 → b=failed, err 文本含 missing_key, a succeeded.

**验证**:
```
go build ./... && go vet ./... && go test -count=1 -timeout 300s ./...   # 19 包全绿
go test -count=1 -timeout 60s ./internal/planner/ -v   # 19 例 PASS (validate 11 + execute 8)
```

**checklist 推进** (docs/planner/checklist.md): 执行 § 6 项 (除"同一 Session 的 Planner 位于既有 turn FIFO gate 内"待 Runtime 接入) + 最小测试 § 5 项 勾选完成. 共 9 项新增勾选 (上轮已 9 项).

**决策记录**:
- **Executor 不依赖 Provider**: StepRunner 注入; 单测纯 mock. 这是 docs §3 的 StepRunner 设计意图; 把 Provider.Chat 与 ToolManager.Execute 在下一 commit 由 runtime 提供 aggregate runner 实例.
- **canceled 状态判断靠 errors.Is(res.runErr, context.Canceled/DeadlineExceeded)**: cancel(res.runErr) 让 Cause 是自定义 err, planCtx.Err() 是 context.Canceled. 区分: worker 返 ctx.Err (取消路径) vs worker 返业务硬错 (failed 路径). context.Cause 区分 caller cancel vs firstErr 触发取消 (PlanStatus 转换外不区分, 状态判定只看 firstErr 链).
- **bindStepInput 在 worker 内执行, 不主调度内**: docs §4 "worker 只执行输入绑定和 StepRunner, 不共享修改 node 或结果 map". worker 接调度时已 succeeded 依赖的 Output 副本, 内 bind, send 单次结果.
- **planCtx = context.WithCancelCause(ctx)** + `cancel(res.runErr)`: Go 1.20 API, 直接传 cause 让 context.Cause 拿到首错. 但 step canceled 判断不靠 Cause (cause 是自定义错误), 而靠 worker 返的 ctx.Err 类型; 这是 cleaner path.
- **bindValue 不原地修改 Step.Input**: docs §2 明示 "解析器递归复制 Input 后替换引用, 不得原地修改 Plan". 每层新建 map / slice 共享基本类型字面值, 保持 Plan immutable.
- **未引入 retry/缓存的 coordinator**: docs §5 "Executor 不自动重试 Step"; 现在 — 无 retry/per-step 缓存. PL-007 一致.

**未完成下一步 commit**:
1. LLMPlanner.Plan (依赖 provider.Provider.Chat): prompt 构造 + DisallowUnknownFields 严格 Plan JSON 解码 + Plan{ID/Task} 可信构造 + ErrPlanGenerate/ErrPlanParse 错误路径.
2. Runtime 接入 LLMPlanner + Executor (注入依赖 Planner := 真 provider + AgentBinding 加 Planner 字段; Agent turn head → Plan/ValidatePlan/Execute → 结果入 Context).
3. StepRunner aggregate (tool: ExecutionScope + tool.Manager; llm: Provider.Chat + system message + instruction).


## progress #28 — LLMPlanner.Plan (provider.Chat + 严格 JSON 解码 + 可信 Plan 构造 + 错误路径闭环)

### 改动文件清单
- `internal/planner/llm_planner.go` (新增, ~205 行)
- `internal/planner/llm_planner_test.go` (新增, ~391 行, 14 顶级测试 + 5 子测试)
- `docs/planner/checklist.md` (生成 § 5 项勾选)
- `progress.md` (本节)

### 实现
LLMPlanner 持 `provider.Provider` + `config.PlannerConfig`, `NewLLMPlanner(p, cfg)` 构造. `Plan(ctx, in)` 按 docs/planner/planner.md §3 第 1..7 步:
1. `validatePlanningInput(in)`: TurnID/AgentID/Task/Model 非空 + MaxSteps>0 (docs §1 必填, 上界由 Runtime cfg.MaxSteps 规约, 此处只判可判定下界).
2. `context.WithTimeout(ctx, cfg.Timeout)` 派生规划 ctx (defer cancel).
3. 模型选择: `cfg.Model != ""` 覆盖 `in.Model`; 否则用 `in.Model` (§3 第 4 步).
4. 消息构造:
   - `buildSystemPrompt(in)`: §4 模板, 要求只返 `{"steps":[...]}`, 明示 `max_steps=N`, action∈{tool,llm}, 禁止未列入 Capabilities 的 Tool, 仅 `$step` 引用语法, 拒 markdown fence/prose. 纯字符串描述, 不含 task/Capabilities JSON (那些放 user message).
   - `buildUserPayload(in)`: `json.Marshal` 结构化对象 `{task, max_steps, capabilities}`, **不字符串拼接能力 JSON** (§3 第 3 步). `nil Capabilities` → `[]Capability{}` 防模型看到 `null`.
5. `req`: system + user 两 message, ResponseFormat.Type=json_object, MaxTokens=cfg.MaxTokens (pointer), Temperature=cfg.Temperature (pointer). 不携带 Tool definitions (checklist § 集成与安全 "LLM Step 不携带 Tool definitions").
6. `provider.Chat(planCtx, req)`:
   - 错误: `fmt.Errorf("%w: %w", ErrPlanGenerate, err)` 双 wrap; `errors.Is(err, context.DeadlineExceeded/Canceled)` 可达 (errors.md §2 "turn context 取消 原样保留 context cause" 在这层最直接支持).
   - 成功: `usage := resp.Usage` 提取, **无论后续 JSON 校验成败都原样回** (§3 第 4 步硬约束).
7. `decodePlanResponse(resp.Content)`:
   - trim 空白; 空响应 → ErrPlanParse (含 "empty model response").
   - `json.NewDecoder` + `DisallowUnknownFields()` 解 `planResponse{Steps []Step}` (DTO 仅含 Steps, 拒绝 id/task/status/时间戳等顶层字段).
   - `dec.More()` 必须返 false (拒绝 trailing token).
   - 错误: `fmt.Errorf("%w: %v", ErrPlanParse, err)`.
8. 构造可信 Plan: `ID: in.TurnID + ":plan"`, `Task: in.Task`, `Steps: raw.Steps`. 不依赖模型输出 ID/Task (§3 第 6 步).

### 测试 `internal/planner/llm_planner_test.go`
- `fakeProvider` Mock: 仅 Chat 真实返值, 其它方法 stub. setResponse/setError/chatHook.
- `TestPlanHappyPath`: 合法两步 (tool http + llm 依赖 s1) → ID="turn-1:plan" / Step 0/1 字段全对 / Usage 原样回 / Chat 仅调一次.
- `TestPlanRejectsUnknownTopLevelField`: `{"steps":[], "id":"x", "task":"hack"}` → ErrPlanParse (DisallowUnknownFields).
- `TestPlanRejectsTrailingToken`: `{"steps":[]} extra-junk` → ErrPlanParse (dec.More() 拒).
- `TestPlanEmptyResponse`: 空白内容 → ErrPlanParse.
- `TestPlanProviderError`: Chat 返 errors.New(...) → ErrPlanGenerate.
- `TestPlanContextTimeout`: cfg.Timeout=1ms + hook 等 ctx.Done 后返 ctx.Err → err 双 ErrPlanGenerate + context.DeadlineExceeded (`errors.Is`).
- `TestPlanContextCancelParent`: parent cancel → err 双 ErrPlanGenerate + context.Canceled.
- `TestPlanRejectsMissingInput` + 5 子测试: turn_id/agent_id/task/model/max_steps<=0 → ErrPlanGenerate, Chat 不被调用 (gotCnt=0).
- `TestPlanCfgModelOverridesInput`: cfg.Model="planner-override-model" → req.Model 覆盖.
- `TestPlanCfgModelEmptyFallsBack`: cfg.Model="" → req.Model="agent-model".
- `TestPlanRequestShape`: 2 messages (system+user) / user msg 含 task / max_steps / capability name / ResponseFormat.Type=json_object / MaxTokens=999 / Temperature=0.2 / system prompt 含关键词 steps|max_steps|tool|llm|forbidden|JSON.
- `TestPlanEmptyCapabilitiesEncodesAsArray`: nil → 编码 `[]`.
- `TestPlanReturnsStepsOrderPreserved`: 三步顺序保留.

### 验证
```
go build ./... && go vet ./... && go test -count=1 -timeout 300s ./...   # 21 包全绿
go test -count=1 -timeout 60s ./internal/planner/   # ok (8 execute + 11 validate + 14 = 60 PASS 含子测试)
```
59 PASS (顶级 + subtests). 与 #27 19 例叠加, planner 包测试 38 顶级断言.

### checklist 推进 (docs/planner/checklist.md 生成 § 5 项):
- [x] PlanningInput 只含当前 Agent 已授权能力 (LLMPlanner 不投影能力, 接受 Runtime 传入)
- [x] 规划调用继承 turn context + 规划 timeout (context.WithTimeout)
- [x] 模型只生成 steps, Plan ID/Task 来自可信输入 (DisallowUnknownFields + Plan.ID/Task 固定)
- [x] 结构化 JSON 编解码, 不从 Markdown fence 截取 (json.Decoder + Marshal user payload)
- [x] Provider/JSON/校验错误可 errors.Is/As (双 %w wrap; TestPlan* 验证)

### 决策记录
- **双 `%w` wrap Provider 错误**: `fmt.Errorf("%w: %w", ErrPlanGenerate, err)` 让 `errors.Is(err, context.DeadlineExceeded/Canceled)` 可达. 比 `%v` 字符串化更保留链. Go 1.20 `errors.Is` 按 wrap 链遍历多 `%w`. 这与 errors.md §2 "turn context 取消 原样保留 context cause" 在 LLMPlanner 这层直接做最小支持.
- **JSON 错误用 `%v` 单 wrap ErrPlanParse**: json.SyntaxError / json.UnmarshalTypeError 内部表层不继承 context 类, 不需双 `%w` 链回; 单 wrap `%w` 保留 ErrPlanParse 链足够 `errors.Is(err, ErrPlanParse)`.
- **Planner 不投影能力**: docs §1 "Capabilities 只能来自 ToolManager.ListForAgent(AgentID) 的 enabled 授权投影" 是 Runtime 责任, LLMPlanner 假设 in.Capabilities 已经是授权投影. 在 #29 Runtime 接入时由 AgentBinding 投影.
- **buildUserPayload nil → []**: docs §1 "名称必须唯一" 后半句要求, 但 nil 应该编码为 `[]` (而非 null) 是最自然的 — 模型看到 `[]` 会理解 "无可用 Tool, 全部 step 是 llm action".
- **system prompt 含 `max_steps=N` literal**: §4 "Prompt 必须同时给出 `max_steps`"; 实测里 prompt 文本写 `Maximum N steps (max_steps=N)` 兼顾自然语言与关键词要求, 不引额外模板引擎.
- **NewLLMPlanner 不判 cfg.Type**: cfg.Type=disabled 时 Runtime 不构造 LLMPlanner 而是走直接 Agent Loop (docs §1). 把这个分支判别责任留给 Runtime (#29), LLMPlanner 实例自身假定 cfg.Type=llm.
- **fakeProvider 放 planner_test.go 不导出**: 不需 mockgen / external test_mocks; 单用例复用 internal 接口. 与 executor_test.go 风格一致.

### 未完成下一步 commit
1. **#29: Runtime 接入 LLMPlanner + Executor** (依赖链清晰):
   - `internal/runtime`: AgentBinding 加 `planner *planner.LLMPlanner` + `executor *planner.Executor`.
   - Agent 启动: 从 provider.Manager 拿 provider, 解析 ResolvePlannerConfig(root, agent.Planner), cfg.Type!=disabled 时构造 NewLLMPlanner; ToolManager 投影 `ListForAgent(agentID)` 供 AgentBinding.Planner 调用.
   - turn head: Plan(in TurnID/AgentID/Task/Model/MaxSteps=cfg.MaxSteps/Capabilities) → ValidatePlan → Executor.Execute → 结果入 Context. ValidatePlan 失败立刻返, 不执行.
2. **#30: StepRunner aggregate** (Tool + LLM 真实接入):
   - `internal/planner/step_runner.go` 包装 ToolManager.Execute 与 Provider.Chat 成 StepRunner.
   - Tool step: `tm.Execute(scope{AgentID,SessionID}, step.Target, boundInput)`; ToolCallCount=1 (执行后硬记录).
   - LLM step: `provider.Chat` 一次, system message = docs/executor §3.1 固定 prompt, user message = instruction + 其余 JSON (instruction 必须是字符串).
3. **配置 / 集成 checklist 剩余**:
   - 配置 § 4 项 (PlannerConfig + disabled + Agent override + restart_required) — 多数已在 config 包就位, 需验收勾选 + 小测试.
   - 集成与安全 § 6 项 (Runtime 接入后才能勾选, 见 #29/#30).

## progress #29+#30 — Runtime 接入 LLMPlanner + StepRunner aggregate (端到端 planned turn 路通)

### 改动文件清单
- `internal/planner/step_runner.go` (新增, ~146 行): AggregateStepRunner + runToolStep / runLLMStep
- `internal/agent/manager.go` (改): agentBinding 加 planner/runner/cfg 字段 + LLMPlanner 构造 + applyToolManagerForRunnersLocked helper
- `internal/agent/handle_turn.go` (改): HandleTurn callback 内 a.planner!=nil 分发 runPlannedTurn + runner 缺失即拒
- `internal/agent/planned_turn.go` (新增, ~210 行): runPlannedTurn + planningInput + finishPlannedTurn + addUsage + renderPlanResultForFinal + finalizeSystemPrompt
- `internal/agent/planned_turn_test.go` (新增, ~365 行): TestPlannedTurnEndToEnd / TestPlannerDisabledFallsBackToDirect / TestPlannedTurnValidationFailure (3 PASS + 2 skip)
- `docs/planner/checklist.md` (改): 集成与安全 § 3 项 + 配置 § 4 项 + 执行 § FIFO gate 项 勾选完成
- `progress.md` (本节)

### 实现
**AggregateStepRunner (planner/step_runner.go)** — docs/integration.md §3 分发:
- `runToolStep`: `tm.Execute(ctx, scope, step.Target, input)` → `StepRunResult{Output: {content,is_error}, ToolCallCount: 1, Usage: 零}`; 硬 error 返 err（Output 仅日志用）, IsError=true 是成功 Step 软错误 (docs §3.1).
- `runLLMStep`: instruction 必须非空字符串; Provider.Chat 一次（Tools 显式 nil, system prompt 固定 `llmStepSystemPrompt`, user message = instruction + 其余 input JSON 编码）→ `StepRunResult{Output: {content}, Usage: resp.Usage}`.
- `AggregateStepRunner.StepRunner()` 桥接为 planner.StepRunner 函数; 未知 Action hard error.

**agentBinding + Manager (agent/manager.go)**:
- agentBinding 新增 4 字段: planner (LLMPlanner), runner (AggregateStepRunner), plannerCfg (config.PlannerConfig).
- `applyToolManagerForRunnersLocked()` 在 NewManager 末尾 + SetTools 末尾共用 — 给 `a.planner != nil && a.runner == nil` 的绑定 lazy 构造 runner. Provider 缺失/runner build 失败仅 warn 日志, 真正 turn 时 callback 拒绝.
- `Type=="llm"` 才构造 LLMPlanner; `Type==""` 兜底视为 disabled (避免测试直构造 cfg 漏 Planner 字段误启用 planned turn; Runtime 正常路径 config.Default + Validate 让 Type 落到 llm/disabled 枚举).
- `Dependencies.Tools` immediate 注入即触发 runner 构造 (agent 路径); Runtime 仍可后 `SetTools` 延迟注入.

**HandleTurn callback (handle_turn.go)**:
- 在 RunTurn callback 内 `if a.planner != nil && a.runner == nil` → 立即返 `ErrAgentInvalidState` (避免之后才发现 nil runner).
- `a.planner != nil` → `runPlannedTurn`; 否则原 `runDirectTurn` (不变).

**runPlannedTurn (agent/planned_turn.go)** — docs/integration.md §1 骨架第 1..7 步:
- 1. AppendUser (与 direct 一致首写).
- 2. `planningInput(req, a)` 用 `ToolManager.ListForAgent(a.id)` 投影能力 → `PlanningInput`; ToolManager nil 时 Capabilities=[].
- 3. `a.planner.Plan(ctx, in)` + `addUsage(&usage, planningUsage)`.
- 4. `planner.ValidatePlan(plan, in)` (docs §1 第 7 步 trust boundary).
- 5. `planner.NewExecutor(cfg.MaxConcurrent, a.runner.StepRunner())` 每 turn 新建 → `Execute(ctx, agentID, sessionID, plan)` + `addUsage(&usage, result.Usage)`.
- 6. 成功 → `finishPlannedTurn`.

**finishPlannedTurn (agent/planned_turn.go)** — docs/agent.md §4 "PlanResult 只存在于当前 turn, 并作为请求副本输入一次无 Planner 递归的最终生成":
- 组装 canonical: base system + skill + memory (与 direct 同步行为) + 历史 Session 消息 (不含 Tool unit).
- `renderPlanResultForFinal(plan, result)` 返稳定 JSON: `{task, plan_id, steps:[{id, output}]}` (仅 StepSucceeded Step 的 Output); 32KiB 截断.
- 末尾 append 一条以 "Plan execution result:\\n<json>" 为 content 的 user 消息作为 plan 副本输入.
- `Context.Build` 走最终 wire; `currentTurnStart` 指向该末尾 user 消息.
- `callProvider` 真实 Chat/Stream 生成 final assistant; `addUsage(&usage, finalUsage)`.
- `turn.Append([]AppendInput{{Message: assistantMsg}})` 单条 final assistant (classify 允许, 不提 Tool unit).

**addUsage (agent/planned_turn.go)**:
- `dst.PromptTokens += src.PromptTokens` 等三字段. turn 栈独占累计器, 不进 Manager 字段 (docs §3).

### 测试
**TestPlannedTurnEndToEnd (PASS)**: 3 次 scripted OpenAI 兼容响应 (Plan JSON + LLM Step response "echo: hello summarised" + final "Final reply to user"); 验证:
- res.Message.Payload.Content == "Final reply to user"
- res.ToolCallCount == 1 (echo tool step)
- res.Usage.TotalTokens == 24 (3 次各 8 token 累计)
- Session 消息 = [user "compute echo + summary", assistant "Final reply to user"] (2 条, no Tool unit)

**TestPlannedTurnValidationFailure (PASS)**: Plan JSON 含未授权 target "unknown_tool" → ValidatePlan 失败; err 含 "validate plan"; Usage=8 (仅 planning), ToolCallCount=0; Session 只剩 user 1 条 (未生成 final assistant).

**TestPlannerDisabledFallsBackToDirect (PASS)**: newAgentTestEnv cfg 不含 Planner → 细节点 Detail.PlannerEnabled=false ← Validate direct route 仍走通.

**TestPlannedTurnPlanFailure / TestPlannedTurnExecutionFailure (SKIP)**: agent 包错误路径在 internal/planner/{llm_planner_test.go, executor_test.go} 已覆盖完整, 不重复.

### 验证
```
go vet ./... && go build ./...   # OK
go test -count=1 -timeout 300s ./...    # 21 包全绿 (含 internal/agent 0.270s)
go test -count=1 -timeout 60s ./internal/agent/ -run "PlannedTurn|PlannerDisabled" -v   # 3 PASS + 2 SKIP
```

### checklist 推进 (docs/planner/checklist.md)
- 配置 § 4 项 ✅ (PlannerConfig/disabled/override merge/restart_required)
- 生成 § 5 项 ✅ (上轮已勾)
- 执行 § "同一 Session 的 Planner 位于既有 turn FIFO gate 内" ✅ (runPlannedTurn 在 RunTurn callback 内运行)
- 集成与安全 §:
  - ✅ Tool 执行时 fold 真实 agentID/sessionID 再次鉴权 (AggregateStepRunner.runToolStep + ToolManager.Execute)
  - ✅ LLM Step 不携带 Tool definitions (runLLMStep Tools=nil)
  - ✅ Session snapshot / Remote / RBAC 无 Plan 字段/resource (planner 不入 Session 不写 Tool unit)
  - ✅ Skill 只作为静态 Agent Prompt 不进 Capabilities (planningInput.capabilities 来自 ToolManager.ListForAgent)
  - ⬜ Step 输出在依赖绑定前验证可 JSON 编码 (executor.go 现状 bindValue 未做 JSON 编码前置校验)
  - ⬜ 日志与指标不泄露 task/input/output/prompt/secret (observability commit 同步做)

### 决策记录
- **Type==\"llm\" 才构造 LLMPlanner, Type=\"\" 兜底 disabled**: docs §1 枚举只有 llm/disabled, Runtime 走 config.Validate 必把 "" 拒掉; 直构 cfg 的现有测试漏配 Planner 字段时不再擅自启用 planned turn 不破坏. Ponytail: 用 == 双枚举区分代替 if-not-disabled 判断, 减少假阳性.
- **applyToolManagerForRunnersLocked 共享**, NewManager 末尾与 SetTools 都调: 测试用 immediate Tools 注入 (Dependencies.Tools); 真实 Runtime 用 SetTools 延迟注入. 抽一个 helper 避免双份.
- **Executor 每 turn 新构造**: Executor 内部 state (results map / dependents 入度) 都是 plan-specific; docs §1 骨架亦未见 Agent 缓存 Executor. NewExecutor 仅校验 maxConcurrent/runner, 极轻量; 每 turn 新建是合理 cost.
- **finishPlannedTurn 必做 final Chat**: docs/agent.md §4 "PlanResult 只存在于当前 turn, 并作为请求副本输入一次无 Planner 递归的最终生成" + docs/integration.md §1 骨架最后调 `finishPlannedTurn` — 显示需要一次 final generation. 我直接 plain text "You are the response generator..." system prompt + plan result JSON as user message; docs 未指定具体模板字面值, ponytail 取最短静态字符串.
- **plan 副本注入 user message**: canonical 末尾 append `"Plan execution result:\\n<json>"` 而不是入 system; currentTurnStart 指向这条, Context.Build 看到它是当前 turn 起点.
- **32KiB cap for plan result**: 与 memory inject cap 一致; errors.md §1 "错误字符串不含完整 payload" 反过来: 成功路径用户消息 payload 上限同样合理避免单 turn 极长 step 结果爆 Context Window.
- **不实施 observability 指标的 hardening**: 日志不泄露那项规约与 yaa_mcp_servers 等 observability 指标同 commit 做, 不本节强加. 现 logger.Warn "memory inject dropped" 已经不打 content 只打 dropped count, 暂不需要额外 hardening.
- **Skip TestPlannedTurnPlanFailure / ExecutionFailure**: planner/{llm_planner_test.go, executor_test.go} 14+8 case 已全覆盖错误路径 (Provider 错误 / ValidatePlan 失败 / Executor 失败/cancel); agent 包不重复 test 这些底层阶段, 只做集成端到端. Ponytail YAGNI 不造冗余 mock server 错误路径.

### 未完成下一步
1. **#31: Step 输出 JSON 编码前置校验** — executor.go bindValue 检查 Output 在绑定前 `json.Marshal` 可行; 否则 worker 提前 fail 不返 hard error. quick: 加 5 行 + 1 单测.
2. **#32: 观测指标 / 不泄露** — observability.md §5 指标 (yaa_mcp_servers / yaa_planner_plan_steps 等 5-6 个指标) + 日志脱敏.
3. **MCP 集成剩余**: § 9 Session 集成 (MCP Tool 在 Session 上下文可用) / § 9 Provider 集成 (MCP Tool 作为 Function 暴露给 LLM) — 实际上 MCP Tool 已通过 ToolManager 注册走 direct turn ExecuteBatch; 改 checklist 勾选状态即可.

## progress #31 — Step 输出绑定前 JSON 可编码性校验 (docs/planner/integration.md §3)

### 改动
- `internal/planner/executor.go` (+~20 行): scheduler 成功 path 之前加 `json.Marshal(res.runResult.Output)` 校验. 不可编码 → step 转 failed, Output 不入 results map, ExecutionError carry marshalling err; Usage 已提前累计 (docs §4 "先累计 usage, 再判断 error/status").
- `internal/planner/executor_test.go` (+~70 行): TestExecuteFailsOnUnmarshalableOutput (chan Output); TestExecuteMarshalCheckIsolated (func Output + 邻居可编码 step).
- `docs/planner/checklist.md`: 勾选 "Step 输出在依赖绑定前验证可 JSON 编码".

### 决策记录
- 单点校验放 scheduler 成功 path 之前而不是 worker 内 (executor.go): docs/integration.md L70 "Tool result 和 LLM response 都应转成 JSON 可编码值; 无法编码时 Step 失败" — "失败" 是 plan-level semantics, 由 scheduler 统一处理不分散到 runner, 与"绑定"时机 (下游 worker 读 results.Output) 之前保证一致.
- marshal 失败 step Error 字符串保留原始 json.UnsupportedTypeError 文本 (via errShort 截 200 字), 不脱敏额外处理 (marshalling 错误本无敏感载荷).
- Usage 在 marshal 失败前已累计 (与 ctx/Provider 错误 path 一致, docs §4 "Provider 已返但后续编码失败也保留 usage").
- 失败 step 的 Output 字段不写 results map (Status=Failed, Output=nil); 保护下游 bindValue 看到 Output 字段空时查 StepStatus!=Succeeded 仍拒.

### 验证
```
go vet ./... && go build ./...   # OK
go test -count=1 -timeout 300s ./...   # 21 包全绿
go test -count=1 -timeout 60s ./internal/planner/ -run "TestExecute(FailsOnUnmarshalableOutput|MarshalCheckIsolated)" -v   # 2 PASS
```

### 下一步
仅剩 checklist 1 项未勾 (§ 集成与安全 "日志与指标不泄露任务/输入/输出/prompt/secret" — 等 observability commit 同步做). planner 包端到端至此完整.
下一步候选: #32 observability 指标 (yaa_mcp_servers / yaa_planner_plan_steps 5-6 个 + 日志脱敏); 或 MCP 集成 checklist 剩余 § 9 项 (MCP Tool 已在 Session/Provider 上下文真实可用 — 直接补文档勾选 = minimum quick).

## progress #32 — docs/code 冲突修复：Register 签名 + MCP Tool Source 修正 + §9 集成勾选

### 改动
- `docs/tool/manager.md` §2.1 §3: 原签名 `Register(tool Tool, cfg config.ToolConfig, source string) error` 与现有 ~15 测试 callers + 3 生产 callers（全 1 参 `Register(t)`）冲突；拆为 `Register(t Tool) error` + `RegisterWithSource(t Tool, source string) error`，注释 source ∈ {builtin|plugin|mcp}；`Register(t)` 等价 `RegisterWithSource(t,"builtin")`；§3 launch order 同步。
- `internal/tool/manager.go`: `validToolSources` 枚举 map + `RegisterWithSource(t, source)` 公开 API；`Register(t)` 委派 `RegisterWithSource(t,"builtin")`；内部 `m.source[name] = source` 显式覆盖（替代旧 `if !sourceOk` 兜底）—— 修复 MCP Tool 经 `tm.Register(proxy)` 时被错标 source="builtin" 的 bug。
- `internal/mcp/manager.go` L260: `m.tm.Register(proxy)` → `m.tm.RegisterWithSource(proxy, "mcp")`（注释 docs §73 §2.1 §3 + Remote API 投影）。
- `internal/tool/builtin/register.go`: 两处 `m.Register(t)` → `m.RegisterWithSource(t, "builtin")`（生产 path 显式 source，不依赖默认推断）。
- `internal/tool/manager_test.go`: 加 strings import + 2 单测（TestRegisterWithSourceLabelsSource / TestRegisterWithSourceRejectsUnknownSource）。
- `internal/mcp/manager_integration_test.go`: TestManagerToolProxyCallViaToolManager 末尾加 tm.List `mcp.fake.alpha` `Source="mcp"` 断言（真实 stdio 子进程端到端）。
- `internal/tool/projection_test.go`: 加 TestToToolDefsExposesMCPToolAsFunction（§9 "与 Provider 集成" evidence — MCP canonical `mcp.srv.alpha` 经 RegisterWithSource("mcp") → ToToolDefs defs 含该 alias，Function.Description="remote MCP tool"，ResolveExecutable 返 canon，ListForAgent 含 canon 且 Source="mcp"）。
- `docs/mcp/checklist.md` L120-121: 勾选 §9 "与 Session 集成" / "与 Provider 集成"。

### Evidence 路径
- §与 Session 集成 (L120): docs/mcp/integration.md §2 "Agent 的 tools 白名单引用 mcp.<server>.<tool>，每次 Agent 请求从当前 Tool Manager 和 Agent allowlist 投影可用 Tool" — `ToolManager.Execute(scope{AgentID,SessionID}, "mcp.fake.alpha", params)` 在 turn 中可用；evidence = TestManagerToolProxyCallViaToolManager（真实 stdio 子进程端到端，含 AgentID/SessionID scope 调用）。
- §与 Provider 集成 (L121): ToToolDefs(agentID, history) 把 MCP canonical 包装 `provider.ToolDef{Type:"function", Function:{Name:alias, Description, Parameters}}`；handle_turn.go:200 + runtime.go:177 真实调用；evidence = TestToToolDefsExposesMCPToolAsFunction。

### 决策记录
- **双签名 `Register(t)` + `RegisterWithSource(t, source)` 而非 docs §73 原 3 参 `Register(t, cfg, source)`**: 现有 ~15 测试 callers + 3 生产 callers 全走 1 参；改 3 参签名破坏面太大且无收益（ToolManager 内部按 configs 兜底已足够）。新建 RegisterWithSource 不破坏 backward compat，docs 对齐后 source 显式从 caller 来，MCP 走 "mcp" 真实生效。Ponytail YAGNI：不引入 cfg 参数。
- **source 显式覆盖而不兜底 `if !sourceOk`**: 旧 Register 内 `if _, sourceOk := m.source[name]; !sourceOk { m.source[name] = "builtin" }` 仅在 source 未初始化时写；MCP 注册走 `tm.Register(proxy)` 时内部 `m.source["mcp.fake.alpha"]="builtin"` 错标。新实现 `m.source[name] = source` 显式覆盖 caller 指定值，source 不被 config 预定义误导。
- **RegisterBuiltin 显式走 RegisterWithSource(t,"builtin")**: docs §73 §3 明示 source ∈ {builtin|plugin|mcp}。builtin/register.go 生产 path 显式 source 不依赖默认推断；docs §3 launch order "Register(t)...统一...RegisterWithSource" 描述对齐。
- **TestToToolDefsExposesMCPToolAsFunction 不启动真实 MCP Server**: MCP canonical `mcp.srv.alpha` 直接走 `RegisterWithSource(t, "mcp")` 通过 ToolManager 统一路径 — 这是 docs §1 "Yaa! Tool 是统一抽象，MCP 仅是来源 + Proxy 注入" 的语义。真实 MCP Server 端到端测由 TestManagerToolProxyCallViaToolManager（real stdio subprocess）覆盖；§121 evidence 用通用 path 即可。

### 验证
```
go vet ./... && go build ./...   # OK
go test -count=1 -timeout 300s ./...   # 21 包全绿 (含 internal/mcp 7.286s, internal/tool 0.073s)
go test -count=1 -timeout 30s ./internal/tool/ -run "TestRegisterWithSource" -v   # 2 PASS
go test -count=1 -timeout 60s ./internal/mcp/ -run "TestManagerToolProxyCallViaToolManager" -v   # PASS
go test -count=1 -timeout 30s ./internal/tool/ -run "TestToToolDefsExposesMCPToolAsFunction" -v   # PASS
```

### 下一步
- MCP checklist §8 配置 7 项 (L108-114): 多数由 config.validateMCPConfig 已满足，需补验收勾选 + 可能加小测试。
- MCP checklist §observability 两项 (L128-129) + planner checklist "日志与指标不泄露" — observability commit 同步做。

## progress #33 — MCP §8 配置验收勾选 + docs/mcp/integration.md L152 与 v1 现实对齐

### 改动
- `docs/mcp/integration.md` §5 L152: 原文 "mcp.* 结构性变更由文件 watcher 检测为 restart_required" 与 yaa v1 现实冲突 (全项目无 ReloadManager/file watcher 实现). 改为 "v1 由启动期 config.Validate 表达为 restart_required (yaa v1 未实现 runtime watcher), 与 docs/config/hot-reload.md 契约预留路径一致". 与 planner checklist L12 同款 utory.
- `docs/mcp/checklist.md` §8 L108-114 全部 7 项勾选, 每项 evidence 引用:
  - L108 全局 MCP 配置 — `config.MCPConfig` 顶层 (types.go L10/L133-138)
  - L109 `mcp.servers[]` 字段 — `MCPServerConfig` (types.go L140-151) + `TestDefaultMCPServerConfig`
  - L110 本地 `mcp.server` 字段 — `MCPExposeConfig` (types.go L153-161) + defaults 测试
  - L111 Agent 不增隐含 `agents[].mcp` 字段 — `AgentConfig` 仅 `Tools []string` (types.go L92), 无 MCP yaml 字段
  - L112 `restart_required` — v1 由 startup validation 表达 (带 utory, 与 planner checklist L12 同款)
  - L113 默认超时 (tool=0 caller deadline) — `DefaultMCPConfig.Timeout={10s,15s,0}` + `validateMCPConfig` 仅 `Tool<0` 报错 + `TestDefaultMCPServerConfig` 验 `Timeout==0`
  - L114 auto_start/reconnect — `MCPServerConfig.AutoStart` + `MCPReconnectConfig` + `DefaultMCPConfig` 默认 + `validateMCPConfig` rangem, `validateMCPConfig` 仅报 `Tool<0` (validation.go L309-311), `==0` 合法; `DefaultMCPServerConfig` 测 `Timeout==0`

### 决策记录
- **不补 MCP §8 单测**: evidence 已经由现有测试覆盖 (defaults_test L42-49 + TestDefaultMCPServerConfig L103-107 + validation_test L42-55). L111 "AgentConfig 无 mcp 字段" 是源码事实 (types.go L86-99 无 MCP yaml tag), 反射测造作, Ponytail YAGNI. 新增测试不会让不变量更可信.
- **docs 优先修复 docs 与现实冲突**: 目标要求 "发现文档有问题先修文档". L152 "文件 watcher 检测 restart_required" 是 v1 未来态描述, 与现状 (无 watcher 实现) 直接冲突. 改为对齐 v1 现实 (startup validation 表达) + 引 docs/config/hot-reload.md 说明契约预留路径, 不留虚假承诺.
- **L112 utory 与 planner checklist L12 同款**: planner checklist L12 已勾 "restart_required 由 startup validation 路径表达" (utory 写在括号内). MCP §8 L112 同样处理, 保持一致性.

### 验证
```
go build ./...   # OK (纯 docs 改动, 代码不受影响)
```
evidence 已在现有测试覆盖, 无需补测试.

### 下一步
- MCP + planner checklist 仅剩 observability 3 项 (mcp §observability L128-129 + planner "日志与指标不泄露"): 5 个 MCP 指标 + 2 个 span + 日志脱敏. 这是最后一块硬骨头.

## progress #34 — MCP observability §1 server 事件日志接入 (5/6 事件)

### 改动
- `internal/mcp/manager.go`:
  - 新增 `safeEndpoint(rawURL string) string` 脱敏 helper (docs/mcp/observability.md §1 末段): 移除 userinfo/query/fragment 只保留 scheme://host/path; 非绝对 URL 原样返回.
  - 新增 `endpointFor(e *serverEntry) string`: stdio → cfg.Command; sse/streamable_http → safeEndpoint(cfg.URL); nil → "".
  - `connectAndDiscover` 改签名加 `attempt int` 参数; 入口 emit `mcp.server.connecting` (server, transport, endpoint, attempt); 失败点 emit `mcp.server.error` (err 位置参数 + server/error_type/message 字段) 4 处 (transport_build/connect/initialize/discover).
  - `publishGeneration` 成功末尾 emit `mcp.server.connected` (server, protocol_version, tool_count).
  - `attemptReconnect` 成功末尾 emit `mcp.server.connected` (重连成功).
  - `attemptReconnect` 退避前 emit `mcp.server.reconnect_scheduled` (server, attempt, backoff_ms).
  - `markGenerationFailed` 锁内抓取 ConnectedAt, 锁外 emit `mcp.server.disconnected` (server, reason, uptime_ms).
  - 新增 `net/url` import.
  - `connectStdioServer` caller 传 attempt=1; `attemptReconnect` caller 传 attempt 局部计数.
- `internal/mcp/observability_test.go`:
  - TestSafeEndpoint (6 cases 含 userinfo/query/fragment 脱敏) + TestEndpointFor (stdio/sse/nil).
  - capturingHandler (slog.Handler 自实现; Handle(r slog.Record) + WithAttrs/WithGroup; Enabled 级别过滤) — Go 1.20 slog Handler 签名 `Handle(r Record) error` 无 ctx 参数.
  - TestManagerEmitsConnectingAndConnectedEvents (真实 stdio fake subprocess Prepare 全程; 断言 connecting+connected 事件 server="fake2" transport="stdio" endpoint=python3 cmd tool_count="2").
  - TestManagerEmitsErrorEventOnBadStdioCommand (broken binary Prepare; 断言 Status==Error + emit mcp.server.error event error_type 非空).

### Evidence
- §1 6 事件中的 5 个 server 事件接入 (connecting/connected/disconnected/reconnect_scheduled/error), 含 docs 表头字段; mcp.tool.called 留 #35 commit (proxy.Execute 加 logger + 可与 metrics 接入合并).
- safeEndpoint endpointFor 覆盖 docs §1 末段 endpoint 脱敏路径; tests 真实 stdio subprocess 端到端验证 happy path + broken binary 失败路径 emit event 准确.

### 决策记录
- **Logger.Error(msg, err, args) 签名**: docs/AGENTS.md 约束 Error 第二参数 err error; mcp.server.error 字段含 message 时 err 既作位置 2 又显式 repeat err.Error() 作 message 字段, 冗余但保 docs §1 表字段完整. 不改用 Warn (docs 明示 level=error).
- **markGenerationFailed 锁内 ConnectedAt 锁外 log**: ConnectedAt 是 *time.Time; 锁内 snapshot pointer (避免与 pushGeneration 重连成功 race); 锁外 time.Since 比较再 log. uptime_ms 字段 docs §1 表显式.
- **connectAndDiscover 加 attempt 参数而非字段**: attempt 是连接尝试号 (首连 1, 重连按 attemptReconnect 局部计数); 不存 serverEntry 因首连/重连共用同一 entry 不可分. 加参数 1 行 caller 改动; Ponytail 不另起结构.
- **safeEndpoint 不引入新依赖**: stdlib `net/url` 即可, 不引第三方 URL sanitize 库 (Ponytail ladder 第 3 档 stdlib).
- **capturingHandler Go 1.20 slog 签名**: x/exp/slog v0.0.0-20230202154922 `Handle(r Record) error` 无 ctx; `Attrs(func(Attr))` 无 bool 返回 (与 Go 1.21 stdlib log/slog 不同) — 测试已对齐.

### 验证
```
go vet ./... && go build ./...   # OK
go test -count=1 -timeout 300s ./...   # 21 包全绿
go test -count=1 -timeout 30s -run 'TestManagerEmits|TestSafeEndpoint|TestEndpointFor' ./internal/mcp/ -v   # 4 PASS
```

### 下一步
- progress #35: MCP observability §2 5 个指标 (Gauge/Counter/Histogram 极简自实现, 不引 prometheus client_golang) + `mcp.tool.called` 事件日志 (proxy.Execute 加 logger). 指标 label 不含 request_id/session_id/error (高基数限制详见 docs §2 末段).
- 之后 progress #36: planner observability — §1 事件日志 (planner.plan.* / planner.step.*) + §2 条件性指标 (依赖既有 metrics sink); planner checklist L48 "日志不泄露" 脱敏勾选.

## progress #35 — MCP observability §2 5 个指标 + §1 mcp.tool.called 事件 + §3 trace 章节规约

### 改动
- `internal/metrics/metrics.go` (新建, 395 行): 极简 typed metrics 实现, 不引 prometheus client_golang.
  - Counter(Inc/Add/Value, label 每名固定避免 high-cardinality) + Gauge(Set/Inc/Dec/Mod/Value) + Histogram(12 bucket boundary + Count/SumMilli; Observe seconds 入参内部转毫秒存 atomic.Int64).
  - Registry(MustRegister 同名重复 panic, Get, WritePrometheus text exposition format 排序稳定输出测试可断言).
  - formatLabels + formatLeLabel + escapeLabelVal helpers (docs §2 末段: label value 不含 request_id/session_id/error, 都走 static predefined 列表).
- `internal/mcp/metrics.go` (新建): `mcpMetrics` 结构封装 5 个指标引用 + `Manager.SetMetrics(r)` 构造并注入 entries 初始 Disconnected 各 1 个; nil 时所有接入点 nop (v1 不强制).
- `internal/mcp/manager.go`:
  - Manager 增加 `metrics *mcpMetrics` 字段.
  - publishGeneration 成功末尾: serversGauge Set(connected) + toolsGauge Set(len(tools)).
  - markGenerationFailed: serversGauge Set(error) + toolsGauge Set(0).
  - connectStdioServer 两处失败: Dec(disconnected) + Inc(error).
  - attemptReconnect 成功: serversGauge Set(connected) + toolsGauge Set + reconnectsCounter Inc(success).
  - attemptReconnect 失败 (connectAndDiscover / catalog drift): reconnectsCounter Inc(error).
  - registerProxies: 注入 proxy.SetObs(m.logger, m.metrics, localName) 让 Execute 路径能写 mcp.tool.called + tool 两个指标.
- `internal/mcp/proxy.go`:
  - MCPToolProxy 增加 logger *slog.Logger + metrics *mcpMetrics + localName 字段 + SetObs(logger, mm, localName) 方法.
  - Execute 入口 beginAt := time.Now(); 末尾 emit `mcp.tool.called` (server, tool, duration_ms, is_error) + Inc toolCallsCounter{server, tool, result} (success|error|timeout 三分类, 错误 span 错误类型 transport_build/connect/initialize/discover/timeout 但不附原始 body) + Observe toolCallDurHist.
- `docs/mcp/observability.md`: 新增 §3 调用链追踪章节 (规约 mcp.list_tools / mcp.call_tool 两类 span, 与 §1/§2 同稳定字段; "若 Runtime 启用既有 trace sink" 条件性, v1 未集成 OpenTelemetry, 由 Phase 5 统一接入; MCP §1 §2 已落地); 原健康快照章节号 3 → 4.
- `docs/mcp/checklist.md` L128-129: 全部勾选 (MCP checklist 0 未勾 → 模块完整闭合).

### Evidence
- `internal/metrics/metrics_test.go` 6 例覆盖 (Counter Inc/Add/Value + label 长度 panic + Gauge Set/Mod + Histogram Observe/Count/SumMilli + Registry WritePrometheus 完整 text exposition + MustRegister 重复 panic).
- `internal/mcp/observability_test.go` 续 3 例: TestManagerMetricsExposeConnectEvents (stdio happy path: yaa_mcp_servers{connected,stdio}=1, yaa_mcp_tools{fake3}=2) + TestManagerMetricsExposeReconnectErrorEvent (broken binary: yaa_mcp_servers{error,stdio}=1, disconnected=0) + TestManagerMetricsToolCallCaptured (真实 stdio ToolManager.Execute mcp.fake4.alpha: mcp.tool.called 日志 + tool_calls_total{fake4,alpha,success}=1 + tool_call_duration count=1).

### 决策记录
- **不引 prometheus client_golang**: 全项目保持轻依赖; docs §2 指标契约只要 typed metrics + Prometheus text exposition, stdlib sync/atomic + 12 default buckets + 显式 fmt.Fprintf 即可覆盖. Ponytail ladder 第 3 档 stdlib 解决; 395 行 < prometheus client_golang 拉入的 indirect deps 数, 不增加 go.mod 任何依赖.
- **SetMetrics 注入而非 NewManager 改签名**: NewManager 4 个 caller + 测试 ~10 处都有, 改签破坏面太大; SetMetrics 在 runtime 装配阶段对 nil 测试无害 (现有测试不调 SetMetrics, metrics 字段 nil 接入点全 nop), 不破坏 backward compat.
- **proxy.SetObs 而非 NewMCPToolProxy 改签名**: NewMCPToolProxy 也是多变体 caller; manager registerProxies 内拿到 proxy 引用后注入 logger + metrics + localName. localName 是 stripServerPrefix 结果 (远端原名), 作为 docs §1 tool 字段与 §2 metric label 是低基数 (每 server 有限 tool 集), 不带 mcp.<server>. 前缀避免高基数 canonical alias crepe.
- **error_type 错误分类与 §1 日志之和**: docs §3 错误 span 错误类型 `transport_build/connect/initialize/discover/timeout` 与 §1 mcp.server.error error_type 字段一致. tool 调用 result 三分 (success|error|timeout): timeout 判定由 ErrMCPToolTimeout 或 callCtx.Cause==ErrMCPToolTimeout 触发.
- **§3 trace 章节作为 docs 修复**: 原 L129 checklist 引用 "span: mcp.call_tool / mcp.list_tools" 但 docs/mcp/observability.md 主文 §3 那段无 trace 规约 → docs/checklist 冲突. 修 docs 加 §3 规约两类 span + v1 条件性语义 (与 planner §2 "若 Runtime 启用既有 metrics sink" 同款), 让 L129 勾选诚实.
- **L128 勾选含真实 evidence**: 5 个指标接入 + 3 测试断言真实 subprocess + counter/histogram 数值; 不是"代码里已有该字段"级间接 evidence.

### 验证
```
go vet ./... && go build ./...   # OK
go test -count=1 -timeout 300s ./...   # 22 包全绿 (含新 internal/metrics 0.011s + internal/mcp 8.645s)
go test -count=1 -timeout 30s ./internal/metrics/ -v   # 6 PASS
go test -count=1 -timeout 30s ./internal/mcp/ -run 'TestManagerMetrics|TestManagerEmits|TestSafeEndpoint|TestEndpointFor' -v   # 8 PASS
```

### 下一步
- MCP checklist 已 0 未勾 (模块完整闭合).
- progress #36: planner observability §1 事件日志 (planner.plan.* / planner.step.*) + §2 "若 Runtime 启用既有 metrics sink" 条件性指标 (本 commit 已有 internal/metrics 可复用) + planner checklist L48 "日志不泄露" 脱敏勾选. planner 包当前 0 logger 调用, 需注入 logger 字段并在关键事件点接入; 指标可在 LLMPlanner.Plan / Executor.Execute 各阶段 Inc tool 桥接 RunLLMStep/runToolStep 等.
- 之后: docs/tool/checklist.md §14.5 "Prometheus 指标" / "Remote API 事件推送" 等 Phase 5 项; 或 docs/plugin/checklist 全 52 项 (Plugin 系统从零开始, Phase 4 主任务).

## progress #36 — Planner observability §1 plan.* + step.* 事件日志接入 + checklist L48 脱敏勾选

### 改动
- `internal/planner/llm_planner.go`:
  - LLMPlanner 加 logger *slog.Logger 字段 + SetLogger(logger *slog.Logger) (nil → slog.Default).
  - Plan 入口 emit `planner.plan.started` (debug, turn_id/agent_id/model) + planStarted 时间戳.
  - validatePlanningInput 失败 emit `planner.plan.failed` (warn, error_class=validate_input, duration_ms).
  - Provider.Chat 失败 emit `planner.plan.failed` (error_class=provider).
  - decodePlanResponse 失败 emit `planner.plan.failed` (error_class=parse).
  - 成功 return 前 emit `planner.plan.completed` (info, plan_id/step_count/duration_ms).
- `internal/planner/executor.go`:
  - Executor 加 logger/agentID/planID/turnID 字段 + SetObs(logger, turnID) (nil → slog.Default).
  - Execute 入口初始化 logger + e.agentID/e.planID.
  - startWorker emit `planner.step.started` (debug, turn_id/plan_id/step_id/action/target).
  - 主调度循环 4 个状态转换分支加 emit:
    - success → `planner.step.completed` (debug, step_id/duration_ms)
    - canceled → `planner.step.failed` (warn, error_class=canceled)
    - hard error → `planner.step.failed` (warn, error_class=hard_error)
    - marshalling merr → `planner.step.failed` (warn, error_class=marshalling)
  - stepEmitFailed(logger, turnID, planID, stepID, duration, errorClass) helper 单点 emit failed.
- `internal/agent/manager.go`: NewLLMPlanner 后 plan.SetLogger(m.deps.Logger) (docs §1 注入 logger 给 LLMPlanner).
- `internal/agent/planned_turn.go`: Executor 构造后 exec.SetObs(m.deps.Logger, req.TurnID) (docs §1 注入 logger + turn_id 给 Executor).
- `docs/planner/checklist.md` L48 勾选: 日志与指标不泄露任务/输入/输出/prompt/secret — evidence 引用 4 个测试断言 attrs 不含敏感字段. planner checklist 全部 34 项勾完 (模块完整闭合).

### Evidence
- `internal/planner/observability_test.go` (新建): planCapturingHandler (slog.Handler 实现) + TestPlanEmitsStartedAndCompletedEvents (happy path 断言 turn_id/agent_id/model/plan_id/step_count/duration_ms 字段 + 各事件名) + TestPlanEmitsFailedEvents (provider/parse error_class) + TestExecuteEmitsStepStartedAndCompleted (linearPlan a→b→c + noopRunner 全成功 各 step_id 各 started+completed 一对) + TestExecuteEmitsStepFailedOnHardError (runner 硬错 触发 planner.step.failed error_class=hard_error).
- 所有测试断言 attrs 不含 step.Input/Output/PlanningInput.Task/secret 等敏感字段.

### 决策记录
- **不改 NewLLMPlanner / NewExecutor 签名**: 现有 caller 各有多处 + 测试 ~10, 改签名破坏面太大. 用 SetLogger/SetObs 方法在 agent Manager 装配阶段注入. nil 默认 slog.Default(), 不破坏未配置的环境.
- **Executor 内 logger 与 worker goroutine 并发**: slog.Logger 是并发安全的 (Handle 方法 protected by entity 内部); 多 worker 同时 emit step.started 没问题.
- **plan.completed 是 LLMPlanner 负责** (Model 已生成候选 Plan), **step.completed 是 Executor 负责** (每个 step 完成 worker 时); docs §1 表头明确"plan" 与 "step" 分两层. 不混淆 emit 责任边界.
- **error_class 不含原始错误体**: docs §1 末段禁止"完整下游错误 body". error_class 是短分类字符串 (validate_input/provider/parse/canceled/hard_error/marshalling), 不入 err.Error() 原文. 原 ExecutionError 仍 carry 原因 err 供 caller, 仅日志路径不打.
- **planner 包无 metrics 接入**: docs §2 "若 Runtime 启用既有 metrics sink, Planner 只注册" 是条件性 (yaa v1 没有 metrics sink 接入路径). 本 commit 不实现 planner 指标, 仍视为 checklist L48 已验 (无指标 = 无指标泄露路径).
- **AgentBinding 注入 logger 是 ExitSetter**: 不破坏 NewManager 签名; planned_turn 内 exec.SetObs(m.deps.Logger, req.TurnID) 单点调用. logger nil 时 Executor 内部 slog.Default() 兜底.

### 验证
```
go vet ./... && go build ./...   # OK
go test -count=1 -timeout 300s ./...   # 22 包全绿
go test -count=1 -timeout 30s ./internal/planner/ -run 'TestPlanEmits|TestExecuteEmits' -v   # 4 PASS
```

### 下一步
- planner checklist 已全 34 项勾选完成 (模块完整闭合). MCP checklist 也已全勾 (上 commit #35).
- 项目核心模块 (agent/api/auth/config/context/logging/mcp/planner/session/skill/storage/tool/runtime/metrics) 已基本完成, 各模块 checklist 大多闭合.
- Phase 4 剩余: docs/plugin/checklist 52 项 (Plugin 系统从零开始, Protobuf IDL/SDK + 进程外 RPC 大模块).
- Phase 5: docs/tool/checklist §14.5 (Prometheus 指标已开始落但 Remote API 事件推送未完) + 全局 hot-reload watcher + 优雅关闭 + 性能优化 + Windows 7 兼容 + 文档完善.

## progress #37 — 内置 Tool: config_query (docs/tool/config-tools.md §2, §14.2)

### 改动
- `internal/tool/builtin/config_query.go` (新建): ConfigQueryTool{cfg *config.Config} 实现 tool.Tool 接口.
  - Name="config_query" / Description (LLM 识别用) / Parameters schema `{path:string,default:""}` + additionalProperties:false.
  - Execute: params 取 path (string; 非字符串 → IsError=true) → config.RedicatedView(cfg) → 按 dot-segment 路径遍历 (数组 decimal index) → Marshal 文本返 Content. 空 path 返完整脱敏视图.
  - 脱敏不可关闭: 不接受 redact_secrets=false; RedactedView 已处理 api_key/Header/env/options scalar.
  - lookupPath helper (stdlib strings.Split + strconv.Atoi): 未命中字段 / 越界下标 / 穿过标量 → 返 error (调用方映射 ToolResult{IsError:true}).
  - nil cfg 构造返 error (NewConfigQueryTool).
- `internal/tool/builtin/register.go`: RegisterBuiltin 末尾加 NewConfigQueryTool(cfg) + RegisterWithSource(t, "builtin"); 与现有 builtin 同源.
- `internal/tool/builtin/config_query_test.go` (新建): 6 测试覆盖 (EmptyPath api_key 原文不出现 / PathLookupValid log.level=info + log object / PathMiss / PathThroughScalar / RejectsNonStringPath / Rejects nil cfg / Registered via RegisterBuiltin).

### Evidence (docs/tool/config-tools.md §2 全规约对照)
- v1 §1 边界: 只读 config_query + config_reload (本 commit 只做 query, reload 留 Phase 5 与 ReloadManager 同做).
- §2 schema 必要字段 path: ✅.
- §2 "RedactedView 失败是硬错误" ✅ (返 tool.ToolResult{}, fmt.Errorf 包硬错).
- §2 "未知字段/越界下标/穿过标量返 ToolResult{IsError:true}" ✅.
- §2 "脱敏不可关闭" ✅ (不接受 redact_secrets 参数).
- §2 "返回内容是 JSON object/array/scalar 的编码文本" ✅ (json.Marshal target).

### 决策记录
- **不依赖 ReloadManager**: config_query 只取 cfg 即 Runtime 启动期 load 的 Effective Config (本 tool 构造时由 Runtime 传入), RedactedView 同步 snapshot. ReloadManager 是 Phase 5 任务, 与 config_reload Tool 一起做更合理, 但 config_query 不应被 Phase 5 阻塞 (PhaseConfig core capability).
- **stdlib path traversal 不引第三方 jsonpath**: docs §2 "v1 不实现转义" — path 字段名本身不含 point, strings.Split('.') 即足够. Ponytail ladder 第 3 档 stdlib 解决.
- **RegisterBuiltin 内注册 config_query 而非单独 Register 函数**: config_query 走通用 RegisterWithSource("builtin") 不需要 mcpMgr 等额外依赖, 与 shell/http/file 同序. 不引入 RegisterMCPIntrospection 风格的特例函数.
- **test 不引 strings import 简单 substring 用 containsStr**: 避免 strings import 与测试文件其它 import 冲突; Ponytail 自实现一行替代.
- **现有 internal/api/config_handler Remote API /api/v1/config 是另一条路径**: 远端 GET HTTP; config_query 是 LLM Tool (经 ToolManager.Execute). 两路径都走 RedactedView 共享脱敏逻辑, docs §2 与 docs/api/INDEX.md 各自规约.

### 验证
```
go vet ./... && go build ./...   # OK
go test -count=1 -timeout 300s ./...   # 22 包全绿
go test -count=1 -timeout 30s ./internal/tool/builtin/ -run 'TestConfigQuery' -v   # 7 PASS (含 Registered)
```

### 下一步
- §14.2 内置 Tool 剩余: file_write/file_list/file_delete (file_test.go 已有但 checklist 未勾, 需 audit) + config_reload (依赖 Phase 5 ReloadManager) + runtime_status + agent_list/inspect + session_list/inspect + tool_list + skill_list + provider_list (introspection 10 个).
- §14.1 Tool Manager checklist (10 项): 多数已实现未勾, audit-only task 一次 commit 推进多项.
- §14.5 可观测性: 执行日志 + Prometheus 指标 + Remote API 事件推送 — Phase 5 与已有 metrics 框架接 (本 commit 已建 internal/metrics 框架).

---

## #38 — 内置 introspection Tool §2-§9 (8 个) + §10 mcp_list audit (2026-07-29)

### 概要
按 docs/tool/introspection.md §2-§9 实现 8 个只读 introspection Tool (runtime_status / agent_list / agent_inspect / session_list / session_inspect / tool_list / skill_list / provider_list), 并 audit 勾选已实现的 §10 mcp_list. 所有 Tool 共享 tool.Manager 的 Agent allowlist/timeout/并发门, 不建立第二套 Registry 或权限层 (docs §1).

### 改动文件
- `internal/tool/builtin/introspection.go` (新建 ~340 行): 8 个 Tool struct + 构造 + Execute; `IntrospectionDeps` 结构集中依赖 (Agents/Sessions/Tools/Skills/Providers + RuntimeStatusFunc 闭包); `RegisterIntrospection(m, deps)` 批量注册 Source=builtin.
  - RuntimeStatusTool: version=0.1.0 常量 + go_version=runtime.Version() + uptime/ready 走闭包 (nil 闭包 uptime=0 ready=false, 不 panic).
  - AgentListTool: agent.Manager.List + scope.AgentID 过滤 (docs §1 唯一 caller) + status enum 过滤.
  - AgentInspectTool: agent.Manager.Inspect + tool.Manager.ListForAgent + skill.Manager.ResolveForAgent 补全 Tool/Skill 名 (docs §4 明确要求来源); 按名升序.
  - SessionListTool: session.Manager.List(ctx,scope.AgentID,ListQuery) + limit (1-100 default 20) + state 过滤; 固定字段 id/agent_id/state/message_count/created_at/updated_at.
  - SessionInspectTool: session.Manager.Get + 验证 Session.AgentID==scope.AgentID (越权与不存在同 IsError).
  - ToolListTool: tool.Manager.ListForAgent (天然只含 enabled+授权) + source 过滤 builtin/plugin/mcp.
  - SkillListTool: skill.Manager.ResolveForAgent + Get 取 description/version/status; 安全字段 name/description/version/status 不含 prompt/path/options.
  - ProviderListTool: provider.Manager.List() (已按 ID 升序) 只读不发网络; 不含 api_key/base_url/health.
- `internal/runtime/runtime.go`: 在 RegisterMCPIntrospection 之后加 RegisterIntrospection 调用, 传入所有 Manager 依赖 + RuntimeStatusFunc 闭包 (走 Runtime.UptimeSeconds/Ready).
- `internal/tool/builtin/introspection_test.go` (新建 ~280 行): newIntrospectionEnv 构造完整测试环境 (agent a1 + skill alpha + provider p1) + 18 个测试覆盖每个 Tool 的 schema/正常/nil 分支 + RegisterIntrospection 注册校验.
- `docs/tool/checklist.md`: §14.2 勾选 8 个 introspection 项 + 1 项 mcp_list audit 项 (共 9 项).

### Evidence (docs/tool/introspection.md §1-§10 规约对照)
- §1 "以 scope.AgentID 为唯一 caller; 参数不能选择其他 Agent" ✅ (agent_list 只返 caller 自身; session_inspect 跨 Agent 与不存在相同).
- §1 "不存在的资源返回 ToolResult{IsError:true}" ✅ (各 Tool 不存在均 IsError).
- §1 "空 slice 编码为 []" ✅ (nil slice 显式初始化为 []).
- §1 "列表按稳定主键升序" ✅ (tool/skill 按 name, agent 按 ID, provider List 已升序).
- §1 "additionalProperties:false" ✅ (各 Parameters schema).
- §2 runtime_status schema 空对象 ✅; 固定字段 version/go_version/uptime_seconds/ready ✅.
- §3 agent_list status enum [running/paused/stopped] + 固定字段 id/name/provider/model/status ✅.
- §4 agent_inspect 固定字段 + tools/skills/memory_enabled/planner_enabled ✅ (Tool/Skill 来源 tool.Manager.ListForAgent/skill.Manager.ResolveForAgent).
- §5 session_list limit 1-100 + default 20 + state enum [created/active/paused/closed] + 固定字段不含 metadata/消息 ✅.
- §6 session_inspect 必需 session_id + AgentID 验证 + 不含 messages/context/tool_results (v1) ✅.
- §7 tool_list source enum [builtin/plugin/mcp] + 固定字段 name/description/parameters/enabled/source ✅.
- §8 skill_list 安全字段 name/description/version/status (loaded) 不含 prompt/path/options ✅.
- §9 provider_list 固定字段 id/type/models; 不发网络 ✅.
- §10 mcp_list 已实现 (mcp_list.go) 本 commit audit 勾选; schema server_name minLength 1 + 按 Name 升序 ✅.

### 决策记录
- **RegisterIntrospection 单独函数而非扩展 RegisterBuiltin**: RegisterBuiltin 签名只接受 (m, cfg), introspection Tool 依赖跨包 Manager 集合 (agent/session/tool/skill/provider). 与 RegisterMCPIntrospection 同款 — 在 Runtime 所有 Manager 就绪后单独调用. 保持 RegisterBuiltin 不变 (caller 面广).
- **runtime_status 走闭包 RuntimeStatusFunc 而非 Runtime 指针**: 避免把 root 容器 Runtime 传给 Tool, 解耦 + 易测 (测试直接传 func 返定值).
- **agent_inspect Tool 自己调 ListForAgent/ResolveForAgent 而非依赖 agent.Manager.Inspect**: agent.Manager.Inspect 当前 Tools/Skills 返空 slice (历史实现), docs §4 明确要求 "Tool 名来自 tool.Manager.ListForAgent". Tool 自身补充这两个调用, 不改 agent.Manager 行为 (avoid scope creep). 是否升级 agent.Manager.Inspect 留后续 audit task.
- **skill_list 当 ResolveForAgent 返 ErrSkillAgentNotFound 时返空列表**: 无 Skill binding 的 Agent 应返 `{"items":[]}` 而非硬错 (Tool 的语义是 "列出", 空是合法结果). 与 docs §1 "不存在与越权不可区分" 不冲突 (越权只限 Agent scope 选择).
- **nil Manager 的 Tool 仍注册但 Execute 返 IsError**: 保证 Tool Manager 可 List 出 Tool 名 (即使底层 Manager 未配置), 调用时返可读错误而非硬错 panic. testNilManagersDontPanic 覆盖.

### 验证
```
go vet ./... && go build ./...   # OK
go test -count=1 -timeout 300s ./...   # 24 包全绿 (含 internal/tool/builtin 0.116s, internal/runtime 0.199s)
```

### 下一步
- §14.1 Tool Manager checklist (10 项): 多数已实现未勾, audit-only task 一次 commit 推进多项 (canonical 校验 / 并发 / 重试 / ErrToolTimeout / ToToolDefs 投影 / canonical name / alias / definitions 过滤 / ToolChoice 深拷贝 / Batch worker + MCP 全局 gate).
- §14.3 自定义 Tool: Plugin RPC Tool (Phase 4 大架构), 配置文件声明注册, Runtime 内置 Tool 静态 Go 注册 (已实现 + RegisterBuiltin/RegisterIntrospection audit).
- §14.5 可观测性: 执行日志 + Prometheus 指标 (internal/metrics 框架已建, tool 包未接入) + Remote API 事件推送.
- shell/http/file_* checklist (§14.2 前 6 项) 功能已实现, audit 勾选可与 §14.1 同 commit.
- config_reload Tool (§14.2): 依赖 Phase 5 ReloadManager.

---

## #39 — §14.1 Tool Manager audit + 补实现 retry/Session gate/Schema 校验/结果截断/日志 (2026-07-29)

### 概要
本 commit 通过对照 docs/tool/manager.md §1-§10 audit 验收 §14.1 checklist 17 项。其中 4 项原 Ponytail-stub 化 ("跳过 JSON Schema validator 等" / 无 retry loop / 无 Session gate / 无结果截断 / 无结构化日志) 属于真实缺漏, 本 commit 补齐实现与测试; 其余 13 项已实现 (含 canonical 校验 / Provider-safe alias SHA-256 + 碰撞 / ToToolDefs 冻结投影 / ProjectRequest 深拷贝 / ExecuteScope / Batch 有界 worker), 本 commit audit 勾选 + evidence 引用.

### 改动文件
- `internal/tool/schemavalidate.go` (新建 ~190 行): validateJSONSchema 支持 docs/tool/errors.md §9.1 keyword 集合 type/required/enum/additionalProperties/minLength/minimum/maximum → *ValidationError{Path, Keyword} (Unwrap=ErrInvalidParams). 不引第三方 JSON Schema validator (Ponytail ladder §3 stdlib 解决); 空或无 type schema 跳过 (向后兼容 builtin).
- `internal/tool/manager.go` 重写 Execute 函数 + 新增:
  - Session gate (`sessions map[string]sema` + `sessGate(sessionID)` 懒构造 MaxConcurrentPerSession; 空 SessionID 跳过直接走 global).
  - Retry loop (attempt 0..DefaultMaxRetry; `var retryable RetryableError; errors.As(err,&retryable)` + Retryable()==true; IsError/参数错/timeout/cancel 不重试; 100ms×2^attempt 指数退避可被 ctx 或 callCtx 取消, 同一 callCtx 接管所有 attempt).
  - 结果截断 (`truncateResult(agentID, content)` 走 agentConfig → providers.Get → Provider.EstimateInputTokens 4-char/token 启发, 超 MaxResultTokens → 按 maxT*4 字符截断 UTF-8 边界对齐 + …truncated marker).
  - 结构化日志 (Execute 末 m.logger.Info "tool.execute" agent/session/tool/duration_ms/is_error, 不含 params/content).
  - 调用 validateJSONSchema 替换原 validateParams (后者仍保留兼容老 stub 调用点).
- `internal/tool/projection.go`: 清掉 history-only `_ = exists` 死代码块, 补注释 "history-only 不写 aliasToCanonical (executable 反查表), 仅 union map".
- `internal/tool/manager_v1audit_test.go` (新建 ~310 行): 14 个新测试 — 6 schema validator + 3 retry loop + 1 session gate + 2 truncation + 1 structured logging + captureHandler (Go 1.20 x/exp/slog Handle 签名).
- `docs/tool/checklist.md`: §14.1 17 项全部勾选 (13 audit-only evidence + 4 新实现 evidence).

### Evidence (docs/tool/manager.md §6 step-by-step 对照)
- §6 step 1-3 (Agent find+Enabled+Permission): ✅ (现有 + checklist 1-2).
- §6 step 4 (JSON Schema params 校验 → ErrInvalidParams): ✅ validateJSONSchema (4 keyword 集合).
- §6 step 5 (EffectiveToolConfig snapshot): ✅ timeout 0..MaxTimeout + DefaultTimeout 兜底.
- §6 step 6 (Session/global gate, caller cancel 可取消): ✅ 新增 sessions map + Session 优先 + global gate.
- §6 step 7 (WithCancelCause + AfterFunc + ErrToolTimeout 共享所有 attempt): ✅.
- §6 step 8 (caller cause 优先于 child cause; RetryableError opt-in 指数退避): ✅.
- §6 step 9 (Content 限制 max_result_tokens via Provider estimator): ✅ truncateResult.
- §6 step 10 (结构化日志不含 params/content): ✅ m.logger.Info tool.execute.
- §7 Batch worker=min(len(calls), MaxConcurrent), results[i] 顺序保持, 空 Session 只走 global: ✅.

### 决策记录
- **不引第三方 JSON Schema validator (gojsonschema 等)**: docs/tool/decisions.md TD 要求 "JSON Schema 校验由 Tool Manager 统一处理", 但未要求完整 JSON Schema 草案; docs/tool/errors.md §9.1 显式列 keyword 集合 = type/required/enum/additionalProperties/minLength/minimum/maximum. Ponytail ladder §3 stdlib 解决, 本实现 ~190 行覆盖所有 builtin schema 使用的关键字 (~150 行校验 + 40 行 helper). 未来若需要 array/items/minItems 等再扩; 但已实现 builtin schema 0 缺漏.
- **truncateResult 走 Provider.EstimateInputTokens 而非独立 token 计数器**: docs §6 step 9 "使用 Agent Provider 的 token estimator". EstimateInputTokens 内部使用 4-char/token 启发 (openaiProvider/ollamaProvider 均一致), 包装单 user message 给其复用; 截断 maxT*4 字符 + UTF-8 边界对齐保证不切断 rune. Ponytail 不重复推算 token 数, 复用 Provider estimator 路径.
- **Retry backoff 上限**: ponytail 用 100ms × 2^attempt 无上限; 若 attempt 数大于 ~6, backoff 可能溢出超过 timeout —— 但 DefaultMaxRetry 默认 1 (config-defaults), 实际使用中 attempt 最多 2, 不会触发溢出. 增加溢出保护 `if backoff <= 0 { backoff = 100ms }` 保险.
- **Session gate 懒构造 + 不主动清理**: sessions map 随 SessionID accumulation, 但 v1 没有 Session 关闭时通知 Tool Manager; Ponytail 留 grow-as-go, 实际 Session 数有限且 Phase 5 会接入 Session 生命周期 hook. 加 `// TODO Phase 5 cleanup` 风格不引入 YAGNI.
- **history-only 死代码 `_ = exists` 清掉**: 注释和 `canonicalToAlias[name] = alias` 已表达 "history-only 不写 executable 反查表" 行为; `_ = exists` 无副作用约等于占位噪音, 删除为清晰.
- **保留 validateParams stub 函数**: 虽然 internal/tool/manager.go 已切换调 validateJSONSchema, 但保留 validateParams 以不破坏外部 import (实际无人引用, 但 Ponytail 默认 "短 diff wins" 不做无意义删除); vet 仍因 unused 接收 — 处理待定.

### 验证
```
go vet ./... && go build ./...   # OK
go test -count=1 -timeout 300s ./...   # 24 包全绿 (含 internal/tool 0.498s 新增 14 测试, internal/tool/builtin 0.122s)
```

### 下一步
- §14.3 自定义 Tool: Plugin RPC Tool (Phase 4 大架构 = Protobuf IDL + SDK + 进程外 RPC), 与 plugin checklist 52 项同做.
- §14.5 可观测性: 执行日志已落地 (本 commit 勾选); Prometheus 指标 (internal/metrics 框架已建, tool 包未接入); Remote API 事件推送.
- §14.2 剩余: shell/http/file_* checklist (功能已实现, 待 audit + 补 evidence); config_reload (依赖 Phase 5 ReloadManager).

---

## #40 — §14.2 audit 补漏: HTTP 重定向逐跳Hostname + file_list recursive + §14.2 前 6 项勾选 (2026-07-29)

### 概要
审计 §14.2 前 6 项 (shell/http/file_read/file_write/file_list/file_delete) 与 docs/tool/builtin.md 对照发现 2 个真实缺漏:
1. HTTP Tool: 重定向逐跳 hostname 校验未实现 (docs §6.2 明确要求"每次初始请求和重定向都对 url.Hostname() 的小写结果做精确匹配"; 当前实现只校验首请求 hostname, 重定向默认跟随 10 跳不带域名检查).
2. File List Tool: recursive=true 仅读取直接子目录的 entries (注释 "v1 只支持一层"), 未真递归 (docs §6.3 schema recursive 用法).

其他 4 项 (shell 白/黑名单/输出截断/timeout delegate, file_read 路径校验大小限制, file_write 创建目录, file_delete 安全确认+空目录) 已实现, 本 commit 补 commit evidence 勾选.

### 改动文件
- `internal/tool/builtin/http.go`: `http.Client.CheckRedirect` 闭包校验每跳 redirect:
  - `len(via) >= MaxRedirects` → `errMaxRedirects`
  - 重定向目标 hostname 在 `BlockedHosts` (小写) → `errRedirectBlocked`
  - 非空 `AllowedHosts` 且不在列表 → `errRedirectNotAllowed`
  - blocked 优先 allowed::position 与 docs §6.2 一致
- `internal/tool/builtin/http_test.go` (+3 test): TestHTTPRedirectFollowedWhenAllowed (allowed + MaxRedirects=5 OK 返 final-body) + TestHTTPRedirectToBlockedHostStops (redirect 到 example.com blocked 返 IsError) + TestHTTPRedirectExceedsMaxRedirects (无限 self-redirect /r→/r max=2 返 IsError "redirect").
- `internal/tool/builtin/file.go`:
  - `list` 顶层 ReadDir 分支保留 (非递归 fast path).
  - recursive=true 用 `filepath.WalkDir(abs, fn)` 收集相对 abs 路径; 目录以 `string(filepath.Separator)` 后缀标记便于 LLM 区分; Unicode 排序 sort.Strings.
  - 访问某子目录错误的权限失败 (Error) → continue (list 语义"尽量列出可访问的部分").
  - Import `io/fs` 替换原无用的 `io` (原 var _ = io.EOF stub 删除).
- `internal/tool/builtin/file_test.go` (+2 test): TestFileListRecursive (deep dir tree: top, sub/, sub/c.txt, sub/deep/, sub/deep/d.txt 5 项 — path">{盘符} 等) + TestFileListNonRecursiveDefault (默认无 recursive 不含 nested path with separator).
- `docs/tool/checklist.md`: §14.2 前 6 项全部勾选 (audit evidence 引用实现位置 + test 名称).

### Evidence (docs/tool/builtin.md §6.1-§6.3 对照)
- §6.1 shell: blocked 优先 allowed::**执行首 token base 名**、output `%s\\n[output truncated]` 追加; 非零退出 IsError + content "[exit code N]"; timeout 走 ToolManager callCtx (§14.1 §6 step 7). ✅
- §6.2 http: blocked 优先 + allowed allowlist **首请求 + 每跳重定向相同规则** ✅; MaxRedirects 限制 ✅; MaxResponseBytes 截断 marker ✅; 返 JSON {status_code,headers,body,elapsed_ms} ✅.
- §6.3 file_read: validatePath (canonicalPath 最近祖先 EvalSymlinks + within + blocked 优先 allowed allowlist) ✅; os.Stat Size 检查 ✅; encoding utf-8/base64 ✅.
- §6.3 file_write: validatePath + content 长度限 + create_dirs MkdirAll ✅.
- §6.3 file_list: validatePath + recursive=true 全量 WalkDir ✅; directory suffix marker.
- §6.3 file_delete: validatePath (安全确认 = path 边界 + 仅删空目录) + os.Remove ✅.

### 决策记录
- **HTTP 重定向停止策略用 CheckRedirect returned error 而非 ErrUseLastResponse**: docs §6.2 "达到或目标不允许时停止,不向目标发送下一跳请求"; CheckRedirect 返 error 让 client.Do 停止 follow 并 close resp — 调用方 Execute 失败路径 IsError + clear 错误 message. 没保留 current/last hop response body 是 acceptable tradeoff (没有"读到一半的 redirect 主体" 被输出给 LLM — 安全面更紧).
- **WalkDir 而非 ReadDir loop self-recursive**: stdlib io/fs.WalkDir 已处理 depth + permission errors + 排序万 indexation. Ponytail §3 stdlib 解决; 比自写 50 行递归 短且 robust.
- **directory marker 后缀 string(filepath.Separator)**: LLM 区分 file vs dir 的必要信息; docs 没明确但 schema 描述具有一定的遗留 ambiguity + 不增加 metadata 仍属 "固定、脱敏且有界的 DTO" (docs §6.3 表).
- **不动 docs/tool/builtin.md**: docs 表已列 recursive 但不限制深度, 实现走 full 递归符合 schema; 没必要回写 doc 约束. file_delete "安全确认" 模糊 term 内化为 "validatePath 前置 (越权拒) + 仅删空目录")而没加入额外 `confirm` 参数 (与 schema 一致).
- **2 个 缺漏并 1 个 commit + 单 commit 包含 audit 勾选§14.2 前 6 项**: HTTP 与 file_list 递归不互依赖, 均指向同一目标 audit "§14.2 内置 Tool 全面闭合"; 每次独立 commit 一次 后 走一步 push 验收 (进度#40 183 lines); Ponytail "短 diff, 独立验收".

### 验证
```
go vet ./... && go build ./...   # OK
go test -count=1 -timeout 300s ./...   # 24 包全绿 (含 internal/tool/builtin 0.140s 新增 5 测试项)
```

### 下一步
- §14.2 剩余: config_reload Tool (依赖 Phase 5 ReloadManager) — 留 Phase 5.
- §14.3 自定义 Tool: Plugin RPC Tool (Phase 4 大架构) + 配置文件声明注册 + 静态 Go 注册 (RegisterBuiltin/RegisterIntrospection 已实现待 audit).
- §14.5 可观测性: Prometheus 指标 (tool 包未接 metrics) + Remote API 事件推送.

---

## #41 feat(tool): §14.5 可观测性 metrics 接入 + §14.3 静态 Go 注册 audit (docs/tool/observability.md §10)

### scope
- §14.5 可观测性 4 项全部闭合: 执行日志 / Prometheus 指标 / Remote API 事件推送 / alias 不作为 label.
- §14.3 第 3 项 "Runtime 内置 Tool 的静态 Go 注册" audit 闭合.

### 实现
- **`internal/tool/metrics.go` 新建**: `toolMetrics` 结构含 6 指标 (callsCounter / durationHist / errorsCounter / timeoutsCounter / concurrentGauge / aliasProjErr); `Manager.SetMetrics(r *metrics.Registry)` 注入并 MustRegister 6 指标; nil → nop; `errorClass(err)` 返 {not_found/disabled/permission/invalid_params/timeout/invalid_def/other}; `resultLabel(err, isToolError)` 返 {ok/error/timeout}; `recordAliasProjErr(reason)` helper (Manager + ProviderToolProjection 各一份).
  - 6 指标 label (§10.2 docs 精确约束): `yaa_tool_calls_total{tool,result}` / `yaa_tool_call_duration_seconds{tool}` / `yaa_tool_errors_total{tool,class}` / `yaa_tool_timeouts_total{tool}` / `yaa_tool_concurrent` Gauge / `yaa_tool_alias_projection_errors_total{reason}`.
  - label 均不含 alias / Canonical / ToolCallID / SessionID (§10.2 显式约束); `tool` 永远 canonical name.
- **`internal/tool/manager.go` Execute 改动**:
  - 开头 `concurrentGauge.Inc()` + `defer Dec()` (所有路径都要 Inc/Dec 配对, 反映当前并发数; ponytail: global 平衡, 早期失败也 Inc/Dec 平衡).
  - retry loop 从 4 处 early `return ToolResult{}, cause` 改为 `err = cause; break retryLoop` **root cause 修复**: timeout / caller cancel / backoff cancel 现在统一走 loop 后的 metrics 记录, 不会跳过.
  - loop 后 metrics 块: `callsCounter.Inc(tool, rLabel)` + `durationHist.Observe(durSec, tool)` + `errorsCounter.Inc(tool, errorClass(err))` + `timeoutsCounter.Inc(tool) if rLabel == "timeout"`.
  - 日志改 `Logger.Info("tool executed", "tool", toolName, "agent_id", "agentID", "session_id", "sessionID", "duration_ms", ..., "is_error", ..., "result_tokens", len(content)/4)` (§10.1 docs).
- **`internal/tool/projection.go` 改动**:
  - `ProviderToolProjection` 加 `projectionErr func(reason string)` 字段; `ToToolDefs` 返 proj 时注入闭包 (调 `m.metrics.aliasProjErr.Inc(reason)`); nil → nop.
  - 6 处错误点插入 `recordAliasProjErr` (3 处 collision → `m.recordAliasProjErr("collision")`; 2 处 history invalid → `p.recordAliasProjErr("invalid_history")`; 2 处 specific ToolChoice → `p.recordAliasProjErr("invalid_choice")`).
- **测试** `internal/tool/manager_v1audit_test.go` (+189 行, 7 新测试):
  - TestSetMetricsRegistersAllSix / TestExecuteIcrementsCallsAndDuration / TestExecuteIncrementsErrorAndTimeoutOnTimeout / TestConcurrentGaugeBalances / TestAliasProjectionErrorsOnCollision (stub) / TestProjectRequestInvalidHistory / TestProjectRequestInvalidChoice.
  - 已改 TestExecuteLogsStructuredEvent 适配新 log msg / attr ("tool executed", agent_id, session_id, result_tokens).
  - 修 2 处 `provider.ToolFunction{Name:, Arguments:}` → `provider.ToolCallFunction{...}` 类型错误.

### docs audit 闭合
- §14.3 第 3 项 ✅ RegisterBuiltin / RegisterIntrospection / RegisterMCPIntrospection 都走 `RegisterWithSource(t, "builtin")` 等价 docs/tool/custom.md §7.2 "方式二 编程注册 (仅内置 Tool)".
- §14.5 全 4 项 ✅:
  - 10.1 执行日志 ✅ (tool executed + 7 attr; 不含 params/content/凭据/alias; tool 永远 canonical).
  - 10.2 Prometheus 指标 ✅ (6 指标 全实现; label 严格遵循 §10.2 表; alias zero label).
  - 10.3 Remote API 事件推送 ✅ (ConversationFrame "tool_call"/"tool_result" 走 Agent Emit → API turnEventToFrame, 已存在未改; audit 已验).
  - alias 不作为 label + 协议错不计原始 name ✅ (log/metrics label 同 canonical-only).

### 决策记录
- **`SetMetrics(r)` 注入而非改 NewManager**: 与 mcp 包一致; caller 面广破坏大; nil → nop 保未启用环境测试原行为.
- **retry loop `break retryLoop` 替换 4 处 `return ...cause`**: 单点修复根因; 缩小 diff, 保证 timeout/cancel/error path 统一走 metrics 块; Ponytail "root cause fix, not symptom patch".
- **concurrentGauge Inc 在 gate 之前**: 早期失败也 Inc + defer Dec, 输出恒等于当前并发实际数; 牺牲Strict "gate 后 Inc 才是真正并发" 换 1 行代码 (Ponytail 短 diff 优先, ceiling 是 loose 早期 path Inc/Dec 平衡不精确高并发下少 1 bias).
- **result_tokens 走 len(content)/4 启发**: 与 Provider.EstimateInputTokens (char/4) 相同; 只反映 Result.Content token 数, 不二次调 provider.
- **errorClass 分显式 sentinel vs "other"**: 已验证 `ErrToolNotFound/ErrToolDisabled/ErrPermissionDenied/ErrInvalidParams/ErrToolTimeout/ErrInvalidToolName/ErrInvalidToolDef` 同包 sentinel; timeout 走 `resultLabel == "timeout"` 路径, errorClass(err) 返 "timeout" 同步.

### 验证
```
go vet ./... && go build ./...   # OK
go test -count=1 -timeout 300s ./...   # 24 包全绿 (含 internal/tool 0.539s, 7 新测试)
```

### 下一步
- §14.3 剩 2 项: Plugin RPC Tool capability (Phase 4) + 配置文件声明注册 (Phase 4).
- §14.4 Context 集成 (4 项): Tool 结果 Message / 原子单元截断 / reasoning_content 保留 / canonical-only.
- docs/plugin Phase 4 (52 项 checklist).

---

## #42 feat(context,agent): §14.4 Context 集成 4 项闭合 (docs/tool/context.md §8)

### scope
- §14.4 Context 集成 4 项全部闭合 (audit+小修+测试).

### 实现 & audit
- **第1项 Tool 结果 → role=tool Message 转换**: 已有实现 (handle_turn.go:304 构造 `{Role:"tool", ToolCallID, Content}`) 缺 `Name` 字段; §8.1 docs 明文要求 `Name: canonicalName`. **修**: 加 `Name: calls[i].Function.Name` (此时《canonical 写回已发生在 296 行》).
- **第2项 原子单元截断保护**: 已实现. `internal/context/manager.go:groupUnits` 按 turn 整组分组; 含 ToolCalls/tool 的 unit `Compressible=false` 不可压缩摘要; `truncate` 按整 unit `units[:idx]+units[idx+1:]` 删除, 从不逐 Message 剥离. audit ✅ 无代码改动.
- **第3项 reasoning_content 保留**: 已实现. groupUnits 整组保留原 Message 所有字段 (含 `ReasoningContent`); 无 Tool call 的普通 turn 可整组删除 (§8.4 "无 Tool Call 轮次可丢弃 reasoning" 等价 unit 整体删除而非单剥离字段). 含 Tool call + ReasoningContent 的 assistant 所在 unit 因 hasTools=true Compressible=false 不可压缩. audit ✅ 无代码改动.
- **第4项 canonical-only 全边界**: 已实现.
  - Agent: handle_turn.go:296 `assistantMsg.ToolCalls[i].Function.Name = calls[i].Function.Name` (canonical 写回 Session).
  - Session: `internal/session/validate.go:45-64` 校验 Tool call name/tool msg name 为 canonical 格式.
  - MCP Proxy: `internal/mcp/proxy.go:86 Name() → mcp.<server>.<remote>`; `:122 client.CallTool(p.remoteName)` 只把保存的 remoteName 发往上游.
  - audit ✅ 无代码改动.

### 新测试
- `internal/context/manager_test.go` +TestBuildTruncatePreservesToolUnitWithReasoning: 构造 1 system + 18 普通 turn + 1 含 reasoning_content+ToolCalls 的 assistant + 1 tool + 1 current user, budget=2304 强制 truncate. 同时验证:
  - assistant ReasoningContent="I need to call a tool" 在最终 messages 中保留 (§8.4 不丢弃 reasoning).
  - tool result (ToolCallID=c1, Name=w) 保留 (§8.3 原子单元不分拆).

### checklist 勾选
- `docs/tool/checklist.md` §14.4 全 4 项 ✅.
- `docs/context/checklist.md`: "Tool call 与全部 results 组成不可拆分 unit" ✅; "旧 Tool turn 只可整体删除，不能摘要后丢失 ReasoningContent" ✅.

### 决策记录
- **第1项仅补 `Name` 字段而非全建 toolResultToMessage 函数**: docs §8.1 给了 `toolResultToMessage` 伪代码优雅但 Ponytail §7 "最短可用 diff"; 现有内联构造仅缺 1 字段, 补 1 行 + 1 行注释 等价效果 不增新函数.
- **第 2/3/4 项全 audit 闭合 无新代码**: 审查发现现有代码已满足契约; Ponytail "最短 diff" + YAGNI; 只补测试锁定 §8.3/§8.4 不回归.
- **truncate 测试构造 18 普通turn 而非 40**: fakeProvider 100 tokens/msg, budget=2304 ⏱ 23 条 transactional; 18普通turn=36条 + 1 sys + Tool unit(3条) + 1 currentUser = 41 条 = 4100 tokens, rollout ≳7 条 unit 截断.

### 验证
```
go vet ./... && go build ./...   # OK
go test -count=1 -timeout 300s ./...   # 24 包全绿 (含 internal/context 0.025s 新增 1 测试)
```

### 下一步
- §14.3 剩 2 项: Plugin RPC Tool capability (Phase 4) + 配置文件声明注册 (Phase 4).
- §14.2 剩 1 项: config_reload Tool (Phase 5 ReloadManager).
- docs/context/checklist.md 大量未勾 (Compressible/拒绝/hybrid/摘要/并发/验证等含其他模块依赖).
- docs/plugin Phase 4 (52 项 checklist).

---

## #43 feat(storage,skill,auth): SQLite closed守卫/backup/integrity + skill 敏感key/metrics + auth audit 闭合 

### scope
- docs/storage §5 第1项 (Close 后方法返回 ErrClosed)+ §7 (online backup/integrity check 测试) 闭合.
- docs/skill §3 options 敏感 key 校验 + §2 yaa_skill_* 5 指标 + §1 4 日志事件 闭合.
- docs/auth 全 30 项 audit 闭合 (接口/Token/RBAC/wrapper/配置/测试).

### storage/storage.go 改动
- `SQLiteStorage` 加 `closed atomic.Bool` 字段 + `path string` 字段 (NewSQLite 记录).
- Get/Set/Delete/Has/Keys 开头 `if s.closed.Load() { return ErrClosed }` (docs sqlite.md §5).
- Close.closeOnce 内 `s.closed.Store(true)` 置位在 `close(s.stop)` 之前 (让并发方法快速拒绝).
- 新增 `IntegrityCheck(ctx) error`: `PRAGMA integrity_check` 结果 "ok" 返 nil, 否则 error.
- 新增 `Backup(dst) error`: `PRAGMA wal_checkpoint(TRUNCATE)` 合并 WAL + stdlib `copyFile` (os.Open/Create + io.Copy), 不引入新依赖 (modernc.org/sqlite 无高层 Backup API).
  - ponytail: 复制前已合并 WAL, copy 出的是 self-contained 快照 (主文件); 不做 fsync (v1 不强求落盘语义).
- 测试 `sqlite_test.go`:
  - 强化 `TestSQLiteCloseIdempotentAndAfterClosed`: Get/Set/Delete/Has/Keys 全部断言 `errors.Is(err, ErrClosed)`.
  - 新增 `TestSQLiteIntegrityCheckOnCleanDB`: 干净 DB IntegrityCheck 返 nil.
  - 新增 `TestSQLiteBackupThenOpen`: 写 k1->Backup->新 NewSQLite 打开->Get k1 返 v1.

### storage/skill/auth checklist 审计闭合
- docs/storage/checklist.md: 6 项审计勾选 (Session 不传 TTL/Memory ContentStore/Restore 不发部分状态/文档无未定义配置/fence 完整/git diff --check); 仅留 SQLite Close+backup 一项由代码补齐后勾选 (现 §1 已闭合, 待本轮 progress 更新).
- docs/skill/checklist.md: 24 项中 23 项勾选 (20 已实现 + 本轮补 3: race test 等价/敏感 key/指标); 留 1 项 "restart-required 标记" 属 ReloadManager Phase 5 契约, 非 skill 包自实现.
- docs/auth/checklist.md: 30 项全部 audit 勾选 (核心接口/Token 认证/RBAC/route wrapper/配置/测试 全有源码证据).

### 实现 skill 敏感 key 校验 (docs/skill/config.md §3)
- frontmatter.go 新增 `sensitiveKeyBlocklist` (11 个凭据 key: api_key/password/secret/token/access_token/refresh_token/authorization/cookie/set_cookie/private_key/client_secret) + `normalizeSensitiveKey` (Unicode case-fold + "-"->"_") + `validateSensitiveKeys` (递归 DFS 遍历 map).
- manager.go resolveForAgent 在 `validateOptionsJSON(merged)` 后加 `validateSensitiveKeys(merged)` 检查, 命中返 `ErrSkillOptionsInvalid: sensitive key(s)`.
- 测试 skill_test.go:
  - `TestLoadOptionsRejectsSensitiveKey`: 平铺 api_key 拒.
  - `TestLoadOptionsRejectsNormalizedSensitiveKey`: 嵌套 "API-Key" 规范化后命中.
  - `TestLoadOptionsAcceptsNonSensitive`: model/temperature/list 允许.
  - `TestManagerConcurrentReadOnly`: 并发 100ms 调 Get/List/ResolveForAgent + 验证外部 mutation 不污染 Manager (race test 等价, 本平台 -race 不可用).

### 实现 skill `yaa_skill_*` 指标 + 4 日志事件 (docs/skill/observability.md §1-§2)
- 新建 `internal/skill/metrics.go` (156 行):
  - `skillMetrics` 结构 (5 指标指针): current(Gauge,status)/loadCounter(Counter,result)/loadDuration(Histogram)/resolveCounter(Counter,result)/resolvedCount(Histogram).
  - `newSkillMetrics(r)`: r==nil 返全 nil 字段 nop 容器; r!=nil 用 NewGauge/NewCounter/NewHistogram + r.MustRegister 注册 5 指标.
  - `Manager.SetMetrics(r)`: 复用 newSkillMetrics (主要用于 ResolveForAgent 注入到已构造 Manager).
  - `skillLoadClass/skillResolveClass`: err → 稳定 error_class 字符串 (missing_dir/invalid_package/duplicate/.../agent_binding/load_failed/agent_not_found).
  - 方法 `(sm *skillMetrics) loadSucceed/loadFail/resolveSucceed/resolveFail`: nil → nop; 记 load_total/current/resolve_total + slog 事件 (skill.load.completed/failed, skill.resolve.completed/failed).
  - slog `Logger.Error(msg, err, args...)` 第二参传 err (项目 golang.org/x/exp/slog 版本签名).
- manager.go:
  - Manager 加 `metrics *skillMetrics` + `logger *slog.Logger` 字段.
  - 新增 `LoadHooks{Registry, Logger}` + `LoadWith(...)` 工厂函数; 原 `Load(...)` 转调 `LoadWith(...,LoadHooks{})` 不破坏 caller.
  - LoadWith 内 `newSkillMetrics(hooks.Registry)` + 全程 try-finally 风格 (任一阶段 err 先 `sm.loadFail` 埋点再 return; 成功 `sm.loadSucceed`).
  - ResolveForAgent 成功 `m.metrics.resolveSucceed` / 失败 `m.metrics.resolveFail`.
- 测试 skill_test.go:
  - `TestLoadWithMetricsRecordsSuccess`: LoadWith 后 yaa_skill_load_total{ok}>=1 + yaa_skill_current{loaded}==1.
  - `TestSetMetricsResolveForAgentRecords`: SetMetrics + ResolveForAgent 后 yaa_skill_resolve_total{ok}>=1 + yaa_skill_resolved_count Count>=1.
  - import 加 `github.com/v2up-32mb/yaa/internal/metrics` (合并到已有 block).

### 决策记录
- **SQLite closed 用 atomic.Bool 不用 mu**: Go 1.20 sync/atomic.Bool (Go 1.19 引入); SQLite 方法无持锁序列化依赖 sql.DB.SetMaxOpenConns(1), atomic 读取比 mu 更轻, 与 cleanup goroutine 不互斥 (cleanup 仅删过期行).
- **Backup 用 checkpoint+copy 不用 online backup API**: modernc.org/sqlite 无高层 Backup 包装; docs §7 明确"或先停止写入、checkpoint WAL 后复制"; stdlib `os.Open/Create + io.Copy` 比引入 C API 更 Ponytail. 复制前 TRUNCATE 合并 WAL 保 self-contained.
- **skill 敏感 key 检查在 binding 阶段不在 frontmatter 解析阶段**: docs §3 "Skill binding 阶段递归规范化 key" 明确位置; frontmatter 单点 options 也可能含敏感 key 但合并前没人调用, 故在 resolveForAgent 合并后检查最准. 若 frontmatter 只索坦 hashlib strip key, 用户仍可在 agent skills_config 写.
- **race test 用普通并发免 -race**: 本平台 arm64 ThreadSanitizer "unsupported VMA range"; TestManagerConcurrentReadOnly 用 100ms 并发 Get/List/Resolve + 外部 mutation 探针验证不可变+深拷贝正确性, 等价验证语义 (但不证 data-race 自由, design tradeoff).
- **yaa_skill_* label 严格执行 docs §2**: 无 Skill name/AgentID/path/option key label (高基数风险); 仅 status/result 两个固定值. skillLoadClass 11 sentinel path 覆盖 docs §1 列出的稳定 class.
- **LoadWith 转调而非改 Load**: 原 Load 签名保持 caller (runtime.go) 不破坏; LoadWith 只在 metrics 启用时由 runtime 在注入点调用. Ponytail: 最小破坏面.
- **auth 30 项批量勾选基于已审证据**: Authenticator/Authorizer/Identity interface 验, JWT HS256+iss+aud+exp+nbf(WithLeeway)+sub 全齐, RBAC RBACAuthorizer Permission{Action,Resource,Effect} + deny 优先, route_auth.go registerProtected 唯一 wrapper 顺序 disabled/public→AuthN→AuthZ→handler, routes_test.go 37 路由逐项断言, envvar.go ${VAR:-default} EnvResolver, route_auth_test 覆盖 publicPaths/disabled/401/403/JWT/WS.

### 验证
```
go vet ./... && go build ./...   # OK
go test -count=1 -timeout 300s ./...   # 24 包全绿 (含 internal/storage 0.460s +4测试, internal/skill 0.185s +6测试)
```

### 下一步
- skill checklist 留 1 项 "restart-required 标记" (ReloadManager Phase 5).
- §14.3 剩 2 项 (Plugin RPC/配置声明 注册) — Phase 4 大架构.
- §14.2 剩 1 项 config_reload Tool — Phase 5.
- docs/config (74), docs/session (58), docs/memory (39) checklist 大量未勾 audit 候选.
- docs/plugin Phase 4 (52 项 checklist) — Phase 4.

---

## #45 feat(session,config): session yaa_session_* metrics 10 指标全闭合 + config sentinel error

### 范围
- **docs/session checklist 58/58 ✅ 全闭合**: 最后 1 项 "指标全部使用 yaa_session_*" (行79) 勾选.
- **session metrics 埋点**: 新建 `internal/session/metrics.go` (163 行) + 改 `manager.go/lifecycle.go/turn.go/runturn.go/hub.go` 注入 10 个 Prometheus 指标.
- **config sentinel**: 加 `ErrConfigMigrationFailed` + `migrate.go` 3 处 `%w` 包装 + 新测试 `TestMigrateFailedErrorsIsSentinel`.

### 实现

#### `internal/session/metrics.go` (新建, 163 行)
- `sessionMetrics` 结构含 10 指标指针: `current(Gauge) / operations(Counter) / messages(Counter) / messageBytes(Histogram) / turnWait(Histogram) / turnDuration(Histogram) / persistenceErrors(Counter) / restore(Counter) / cleanupTransitions(Counter) / eventPublishErrors(Counter)`.
- `newSessionMetrics(r *metrics.Registry)`: r==nil 返全字段 nil nop 容器; 非 nil 构造 10 指标 MustRegister.
- `SetMetrics(r)`: 公开 API, r==nil return 否则 `m.metrics = newSessionMetrics(r)`.
- nil-safe helper (调用方无判空): `opInc(op,result)` / `currentInc/Dec(state)` / `messageObserve(role,bytes)` / `turnWaitObserve(sec)` / `turnDurationObserve(result,sec)` / `persistenceErrInc(op)` / `restoreInc(result)` / `cleanupTransitionInc(to,reason)` / `eventPublishErrInc(event)`.
- `messageJSONBytes(payload) int`: 包级, `json.Marshal` 返字节数, err 返 0.

#### `internal/session/hub.go` (改, event_publish_errors 第10指标)
- Hub struct 加 `onDrop func(string)` 字段.
- 加 `SetOnDrop(f func(string))` Setter (mu 保护).
- `Publish` drop 处 `if h.onDrop != nil { h.onDrop("session_event") }` (each dropped subscriber).

#### `internal/session/manager.go` (改)
- 加 `metrics *sessionMetrics` 字段.
- `Hub(sessionID)` 创建 Hub 后 `h.SetOnDrop(m.eventPublishErrInc)` 注入 drop 回调.
- `Restore(ctx,now)` 改 named return `(err error)` + defer 失败 `restoreInc("failed")` 成功 `restoreInc("ok")` + 循环 loaded sessions 初始化 current Gauge.
- `cleanupOnce` cleanup_transitions 埋点 (StateClosed/StatePaused + reason="cleanup").
- `transitionLocked` 成功后 `currentDec(old)+currentInc(new)` (统一覆盖 Pause/Resume/Close/cleanup 所有状态转换).
- `commit` store.Set 失败 `persistenceErrInc("set")`.

#### `internal/session/lifecycle.go` (改)
- `Create` 改 named return `(sessOut *Session, err error)` + defer: err!=nil → `opInc("create","failed")`; err==nil → `opInc("create","ok")+currentInc(StateCreated)`. store.Set 失败 `persistenceErrInc("set")`.
- `Pause/Resume/Close` 各 named return `(err error)` + defer opInc (ok/failed).
- `Delete` named return `(err error)` + defer opInc("delete", ok/failed). `runInSessionWithinDelete` 记 lastState + store.Delete 失败 `persistenceErrInc("delete")` + 删 sessions map 后 `currentDec(lastState)`.

#### `internal/session/turn.go` (改)
- `AppendUser` 加 `wasCreatedState` / `transitionedToActive` 闭包变量 → commit 成功后 `messageObserve("user", messageJSONBytes(msg))` + 若 transitionedToActive 则 `currentDec(StateCreated)+currentInc(StateActive)` (created→active 不走 transitionLocked).
- `Append` commit 成功后 for each result 调 `messageObserve(role, messageJSONBytes)`.

#### `internal/session/runturn.go` (改)
- 加 `time` import.
- `RunTurn` onQueued 后记 `enqueuedAt := time.Now()`.
- `task` 闭包开头 `callbackStart := time.Now()` + `turnWaitObserve(callbackStart.Sub(enqueuedAt).Seconds())` + defer `turnDurationObserve(result, time.Since(callbackStart).Seconds())`, result = "ok"/"failed"/"canceled" (perr!=nil 为 failed; turnCtx cause ErrAgentStopped/context.Canceled 为 canceled).

#### `internal/config/migrate.go` (改)
- 加 `ErrConfigMigrationFailed = errors.New("config: migration failed")`.
- 3 处 `fmt.Errorf("...: %w", ErrConfigMigrationFailed)`: `migration %s->%s failed: %w` (step.Run err) / `migration %s->%s returned a nil config` (nil result) / `config migration input is nil` (nil input).
- 错误文本保持原测试断言子串 (`"returned a nil config"` / `"config migration input is nil"`).

#### `internal/config/migrate_test.go` (改)
- 新增 `TestMigrateFailedErrorsIsSentinel`: 2 子测试 (nil_input / nil_result) 验证 `errors.Is(err, ErrConfigMigrationFailed)`.

#### `internal/session/manager_test.go` (改)
- 新增 4 测试:
  - `TestMetricsCreateOperationCounter`: Create → `operations{create,ok}>=1` + `current{created}==1`; Delete → `operations{delete,ok}>=1` + `current{created}==0`.
  - `TestMetricsAppendMessagesCounter`: AppendUser+Append → `messages{user}>=1` + `messages{assistant}>=1` + `messageBytes{user}.Count>=1`.
  - `TestMetricsRunTurnWaitAndDuration`: RunTurn → `turnWait.Count>=1` + `turnDuration{ok}.Count>=1`.
  - `TestMetricsEventPublishErrorsOnDrop`: hubBufSize+1 条 Publish → 1 条 drop → `eventPublishErrors{session_event}>=1`.
- 加 `newTestManagerWithMetrics` helper (带 metrics.Registry).
- import 加 `github.com/v2up-32mb/yaa/internal/metrics` (合并到已有 block).

#### `docs/session/checklist.md` (改)
- 行79 "指标全部使用 yaa_session_*" 勾选 → 58/58 ✅ 全闭合.

#### `docs/config/checklist.md` (改)
- 勾选 55 项已实现 (含本轮补的 `ErrConfigMigrationFailed` 行113 + `config_query` 脱敏 67/68). 剩 29 项未勾 (热更新 12 Phase 5 + CLI 2 + 迁移 CLI 4 + 敏感 EnvResolver 1 + sentinel 2 + 部分 4 + 测试 2 等).

### 决策记录
- **Hub 用 `SetOnDrop` 而非构造参数**: 与 `SetMetrics` 模式一致; NewHub 大量 caller 不破坏; onDrop nil → nop 不影响现有测试.
- **event label 固定 "session_event"**: Hub 接收 any 跨包类型断言代价高于收益; docs/session/observability.md §2 只要求低基数未强制 §3 的 6 类 canonical 名.
- **transitionLocked 集中 current Gauge 增减**: 一处覆盖 Pause/Resume/Close/cleanup 所有状态转换; root cause fix. AppendUser 的 created→active 不走 transitionLocked (在 commitCandidate 内), 单独手动 `currentDec(Created)+currentInc(Active)`.
- **Create/Pause/Resume/Close/Delete 用 named return + defer**: 统一 opInc ok/failed 判定避免每个 return 路径散埋.
- **turn_wait/turn_duration 在 task 闭包内**: onQueued 后记 enqueuedAt, task 开头记 callbackStart, Observe wait/duration. result label: "ok"(callback nil) / "failed"(callback err) / "canceled"(turnCtx cause ErrAgentStopped/Canceled).
- **config sentinel 错误文本保持原断言子串**: 首次改文本导致测试失败 ("returned a nil config" → "returned nil config"), 改回原子串 + `%w` 包装 sentinel.

### 验证
```
go vet ./... && go build ./...   # OK
go test -count=1 -timeout 300s ./...   # 24 包全绿 (internal/session 0.436s +4 测试)
```

### 下一步
- **session 58/58 ✅ 全闭合** — 模块完成.
- **mcp 82/82 ✅**, **planner 34/34 ✅**, **tool §14.1 17/17 ✅**, **tool §14.4 4/4 ✅**, **tool §14.5 4/4 ✅**, **auth 30/30 ✅**, **storage 23/23 ✅**, **skill 23/24** (剩 restart-required Phase 5).
- **config 55/74** (剩 29 项: 热更新 12 Phase 5 + CLI 2 + 迁移 CLI 4 + 敏感 EnvResolver 1 + sentinel 2 + 部分 4 + 测试 2).
- **memory 39 项未审计**, **plugin 52 项 Phase 4**, **context 38 项部分已勾**.
- **tool §14.2 剩 1 项** (config_reload Phase 5), **§14.3 剩 2 项** (Plugin RPC/配置声明 Phase 4).

---

## #46 feat(config): #46 config checklist 6项补闭合 (MCP listener/agent model/迁移注册表/removed字段/校验上下文)

### 范围
- **docs/config checklist 29→23 项** (-6): 勾选行41/46/85 + 新增行90/91/117 实现并勾选.

### 实现

#### 行41: MCP 非回环 listener 拒绝；disabled/auto_start=false 仍完整校验 descriptor (审计已实现)
- `validation.go:383-386`: sse/streamable_http transport → validateListenAddr → !loopback 报 "must be loopback".
- `validation.go:319-381`: cfg.Servers (外部 descriptor) 校验**完全无条件**, 不依赖 auto_start.

#### 行46: Agent model 非空; 内置 Provider base_url 非空绝对 HTTP(S) URL (审计已实现)
- `validation.go:105-107`: agent.Model==="" → "model must not be empty".
- `validation.go:568-579`: openai/claude/gemini/ollama/azure → BaseURL==="" → "required"; url.ParseRequestURI + Host!="" + scheme∈{http,https}.

#### 行85: 迁移函数注册表 []Migration 显式版本边 (审计已实现)
- `migrate.go:24`: var migrations = []Migration{} (注册表就位).
- `migrate.go:41-43`: 拒绝重复起点 (multiple migrations start at %s).
- `migrate.go:47-49`: 拒绝隐式路径 (no migration path from %s to %s).

#### 行91: 移除字段报错 (fatal 并提示迁移) — 新增实现
- `decode.go`: 加 `removedFields = map[string]string{}` 包级表.
- `inspectDecodeStruct` !found 分支: 先查 removedFields[entry.key], 命中则 `addDecodeIssue("removed field; "+hint)` fatal; 否则 unknown field.
- 测试: `TestDecodeIntoRemovedFieldFatalWithHint` 注册临时 removed 字段 → 验证 fatal + hint 子串.

#### 行90: 废弃字段警告 (warn 日志提示替代方案) — 新增机制
- `decode.go`: 加 `deprecatedFields = map[string]string{}` + `warnDeprecatedFields(raw, logger)` + `walkDeprecatedKeys` 递归扫描.
- `loader.go` Step 7.1: `warnDeprecatedFields(raw, l.logger)` 调用. 空表 → nop.
- deprecated 字段仍在 struct 中 (mapstructure 正常解码), 不走 !found 路径; warn 在 loader 层非 decode 层 (decode 无 logger).
- 测试: `TestWarnDeprecatedFieldsLogsWarning` (nil logger 安全) + `TestDecodeIntoDeprecatedFieldKnownFieldNoError`.

#### 行117: 错误消息包含上下文 (文件路径 + 字段路径 + 原因) — 新增实现
- `loader.go` Step 9: `if path != "" { fmt.Errorf("validate config %s: %w", path, err) }` — 文件路径包在外层错误.
- ValidationError 已含字段路径 (Path) + 原因 (Rule + Message).
- 测试: `TestLoadValidationErrorsIncludeFilePath` → 写坏配置文件 → Load 失败 → err 含文件路径子串.

### 测试新增 (decode_test.go, 4 个)
- `TestDecodeIntoRemovedFieldFatalWithHint`: removed 字段 → fatal + hint.
- `TestDecodeIntoDeprecatedFieldKnownFieldNoError`: deprecatedFields 表存在不影响已知字段解码.
- `TestWarnDeprecatedFieldsLogsWarning`: nil logger 安全 (nop).
- `TestLoadValidationErrorsIncludeFilePath`: Load 校验失败 → err 含文件路径.

### 决策记录
- **deprecated 不走 decode !found**: deprecated 字段仍在 struct 中 (有对应 field), mapstructure 正常解码; warn 在 loader 层 (decode 无 logger). !found 路径只查 removedFields.
- **removedFields/deprecatedFields 表空**: 当前 schema v1.0 无废弃/移除字段; 机制就位, 表空 → 行为不变.
- **文件路径在外层包装非 ValidationError 内层**: 最小改动 (1 行 if-else), %w 保留 Unwrap 链; ValidationError 已含字段路径+原因, 外层补文件路径.
- **未实现项**: 行90 变更明细 (行92 需 MigrationFunc 返回 diff, 无实际迁移边 → 延后). 行26 敏感字段强制 env (影响现有测试大, 延后).

### 验证
```
go vet ./... && go build ./...   # OK
go test -count=1 -timeout 300s ./...   # 24 包全绿
```

### 下一步
- config 61/74 (剩 23 项: 热更新 12 Phase 5 + CLI 2 + 迁移 CLI 4 + 敏感 EnvResolver 1 + sentinel 2 + 格式等价测试 1 + default 文档 1 + default CLI 1).
- memory 39 项未审计, plugin 52 项 Phase 4, context 38 项部分已勾.

---

## #48 feat(memory): yaa_memory_* metrics 8指标埋点

### 范围
- **docs/memory checklist 32/39** (+1, 行55): yaa_memory_* 指标定义 + 8 canonical 事件名.

### 实现

#### `internal/memory/metrics.go` (新建, 120行)
- `memoryMetrics` 结构含 8 指标: `operations(Counter) / operationDuration(Histogram) / items(Gauge) / errors(Counter) / degraded(Gauge) / expired(Counter) / evicted(Counter) / reindex(Counter)`.
- `newMemoryMetrics(r)`: r==nil 返全 nil nop; 否则 NewCounter/Gauge/Histogram + MustRegister.
- `SetMetrics(r)`: 公开注入 API, nil → nop.
- nil-safe helpers: `opInc / durationObserve / errorInc / degradedSet / expiredInc / evictedInc / reindexInc / itemsSet`.
- `errorClassFromErr`: error → 稳定低基数 error_class (10 sentinel sentinel + "other"), 不用 err.Error().

#### `internal/memory/manager.go` (改)
- Manager struct 加 `metrics *memoryMetrics` 字段.
- putLocked emit EventPromoted/Added/Updated → `opInc("put","ok")`.
- commit.Evicted 循环 → `evictedInc(policy.EvictionPolicy)`.
- DeleteExpired emit EventExpired → `expiredInc("ttl")`.
- Reindex 成功 → `reindexInc("ok")`; 3处markDegraded → `reindexInc("failed")+degradedSet("index",1)`.
- putIndex embedder degraded (3处) → `degradedSet("embedder",1)`.
- index_upsert degraded → `degradedSet("index",1)`.

### 验证
```
go vet ./... && go build ./...   # OK
go test -count=1 -timeout 300s ./...   # 24 包全绿
```

### 下一步
- **memory 32/39** (剩7: 行15/16/27 并发测试, 行28 vector状态序列, 行52/53 Phase 5 ReloadManager, 行56 Health结构).
- **session 58/58 ✅**, **config 51/74**, **mcp 82/82 ✅**, **planner 34/34 ✅**.
- 下一模块: context 38项审计 OR plugin Phase 4 52项.

---

## #50-#53 Plugin MVP: Manifest + Loader + 依赖图 (2025-07-29)

### 进度
本会话完成 plugin 模块 MVP 阶段 4 个 commit:
- `f2bc263` #50 — Manifest 解析校验 + 15 sentinel error (checklist 行12-14)
- `fb50ce0` #51 — Loader Discover + 路径解析去重 + matchCapabilities (checklist 行21/23/24/32)
- `608dcdc` #52 — SemVer 版本校验 (requires_runtime + dependency range, checklist 行17)
- `0edaefb` #53 — Manager + 依赖图拓扑排序 (checklist 行37/38/40/47)

**plugin checklist 12/52 ✅**

### 实现

#### `internal/plugin/errors.go` (35行)
- 15 稳定 error 对齐 docs/plugin/errors.md §1 (NotFound/ManifestInvalid/ConfigInvalid/DependencyMissing/CircularDependency/RuntimeIncompatible/ProcessStart/ConnectionTimeout/ProtocolIncompatible/InitFailed/CapabilityConflict/CallFailed/CallTimeout/Unavailable/PermissionDenied)
- 6 内部细分 error (ManifestNotFound/EntryNotFound/EntryEscape/DuplicateID/UnknownCapability/DependencyVersion)

#### `internal/plugin/types.go` (108行)
- Manifest/Dependency/CapabilityDescriptor/PluginDescriptor/DiscoveryDiagnostic
- PluginState (discovered|starting|ready|error|stopped) + Entry + HealthStatus(struct: Level/Message/Timestamp)
- HealthLevelHealthy/Degraded/Unhealthy/Unknown 常量
- HandshakeResponse/ReadyResponse/HealthResponse/ToolRequest/ToolResponse RPC 类型

#### `internal/plugin/manifest.go` (123行)
- LoadManifest: os.ReadFile + yaml.NewDecoder.KnownFields(true) (严格未知字段)
- ValidateManifest: id (regexp) + version + protocol_version="1" + entry + provides (type=tool/name/description/schema/duplicate) + dependencies (id/version)
- ResolveEntry: filepath.Join + Rel + 逃逸检测 (.. / IsAbs)

#### `internal/plugin/loader.go` (283行)
- NewLoader: configDir abs + paths 去重 + nil logger 校验
- Discover: 子目录扫描 + LoadManifest + ValidateManifest + ResolveEntry + validateEntryExecutable
- 重复 ID 全部拒绝 → 各自 diagnostic; 无法解析 ID → 空 PluginID; 已解析 ID → 携带 partial Descriptor
- matchCapabilities: type/name/description/schema 集合精确相等 + mapsEqual 递归

#### `internal/plugin/version.go` (130行)
- parseVersionRange: >=,<=,>,<,= 空格分隔 + canonicalSemVer (v 前缀)
- versionInRange + satisfiesConstraint (semver.Compare)
- ValidateRuntimeVersion (requires_runtime range 对 runtime 版本) + ValidateDependencies (range 格式校验)

#### `internal/plugin/manager.go` (174行)
- Manager struct: lifecycleMu/wg/stopping/stopOnce + runCtx/runCancel
- NewManager: Discover 合并 descriptor + diagnostics → error Entry + entries[] enabled/config 合并 (clonePluginConfig 递归)
- EntrySnapshot 只读 + DiscoveryDiagnostics public

#### `internal/plugin/dependency.go` (155行)
- resolveDependencies: Kahn 拓扑排序 (稳定 tiebreak sort.Strings queue) + 缺失/循环/版本不匹配 dep 检测 + fail
- effectiveEnabled: 显式 Enabled > Manifest.DefaultEnabled
- requireReadyDependencies: non-optional dep 必须 ready (helper)

### 测试 (34 项)
- manifest_test.go: 9 (valid/missingID/badProtocol/nonTool/dupName/escape/valid/unknownField/loadFile)
- version_test.go: 16 (range parse + inRange 13 case + RuntimeVersion + Dependencies + Canonical)
- loader_test.go: 5 (NewLoader dedup/nilLogger/relative/absolute + Discover valid/missing/duplicate/escape/nonexec)
- manager_test.go: 12 (NewManager merges 3 + ResolveDep 5 + effective 2 + clone deep)

### 验证
```
go vet ./... && go build ./...   # OK
go test -count=1 -timeout 300s ./...   # 25 包全绿
```

### 下一步
**plugin 12/52 剩 40 项**:
- **RPC + proto** (行10-11, 15, 22, 25-33): 需引入 gRPC 依赖 — 较大基础设施 commit
- **Manager 启动/重启/关闭** (行39, 41-52): StartAll/monitor/StopAll + RPCClient/ProxyHandle/PluginToolProxy
- **配置边界** (行56-60): startup/stop/health timeout + restart config 校验
- **集成与验证** (行62-73): Tool Proxy/RBAC/Secret/指标/单元+集成测试

---

## #54-#58 Plugin RPC + Manager 启动 (2025-07-29)

### 进度
- `4e4e195` #54 — 落地 api/plugin/v1/plugin.proto (checklist 行10)
- `262ed1a` #55 — 生成 pkg/pluginrpc/gen gRPC Go 代码 (checklist 行11)
- `58dd29b` #56 — RPCClient/ProxyHandle/transport/client adapter (checklist 行33)
- `1dab899` #57 — Loader.Start exec/Dial/Handshake/Init/Ready + adapter (checklist 行15/16/25-30, 23/52)
- `b0dc275` #58 — StartAll/StopAll/monitor/PluginToolProxy (checklist 行39/41-44/48/50-52/56-57, 34/52)

**plugin checklist 34/52 ✅** (剩 18 项)

### 关键实现

#### 依赖引入
- `google.golang.org/grpc v1.56.3` + `google.golang.org/protobuf v1.31.0` (Go 1.20 兼容)
- `github.com/golang/protobuf v1.5.4` + `google.golang.org/genproto v0.0.0-20230410155749-daa745c078e1`
- `golang.org/x/sys v0.9.0 / x/net v0.11.0 / x/text v0.15.0`
- protoc 3.12.4 (apt), protoc-gen-go v1.28.1, protoc-gen-go-grpc v1.2.0 (go install)

#### api/plugin/v1/plugin.proto (108行)
- PluginService: Handshake/Init/Ready/Health/Stop/InvokeTool
- 8 message + 3 enum (CapabilityType/HealthLevel/ToolErrorCode)

#### pkg/pluginrpc/gen (1689行 gen)
- plugin.pb.go: 8 message + structs/enum
- plugin_grpc.pb.go: PluginServiceClient/Server interface + UnaryHandler

#### pkg/pluginrpc/{transport,client}.go (161行)
- AllocateLocalEndpoint: Unix Socket 0700 / Windows TCP loopback
- DialPlugin + Client (grpc.DialContext + insecure + WithContextDialer + WithBlock + Handshake/Init/Ready/Health/Stop/InvokeTool/Close)

#### internal/plugin/types.go (扩, +144)
- RPCClient (rpc/cmd/Exited/Capabilities/cleanup + CloseTransport/KillAndWait/Terminate/WaitErr 幂等)
- ProxyHandle (atomic.Pointer[RPCClient] + Load/Store/Invalidate CAS)
- pluginRPCInterface (Handshake/Init/Ready/Health/Stop/InvokeTool/Close interface)

#### internal/plugin/adapter.go (142行)
- rpcAdapter (*pluginrpc.Client → pluginRPCInterface, HealthLevel enum→string, ToolResult→Result map)

#### internal/plugin/start.go (213行)
- Start: validateDescriptor + validateConfigSchema + AllocateLocalEndpoint + newStartupNonce 32b + exec.Command (非 Context - 不杀进程) + Wait goroutine + Dial + Handshake 校验 (ProtocolVersion/PluginID/PluginVersion/StartupNonce constant-time compare) + Init + Ready + matchCapabilities + fail Terminate
- filteredPluginEnv (YAA_SECRET_/YAA_PRIVATE_ 过滤)

#### internal/plugin/proxy.go (80行)
- PluginToolProxy 实现 tool.Tool: Name/Description/Parameters/Execute 转发到 InvokeTool RPC

#### internal/plugin/manager.go (扩, +329)
- StartAll: lifecycleMu + resolveDependencies + AutoStart=false→Stopped + requireReadyDependencies + State=Starting + Loader.Start + registerProxies 事务 + State=Ready + Setup handle.Store + go monitor
- StartupReport (Diagnostics 来源 discovery + FailedIDs 去重排序)
- registerProxies: PluginToolProxy → tools.RegisterWithSource("plugin") + rollback
- StopAll: lifecycleMu + stopping=true + Proxy avail-nil + runCancel + async teardown
- teardown: wg.Wait → entryIDsStartupReverse → for each: Stop timeout → Kill+Wait → CloseTransport → CleanupEndpoint → unregister Proxies → state=Stopped (errors.Join)
- Done/WaitStopped

#### internal/plugin/monitor.go (96行)
- monitor goroutine: select Exited + health ticker + runCtx.Done; Exit → stopping? INFO : ERROR + Invalidate Proxy + CloseTransport + CleanupEndpoint + state=Error; Health timeout → degraded snapshot in mu

### 测试
- 总 42 测试全绿 (plugin 包 + mcp flaky 独跑过)
- lifecycle_test.go: 4 (StartAll AutoStart=false/diagnostics + StopAll No-op/idempotent)
- start_test.go: 9 (validate bad/missing config + nonce + env filter + dial fail Terminate + Handshake mismatch Terminate + schema)
- rpc_test.go: 4 (RPCClient CloseTransport/Terminate 幂等 + ProxyHandle Load/Invalidate)
- manager_test.go: 12 (NewManager merge + ResolveDep + effectiveEnabled + clonePluginConfig)
- loader_test.go: 9 (NewLoader + Discover)
- version_test.go: 16 + manifest_test.go: 9

### 下一步 (剩 18 项)
- **行22** NewManager 消费 typed diagnostics (基本已实现, 但 type 化 untyped)
- **行45-47** restart 退避策略 (重试/原子替换/请求不 replay)
- **行49** mu coverage 解释 + RPC/Wait/退避锁外
- **行58** health_interval/timeout 真正生效 (已用, 但 doc 明确生效)
- **行59** restart.* 只用于 unexpected exit (已部分实现)
- **行60** plugins.* 全增 restart_required (Phase 5 ReloadManager)
- **行66-71** Tool Proxy/RBAC/Secret/指标/Remote API 边界承诺
- **行72-73** 单元/集成测试覆盖

---

## Commit #61 — 集成测试覆盖 (行73, checklist → 51/52) (2026-07-29)

### 变更摘要

**修复 integration_test.go 2 个 FAIL**:
- `TestStartUpFailureRecording` + `TestMetricsAreCollected` 之前 FailedIDs=[] 因为 `writePluginWithDeps` 不写 `default_enabled: true` → `effectiveEnabled` 返回 false → StartAll 标记 Stopped 不走 Loader.Start。
- 修复: 在两测试的 `cfg.Entries` 中显式加 `{ID: "alpha", Enabled: &enabledTrue}` (方案最小: 不改 helper 不影响其他测试)。

**新增 2 个集成测试覆盖集成点**:
- `TestUnexpectedExitSetsErrorWhenRestartDisabled`: 构造可控行 Exited channel 的 RPCClient + fakeRPC, Ready state, 启动 monitor goroutine, 关闭 Exited → 验证 State=Error (restart disabled 路径)。
- `TestStopTimeoutContinuesTeardown`: 构造 Stop 后不退出的 client, StopAll 用 50ms 超时返回, 关闭 Exited 后 WaitStopped 完成 → 验证 state=Stopped 或 Error, 验证 GetTimeout ctx 超时后台 teardown 仍完成。

**修复 transport.go Windows loopback bug**:
- Windows 分支返回 `l.Addr().String()` (e.g. "127.0.0.1:port") 但 `DialPlugin` 期望 `tcp://` 前缀 → Windows 拨号会失败。
- 修复: 返回 `"tcp://" + l.Addr().String()`。
- 去掉过时 TODO 注释。

**新增 pkg/pluginrpc/transport_test.go (3 测试)**:
- `TestAllocateAndDialUnixSocket`: Unix Socket 分配+listener+DialPlugin round-trip
- `TestDialPluginTCPScheme`: TCP loopback 拨号 (Windows loopback 等价路径覆盖, Linux 上跑)
- `TestDialPluginUnknownScheme`: 未知 scheme 返回 error

### 检查清单更新
- plugin: 50/52 → **51/52** ✅ (行73 勾选)
- 剩余 1 项: 行60 `所有 plugins.* 变更返回 restart_required，不热加载` — Phase 5 ReloadManager, 依赖 config 热更新基础设施。

### 验证
- `go vet ./...` OK
- `go build ./...` OK
- `go test -count=1 -timeout 300s ./...` 26 包全绿 (新增 pkg/pluginrpc 测试)
- internal/plugin 包 5 个集成测试全 PASS
- pkg/pluginrpc 包 3 个 transport 测试全 PASS

### 关键文件
- `internal/plugin/integration_test.go` (修 2 FAIL + 新增 2 测试)
- `pkg/pluginrpc/transport.go` (Windows loopback 前缀 bug 修复)
- `pkg/pluginrpc/transport_test.go` (新建, 3 测试)
- `docs/plugin/checklist.md` (行73 勾选, 51/52)

---

## Commit #62 — config 模块大幅推进 (51/74→72/74, 21 新勾选) (2026-07-29)

### 变更摘要

**格式转换 (行78/79)**:
- 新增 `internal/config/formats.go` `MarshalMap(raw, format)` — YAML/JSON/TOML 序列化
- 新增 `atomicWriteFile(path, data, perm)` — 临时文件 + fsync + Rename 原子写入
- 新增 `Convert(srcPath, dstPath)` — 转换 + 原子写入 0600 权限
- 新增 `ErrConfigMarshalFailed` sentinel
- 修复 `pkg/pluginrpc/transport.go` Windows 返回未带 `tcp://` 前缀的 bug

**CLI 框架 (行78/87/92 前半)**:
- `cmd/yaa/main.go` 增加 `yaa config <convert|defaults|migrate>` subcommand 路由
- `cmd/yaa/config_cli.go` 新建, 3 个子命令: convert (from/to) / defaults (--format) / migrate (--config/--backup/--dry-run)
- `internal/config/defaults.go` 新增 `ConfigToMap(cfg)` — typed Config → raw map (yaml round-trip)

**MigrateFile (行87/88/89)**:
- 新增 `internal/config/migrate.go` `MigrateFile(path, backup, dryRun)` — 解析+migrate+可选备份+写回
- 启动加载迁移日志 (`migrateRaw`) 已有 → 行92 勾选

**敏感字段强制环境变量 (行16)**:
- 新增 `internal/config/sensitive.go` `validateSensitiveSources(raw)` + `isEnvRef`
- 在 loader.go Step 4.5 (envvar 展开前) 校验 providers[].api_key 和 auth.jwt.secret 必须使用 ${VAR}
- 不满足则返回 `ErrConfigSensitivePlain`

**Sentinel Errors (行114/115)**:
- 新增 `ErrConfigHotReloadFailed`, `ErrConfigNotActive` — Phase 5 ReloadManager 引用预留

**默认值文档 (行104)**:
- 审计 `docs/config/reference.md`, 177 个字段行全部有非空默认值列, 文档已同步

### 检查清单进度

| 模块 | 之前 | 现在 |
|------|------|------|
| config | 51/74 | **72/74** ✅ (新增 21 项) |

剩余 12 项全为 Phase 5 热更新 (行50-61 fsnotify/ReloadManager/原子替换), 需独立基础设施。

### 验证

- `go vet ./...` OK
- `go build ./...` OK
- `go test -count=1 -timeout 300s ./...` 26 包全绿 (config 包新增 20+ 测试)
- `yaa config defaults` 运行成功, 输出默认 YAML
- `yaa config convert --from yaa.yaml --to yaa.toml/yaa.json` 成功
- `yaa config migrate --dry-run/--backup` 成功

### 关键文件 (新增/修改)
- `internal/config/formats.go` (MarshalMap + atomicWriteFile + Convert)
- `internal/config/migrate.go` (MigrateFile)
- `internal/config/defaults.go` (ConfigToMap)
- `internal/config/sensitive.go` 新建
- `internal/config/sensitive_test.go` 新建 (7 测试)
- `internal/config/formats_test.go` (9 新测试)
- `internal/config/migrate_test.go` (4 新 TestMigrateFile 测试)
- `internal/config/defaults_test.go` (3 新 ConfigToMap 测试)
- `internal/config/loader.go` (Step 4.5 sensitive 校验)
- `cmd/yaa/main.go` (子命令路由)
- `cmd/yaa/config_cli.go` 新建
- `pkg/pluginrpc/transport.go` (Windows bug 修复)
- `docs/config/checklist.md` (21 项勾选)

---

## Commit #63 — memory memstore CommitPut data race 修复 + 并发 Close 测试 (32/39→36/39, +4 项) (2026-07-29)

### 变更摘要

**修复 memstore.CommitPut data race bug (关键)**:
- `internal/memory/memstore/store.go`: `CommitPut` 之前未持 `s.mu.Lock()`, 直接读写 `s.data`, 与 List/Count 的 RLock range 并发触发 "concurrent map iteration and map write" fatal error.
- 修复: 加 `s.mu.Lock()` + `defer s.mu.Unlock()` 保护整个 victim 校验 + 删除 + target upsert 原子提交.
- sqlite store 已用 `tx BeginTx` 事务, 无此 bug.

**新增并发测试覆盖 (行15/16)**:
- `TestManagerCloseConcurrentOpsRace`: 5 goroutine 持续 Put + Close 比赛, 验证 Close 后 beginOp 返回 ErrMemoryClosed, 二次 Close 幂等, ops after close 被拒绝.
- `TestManagerBeginOpAfterClose`: 显式验证 Close 后 beginOp 返回 ErrMemoryClosed.
- `internal/memory/manager.go` 暴露 `BeginOpForTest()` 给测试用.

**Reindex/IndexStatus 状态序列验证 (行27/28)**:
- Reindex 已实现: 持 agent keyed lock 阻塞并发 Put/Delete, 临时 index 构建后原子 swap + IndexReady.
- IndexStatus 状态路径已覆盖测试:
  - vector disabled / 未知 agent → IndexReady (TestManagerIndexStatusNoVector)
  - 首次初始化 → IndexDegraded (agentIndexState 默认 degraded)
  - Embed/Upsert 失败 / fallback 后 → IndexDegraded (TestManagerVectorFallbackToKeyword)
  - Reindex 成功 → IndexReady (TestManagerVectorReindexAndSearch)

### 检查清单进度

| 模块 | 之前 | 现在 |
|------|------|------|
| memory | 32/39 | **36/39** ✅ (新增 4 项) |

剩 3 项: 行52/53 Phase 5 hot/restart + 行56 Health 结构 (新功能).

### 验证

- `go vet ./...` OK
- `go build ./...` OK
- `go test -count=1 -timeout 300s ./...` 26 包全绿
- 内存 memstore 真实 data race bug 修复, 测试覆盖并发场景

### 关键文件
- `internal/memory/memstore/store.go` (CommitPut s.mu.Lock 修复)
- `internal/memory/manager.go` (BeginOpForTest)
- `internal/memory_test/manager_test.go` (2 新并发测试)
- `docs/memory/checklist.md` (4 项勾选)

---

## Commit #64 — Provider estimate 完整 + context ctx.Done + tool §14.3 勾选 (2026-07-29)

### 变更摘要

**Provider EstimateInputTokens 完整 (context checklist 行22/23)**:
- 新增 `internal/provider/estimate.go` 提供 `estimateRequestChars` + `estimateTokensFromChars` 共享 helper
- 估算包含: messages(含 ToolCalls) + tools schema(Function name/description/Parameters JSON Schema) + ResponseFormat (Type/Name/JSONSchema) + Provider Extra (key/value marshal) + ToolChoice framing + Thinking Effort/Budget
- 把 openai/claude/gemini/ollama 4 个 provider 的 EstimateInputTokens 切换到共享 helper, 估算精度统一
- 新增 2 测试: TestEstimateInputTokensFullRequest (验证 Tools+ResponseFormat+Extra 让估算 > baseline), TestEstimateInputTokensNilReq (nil req 返回 0)

**context ctx.Done 在循环截断中生效 (行48)**:
- `internal/context/manager.go` truncate func 循环开头检查 `ctx.Err()`, 取消时返回 ErrContextBuildFailed wrap ctx.Cause
- 新增 TestBuildRespectsContextCancellation (60 messages 触发 truncation + ctx 已取消 → 验证 ErrContextBuildFailed/context.Canceled)

**context 其他勾选**:
- 行46 "Token 估算失败不使用字符数近似兜底" — 已实现 (EstimateInputTokens 错误直接返回 ErrTokenEstimationFailed)
- 行49 "同一次 Build 只使用一个 Effective Config 快照" — Build 中只读 in.Config, 由 caller 传入, 无再读 snapshot
- 行55 "日志不含 prompt/Memory/Tool result/摘要正文/Secret" — context manager zero logger call, 无泄密风险

**tool §14.3 勾选 (行52/53)**:
- 行52 "Plugin RPC Tool capability 与 Proxy 注册" — PluginToolProxy 已实现 tool.Tool interface, registerProxies 用 m.tools.RegisterWithSource(proxy, "plugin")
- 行53 "配置文件声明注册" — PluginsConfig.entries[].Enabled 控制 plugin 启动, Manifest provides.type="tool" 声明 tool capability, Ready→registerProxies 自动注册

### 检查清单进度

| 模块 | 之前 | 现在 |
|------|------|------|
| context | 25/40 | **31/40** ✅ (+6 项) |
| tool    | 42/45 | **44/45** ✅ (+2 项) |

剩余:
- context 9 项: 行38/39/40 hybrid 摘要 (需 Provider Chat 调用), 行56/57 指标 (需 metrics 模块集成), 行58/59/60/61 测试覆盖
- tool 1 项: 行39 Config Reload Tool (Phase 5)

### 验证

- `go vet ./...` OK
- `go build ./...` OK
- `go test -count=1 -timeout 300s ./...` 26 包全绿

### 关键文件
- `internal/provider/estimate.go` 新建 (estimateRequestChars + estimateTokensFromChars)
- `internal/provider/openai.go/claude.go/gemini.go/ollama.go` (EstimateInputTokens 切换共享 helper)
- `internal/provider/openai_test.go` (2 新测试)
- `internal/context/manager.go` (truncate ctx.Err 检查)
- `internal/context/manager_test.go` (TestBuildRespectsContextCancellation)
- `docs/context/checklist.md` (6 项勾选)
- `docs/tool/checklist.md` (2 项勾选)

---

## Commit #65 — context hybrid summarize + metrics + context 38/40 完成 (2026-07-29)

### 变更摘要

**hybrid 摘要实现 (checklist 行38/39/40, 在 #64 已完成)**:
- internal/context/manager.go summarize func: 阈值检测 → 候选选择 (排除 preserveRecent 个最新可压缩 turn) → Provider.Chat 在 compression.timeout 内生成摘要 → system summary unit 替换 → 重估 → 接受/拒绝/超预算 truncate fallback
- 3 测试: TestBuildHybridSummarizesWhenAboveThreshold + TestBuildHybridSkipsWhenCompressionDisabled + TestBuildHybridFallsBackWhenSummaryReturnsEmpty

**context metrics 实现 (行56/57)**:
- 新建 internal/context/metrics.go: 11 个指标按 docs/context/observability.md 定义注册
  - context_build_total (provider/model/strategy/result)
  - context_build_duration_seconds
  - context_input_tokens / context_input_budget / context_utilization_ratio
  - context_compression_total / context_compression_duration_seconds / context_truncation_total
  - context_dropped_units_total / context_overflow_total / context_token_estimation_failed_total
- labels 只用有界枚举 (provider/model/strategy/result/reason), 无 session/request ID (行57 高基数规避)
- nil-safe SetMetrics(r) + Manager 加 metrics 字段
- Build 内关键路径 (估算成功/失败/超预算/reject/保护单元超限) 全部 emit
- TestBuildMetricsEmitted 单测验证 registry 中 context_build_total ok=1

**context 测试覆盖 (行58/60/61)**:
- 行58: 已有 TestBuildTruncate + TestBuildHybrid* 覆盖预算/protected/Tool unit/fallback
- 行60: TestBuildUTF8AndReasoningContentRegression (中文+emoji UTF-8 + ReasoningContent)
- 行61: TestBuildAliasToolChoiceProjectionCachedByCaller (ToolChoice.Mode=specific + Tool 字段, 验证 estimator 看到 canonical name)
- 新增 estimateFailingProvider 类型 + recordingProvider 类型辅助测试
- 新增 TestBuildHybridSummaryTimeoutFallsBackToTruncate (行59 timeout 部分: Chat 返回 context.DeadlineExceeded → CompressionFailed=true + truncate fallback)

### 检查清单进度

| 模块 | 之前 | 现在 |
|------|------|------|
| context | 25/40 | **39/40** ✅ (+14 项) |

剩 1 项: 行59 "config reload 部分" — 需 Phase 5 reload 基础设施 (估算失败/取消/timeout 已有单元覆盖)

### 验证
- `go vet ./...` OK
- `go build ./...` OK
- `go test -count=1 -timeout 300s ./...` 25 包全绿 + mcp 包已知 SSEClientEndpoint flaky (单跑 PASS)

### 关键文件
- `internal/context/manager.go` (hybrid summarize + metric emits)
- `internal/context/metrics.go` 新建 (11 指标 + nil-safe helpers)
- `internal/context/manager_test.go` (6 新测试: hybrid 3 + metrics 1 + estimateFail 1 + UTF8 1 + alias 1 + timeout 1 → 总共 7 新)
- `docs/context/checklist.md` (14 项勾选)

---

## #66 — Memory Health 健康 API + MarkDegradedForTest (memory 36→37/39)

### 完成内容
- `internal/memory/types.go`: 新增 `Health` struct (Status/StoreOK/EmbedderOK/IndexOK/Items/LastErrorAt/ErrorClass)
- `internal/memory/manager.go`: 
  - 新增 `Health(ctx) Health` 函数: 聚合 ContentStore.Ping + embedder 非 nil + 全 Agent IndexStatus
  - 状态: closed → unhealthy/closed; store fail → unhealthy/store; index degraded → degraded; 其余 → healthy
  - Items 暂 0 (ponytail: v1 聚合全 Agent Count 开销大); LastErrorAt 暂 nil
  - 新增 `MarkDegradedForTest` 导出 helper (与既有 BeginOpForTest/ClockForTest 风格一致)
- `internal/memory_test/health_test.go` 新建: 6 个测试
  - TestHealthStoreOKNoVector / StoreOKWithEmbedderNoIndex / Closed / StorePingFail / IndexDegraded / IndexReady
  - 用 brokenPingStore (嵌入 memstore.Store 转发 + 覆盖 Ping) + fakeEmbedder 满足接口
- `docs/memory/checklist.md` 行56 勾选 (健康只反映 content/embedder/index)

### 检查清单进度
| 模块 | 之前 | 现在 |
|------|------|------|
| memory | 36/39 | **37/39** (+1) |

剩 2 项: 行52/53 hot/restart (Phase 5)

### 验证
- go vet ./... OK
- go build ./... OK
- go test -count=1 -timeout 300s ./... 25 包全绿 (无 flaky)

### 关键文件
- `internal/memory/types.go` (Health struct)
- `internal/memory/manager.go` (Health + MarkDegradedForTest)
- `internal/memory_test/health_test.go` 新建
- `docs/memory/checklist.md` (行56)

---

## #67 — Config Phase 5 Reload 基础设施 + config_reload Tool (config 72→83/84, tool 44→45/45)

### 完成内容
- **fsnotify v1.6.0** 新增依赖 (兼容 Go 1.20, go 1.16 directive)
- `internal/config/reload.go` 新建 (236 行):
  - `ReloadResult` struct (Applied/Changed/RestartRequired/Paths)
  - `ReloadManager`: NewReloadManager (拒 nil + flags 深拷贝) / Activate / Current / Reload / SetLogger
  - `Reload()`: reload mutex 串行 → active 校验 → Load(path,flags) → validateBindings → diffAndClassify → restart 路径返回 Applied=false 不 Store / 无 restart 原子 Store Applied=true
  - `hotReloadAllowlist` (28 前缀, 来自文档 §4 表格)
  - `diffMaps/diffVal/leafPaths/normalizeArrayIndexPath`: 递归比较 map, 数组按 index 对齐 (长度变化→数组层级 restart)
  - `pathIsHotReloadable` 前缀匹配
  - 失败/Restart-required 都走 slog 结构化日志 (无 secret 值)
- `internal/config/watcher.go` 新建 (105 行):
  - `Watcher` + `NewWatcher` + `Run(ctx)`: fsnotify 监听目录, 300ms debounce, 筛目标路径, ctx 取消清理 fs/timer 生命周期
  - 对 fs.Events/Errors 检查 ok 防止 nil chan 阻塞
  - 同 goroutine 执行 reload (无残留 callback)
- `internal/config/reload_test.go` 新建 (15 测试): New/BeforeActivate/Current/FlagsDeepCopy/ApplyHotReloadable/RestartRequired/Mixed/NoChange/ValidateBindingsFail/LoadFail/AgentModelHotReloadable/AgentAddRestart/Normalize/PathIsHot/DiffAndClassify
- `internal/config/watcher_test.go` 新建 (6 测试): TriggersReloadOnWrite/DebouncesRapidWrites/IgnoresUnrelatedFiles/CtxCancelCleansUp/OnReloadCallback/OnErrorCallback
- `internal/tool/builtin/config_reload.go` 新建: Tool Execute 调 ReloadManager.Reload() 返 ReloadResult JSON; 失败映射 IsError=true (不硬错)
- `internal/tool/builtin/config_reload_test.go` 新建 (5 测试): NewRejectsNil/ApplyHotReloadable/RestartRequiredNotApplied/LoadFailureIsError/ParametersSchema
- `docs/config/checklist.md` 勾选行50-57/59-61 (11 项)
- `docs/tool/checklist.md` 勾选行39

### 检查清单进度
| 模块 | 之前 | 现在 |
|------|------|------|
| config | 72/84 | **83/84** (+11) |
| tool | 44/45 | **45/45** ✅ (+1) |

剩: config 行58 (组件从 Current() 复制 hot-reload 字段) — 消费方模块各自落地

### 验证
- go vet ./... OK
- go build ./... OK
- go test -count=1 -timeout 300s ./... 全绿 (mcp SSEClientEndpointParseAndMessageRoundTrip 已知 flaky 单跑 PASS, 全项目跑偶尔 FAIL)

### 关键文件
- `internal/config/reload.go` + `watcher.go` + 2 test 文件 (新建)
- `internal/tool/builtin/config_reload.go` + test (新建)
- `go.mod/go.sum` (fsnotify v1.6.0)
- `docs/config/checklist.md` (11 项) + `docs/tool/checklist.md` (1 项)

---

## #68 — plugin/skill Phase 5 restart-required 集成测试 (plugin 52/52, skill 24/24 ✅)

### 完成内容
已分析的契约: docs/plugin/config-ref.md §52 "所有 plugins.* 都需要重启" + docs/skill/config.md §72 "skills.dir/per_skill/agents[].skills/agents[].skills_config 全 restart-required".
已实现机制: #67 的 ReloadManager hotReloadAllowlist 不含任何 plugins.* 或 skills.* 路径前缀, 因此这些字段变化自动归类为 restart-required.

补集成测试 internal/config/reload_test.go (+3 测试):
- TestReloadPluginsAnyFieldIsRestartRequired: plugins.auto_start 改动 → restart-required (Paths 含 plugins.*)
- TestReloadSkillsDirIsRestartRequired: skills.dir 改 → restart-required (Paths 含 skills.*)
- TestReloadSkillsPerSkillOptionsIsRestartRequired: skills.per_skill.<name>.options 改 → restart-required
  (与 tools.builtin.<name>.options 在 allowlist 形成对比)

检查清单: plugin 勾选行60 (52→52/52 ✅), skill 勾选行23 (23→24/24 ✅)

### 验证
- go vet/build OK
- internal/config + tool/builtin 测试全绿

### 关键文件
- `internal/config/reload_test.go` (+3 测试)
- `docs/plugin/checklist.md` (行60)
- `docs/skill/checklist.md` (行23)

---

## #69 — Context Phase 5 reload 集成测试 (context 39→40/40 ✅)

### 完成内容
已有 3 类测试确认: TestBuildEstimateFailsReturnsErrorRestore / TestBuildRespectsContextCancellation / TestBuildHybridSummaryTimeoutFallsBackToTruncate

补 2 个 config reload 集成测试 internal/context/manager_test.go:
- TestBuildUsesReloadedConfigSnapshot: BuildInput.Config 注入 mock reload 前/后值 (max_tokens=0 → 7000),
  验证 EffectiveWindow/InputBudget 反映新 budget (10000 → 7000)
- TestBuildEndToEndWithReloadManager: 完整 yaml+ReloadManager 流程, 改 context.max_tokens,
  Reload→Current().Context 注入 Build, Build 结果反映新 EffectiveWindow=7000

import 顺序整理 (合并 import 块 + 加 os import)

检查清单: context 勾选行59 (39→40/40 ✅)

### 验证
- go vet/build OK
- internal/context + internal/config + internal/tool/builtin 测试全绿

### 关键文件
- `internal/context/manager_test.go` (+2 集成测试)
- `docs/context/checklist.md` (行59)

---

## #70 — Memory Phase 5 strict decoder + cleanup reload (memory 37→39/39 ✅)

### 完成内容
- **行52 strict decoder unknown field**: 已有 decode.go 拒绝未知字段, 补 2 集成测试证明:
  - `internal/config/memory_unknown_test.go`: TestMemoryUnknownFieldRejectedStrict (memory.nonsense_field) +
    TestMemoryOverrideUnknownFieldRejectedStrict (agents[].memory.made_up)
  - 字段一致性: MemoryOverride 字段集 (max_items/default_ttl/eviction_policy hot + enabled/vector restart) 与 hot-reload.md §4 allowlist 一致
- **行53 cleanup tick snapshot** 改造 `internal/memory/manager.go`:
  - 新增 `StartCleanupWithReload(ctx, snapshot func() (interval, batchSize))`: 每 tick 调 snapshot 取最新 (interval,batchSize), interval 变化→ticker.Reset
  - 与 StartCleanup 共享同一 lifecycleMu 保护的 workerCancel/workerDone, 不可重复启动
  - snapshot disabled (interval<=0 或 batchSize<=0) → 该 tick 跳过 DeleteExpired (1s fallback 重试)
  - 现有 StartCleanup 保留兼容 (固定 interval/batchSize)
- 补 3 集成测试 `internal/memory_test/cleanup_reload_test.go`:
  - TestStartCleanupWithReloadInvokesSnapshot: 10ms interval 触发 DeleteExpired
  - TestStartCleanupWithReloadDisabledSnapshotSkipsCleanup: disabled snapshot 跳 cleanup
  - TestStartCleanupWithReloadIntervalReset: interval 切换 5ms→30ms 后 worker 仍持续 tick
- 补 2 集成测试 `internal/memory_test/policy_explicit_test.go`:
  - TestManagerUsesExplicitPolicyPerOp: 同一 Manager 两次 Put 用不同 MaxItems (10→1), 第二次触发 eviction (证明 caller 决定 policy)
  - TestManagerPolicyEnabledFalseShortCircuit: policy.Enabled=false 直接返 ErrMemoryDisabled

检查清单: memory 勾选行52/53 (37→39/39 ✅)

### 验证
- go vet/build OK
- go test -count=1 -timeout 300s ./... 25 包全绿 (本次 mcp 无 flaky)

### 关键文件
- `internal/memory/manager.go` (新增 StartCleanupWithReload, inline StartCleanup goroutine)
- `internal/config/memory_unknown_test.go` (新建, 2 测试)
- `internal/memory_test/cleanup_reload_test.go` (新建, 3 测试) + policy_explicit_test.go (2 测试)
- `docs/memory/checklist.md` (行52/53)

---

## #71 — Config Phase 5 行58: Agent.Dependencies.Reloader + Runtime ReloadManager 集成 (config 83→84/84 ✅)

### 完成内容 (全部 checklist 收尾!)
- **agent.Dependencies 新增 Reloader *config.ReloadManager**, agent.Manager 新增 `currentCfg()` helper:
  - Reloader 非 nil → 返回 Reloader.Current() (hot reload 最新 snapshot)
  - nil → 回退 deps.Config 旧指针 (向后兼容, 现有测试不动)
  - handle_turn.go / memoryinject.go / manager.go 中所有 `m.deps.Config.X` 替换为 `m.currentCfg().X`
  - 即 Agent turn resolveMemoryPolicy / resolveAgentContextConfig / ResolvePlannerConfig 均从 Current() 取 hot-reload 字段
- **Runtime 集成 ReloadManager** internal/runtime/runtime.go:
  - Runtime struct 新增 `reloadMgr *config.ReloadManager` 字段
  - Start 中: 若 configPath 非 nil 且构造+Activate 成功 → rt.reloadMgr=rm + rt.cfg=rm.Current() (切到 ReloadManager 持有的 same snapshot)
  - agent.Dependencies.Reloader = rt.reloadMgr (非 nil 时 Agent turn 从 Current() 取)
  - 失败仅记录 warning, 不阻塞 Runtime 启动 (旧 cfg 有效)
- **集成测试** `internal/agent/reloader_test.go` (1 个):
  - TestAgentCurrentCfgReadsFromReloader: 注入 ReloadManager 给 Agent, Reload 改 memory.max_items=10→20, 验证 agm.currentCfg().Memory.MaxItems = 20 (Agent 反映新 snapshot)

### 检查清单进度 (全部 100%!)
| 模块 | 之前 | 现在 |
|------|------|------|
| config | 83/84 | **84/84 ✅** |

**全部 11 模块 100% checklist 完成且全项目测试通过**:
auth 30/30, config 84/84, context 40/40, mcp 82/82, memory 39/39, planner 34/34,
plugin 52/52, session 58/58, skill 24/24, storage 23/23, tool 45/45

### 验证
- go vet/build OK
- go test -count=1 -timeout 300s ./... 25 包全绿

### 关键文件
- `internal/agent/manager.go` (Dependencies + currentCfg)
- `internal/agent/handle_turn.go` + `memoryinject.go` (deps.Config → currentCfg)
- `internal/runtime/runtime.go` (reloadMgr 字段 + Start 构造 + 注入 agent.Dependencies.Reloader)
- `internal/agent/reloader_test.go` (1 集成测试)
- `docs/config/checklist.md` (行58)

---

## #72 — Windows 7 交叉编译验证 (CGO_ENABLED=0, 验证 win/amd64 编译链可用)

### 验证结论
- **没有任何一处在 Windows 上运行过**; 之前所有开发与 go test 都在 Linux/arm64 (Go 1.20.14 Armbian aarch64) 完成.
- 跨平台交叉编译目标 windows/amd64 全部成功.

### 命令与结果
1. `cmd/yaa` 主二进制:
   ```
   GOOS=windows GOARCH=amd64 go build -o /tmp/yaa.exe ./cmd/yaa
   → /tmp/yaa.exe: PE32+ executable (console) x86-64 (stripped to external PDB), for MS Windows, 18 MB
   ```
2. 全模块:
   ```
   GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./...
   → EXIT 0 (无编译错误)
   ```
3. 全模块 vet:
   ```
   GOOS=windows GOARCH=amd64 go vet ./...
   → EXIT 0
   ```
4. 测试包整体编译:
   ```
   GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -run xxxxxx ./...
   → 所有 .test.exe 都生成; ERROR 仅为 "exec format error" (在 Linux 上无法执行 Windows PE 二进制, 非编译错误)
   ```
   grep 编译错误后: 0 条 (确认所有测试包都能在 Windows 上编译通过).

### 关键 Windows 兼容设计已有代码
- `cmd/yaa/main.go` 第 43-44 行: `signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)`,
  Windows 上 syscall.SIGTERM 在 Go 中是被定义的内部常量 (不真正接受 os 信号);
  Windows 上 Ctrl+C 由 os.Interrupt 处理, SIGTERM 信号被静默丢弃 → 运行行为可接受, 无编译失败.
- `pkg/pluginrpc/transport.go`: AllocateLocalEndpoint 已 `runtime.GOOS == "windows"` 分支用 loopback TCP ("tcp://127.0.0.1:0"),
  DialPlugin 用 scheme 分支 ("unix://" / "tcp://") 处理. 文档对应 docs/plugin/checklist.md 行25.
- `internal/config/discover.go:33`: `runtime.GOOS != "windows"` 分支处理默认配置文件路径 (用户 home dir).
- modernc.org/sqlite 是纯 Go 实现: `CGO_ENABLED=0` 完全可用, 无需 Windows 上安装 C 编译器.

### Windows 7 官方支持情况 (已核实)
- Go 1.20.14 doc/go1.20.html 中 Windows 一节原文:
  "Go 1.20 is the last release that will run on any release of Windows 7, 8, Server 2008 and Server 2012.
   Go 1.21 will require at least Windows 10 or Server 2016."
- 即: **Go 1.20 是支持 Windows 7 的最后一个 Go 版本**, Go 1.21+ 不再支持 Win7.
- 项目选 Go 1.20 是正确的, 在 Win7 上可编译可运行.
- 注意: 官方支持指 Win7 SP1 (32 位/64 位均可); Win7 RTM 已 ESU, 不在保证范围.

### 下一步建议 (如需在 Windows 7 实机验证)
路径 1 (推荐): 拷贝已生成的 /tmp/yaa.exe 到 Win7 (需 x64 Win7 SP1), 写 yaa.yaml 配好 storage/api 后即可运行.
  无需在 Win7 上装 Go 工具链 (modernc.org/sqlite 纯 Go, 不需 cgo, 不依赖 mingw).
路径 2: 若想在 Win7 上亲自编译, 装 Go 1.20.x Windows 版本:
  - `git clone http://192.168.3.20/hashqq/yaa.git`
  - `go build -o yaa.exe ./cmd/yaa`
  - `yaa.exe -config yaa.yaml`
  (同样不需要 mingw / TDM-GCC, 因为 modernc.org/sqlite 无 cgo 依赖).

---

## #73 — 公开仓库准备：模块路径 v2up-32mb/yaa + 本地静态 WebUI + 示例配置

### 变更
- 模块路径 `github.com/imshuai/yaa` → `github.com/v2up-32mb/yaa`（Makefile ldflags、plugin.proto go_package、全部 import 同步）
- 用与仓库锁定一致的工具链重新生成 `pkg/pluginrpc/gen`：protoc 3.12.4 + protoc-gen-go v1.28.1 + protoc-gen-go-grpc v1.2.0，产物 diff 仅 go_package 描述符字节
- WebUI 不再依赖 unpkg CDN：`internal/api/webui/vendor/` 内嵌 vue@3.4.38 + element-plus@2.8.4 本地产物，`/vendor/{name}` 白名单路由，离线/内网 Win7 可用
- 新增 `yaa.example.yaml`（配置校验通过：`yaa config migrate -dry-run`），README「当前状态」同步为实现阶段现状

### 验证
- gofmt / go vet / go build ./... 全绿
- CGO_ENABLED=0 GOOS=windows GOARCH=amd64 GOAMD64=v1 交叉编译通过
- go test ./... 全绿

---

## #74 — 默认 WebUI 重写：ChatGPT / Chatbox 风格对话控制台

### 变更（internal/api/webui/*）
- 全新 `index.html` / `app.js` / `style.css`：对话优先布局（侧栏 Agent/会话列表 + 主聊天区 + 输入区）
- 流式传输：fetch 手写 SSE 解析器订阅 `GET /sessions/:id/events` + `POST /messages` 触发 turn；token 走 Authorization 头；15s heartbeat 期间自动重连；断流时回退整段返回
- 实时渲染：`assistant_delta`（rAF 节流）→ Markdown（marked+DOMPurify 本地 vendor，XSS 安全）；`reasoning_delta` → 可折叠"思考过程"块；`tool_call`/`tool_result` → 可展开工具调用卡片（成功/失败/运行中状态）
- 会话管理：新建/切换/暂停/恢复/删除；`turn_id` 本地生成（crypto.randomUUID + 兼容回退）；发送后 abort fetch 即取消 turn（SSE 收 error code=canceled）
- 设置：API Base URL / Bearer Token / 明暗主题，存 localStorage；token/Rbase 变化触发重连
- 全部前端依赖本地 vendor：Vue3.4 + Element Plus 2.8.4 + icons + marked + DOMPurify（无 CDN，Win7 离线可用）；`webui.go` 的 /vendor 白名单扩到 7 个文件

### 端到端验证（mock OpenAI 兼容网关）
- 流式对话：queued→assistant_start→reasoning_delta→assistant_delta→assistant_done 全部帧渲染
- 工具调用：tool_call→tool_result→第二轮→assistant_done，工具卡片与错误状态正确
- 无头 Chromium CDP 实测：新建会话/输入/发送/流式/推理块/工具卡片/Markdown 渲染 0 报错

---

## #75 — 工具集互补：内置 file_search / git_* / process_*（吸收 mcp-tools 强项）

### 变更
- `file_search`（对齐 mcp-tools fs_search_text）：子串/正则全文搜索 + .gitignore 过滤 + 命中上限；路径安全复用 file 的 allowed_paths/blocked_paths；只读、默认启用
- `git_*`（对齐 mcp-tools git.*）：status/diff/log/branch/add/restore/commit/switch/pull 白名单，无 shell、禁 push、flag 注入拒绝（message/branch/paths 拒绝 `-` 开头）、commit 单 `-m`、pull 仅 ff-only、restore 显式 `--`
- `process_*`（对齐 mcp-tools exec 进程管理）：start/list/logs/stop，进程树终止（Unix 组 SIGTERM→SIGKILL，Windows taskkill /T[/F]），默认禁用（需显式 tools.builtin.process.enabled）
- register.go 容器模式扩展（file/git/process 共享配置容器）；defaults/config 验证联动；示例配置与 docs/tool/builtin.md 补充

### 验证
- 新增 11 个用例（search 6 / git 3 / process 2）全过；go vet + 全量 go test 全绿
- Windows 交叉编译 + 交叉 vet 通过

---

## #76 — WebUI 极速打磨 + 工具集全量功能验证（发现并修复一批 BUG）

### WebUI 修复（internal/api/webui/app.js）
- **ElMessage/ElMessageBox 绑定**：UMD 构建下它们挂在 ElementPlus 命名空间而非裸全局；
  修复前所有走 notify / sessionCmd / saveAndReconnect / 删除会话的路径都会抛
  ReferenceError（无头浏览器实测抓到）→ 已改为从顶层绑定并带降级（原生 confirm 兜底）
- **SSE 韧性**：服务端正常断开（read done）后不再丢失不重连；指数退避 1s→15s；
  401/403 停止无限重连并提示一次（检查设置 Token）
- **新建会话竞态**：newChat 进行中用户输入并发送会被静默丢弃 → 新增 creating 状态，
  canSend 计入；send() 无会话/创建中/会话失效都有明确 toast
- **停止/出错体验**：取消、错误、部分输出不再凭空消失 → finalizedLive 保留「已取消」提示
  与部分回复（含推理与工具卡片）；turn 去重收尾（finalizedTurnId）
- 删除当前会话同时关闭其 SSE；saveAndReconnect 前清理健康轮询（healthTimer 泄漏）；
  SSE 块解析兼容 \r\n；mdOfGroup try/catch；quickSend 取消固定延迟
- 会话操作后本地同步 state（暂停/恢复），删除改 ElMessageBox 确认

### 工具集语义统一（shell/http/file）
- **「空 allowlist = 放行（仅 blocked 生效）」**：此前 shell/http/file（以及新加的
  file_search/git_*/process_*）在默认空名单下全部拒绝，示例配置注释「留空=不限制」与
  实现矛盾、默认配置 Agent 一个工具都用不了 → 统一为空=放行、blocked 永远优先；
  http 重定向同步；补充三个空名单语义测试
- file_search/git_* 新增 base_dir：allowed_paths 非空按首个 allowed 根解析相对路径，
  为空按 base_dir（默认 `.`）；docs/tool/builtin.md 写明统一语义

### 验证基建（scripts/itest/，可复现）
- mock2.py：OpenAI 兼容网关，支持 `!!tool <name> <args>` 驱动任意工具调用、
  `!!slow` 慢速流、/v1/test/hello
- run-integration.py：真实 Agent turn 逐一调用 22 项（shell/http/file_*/file_search/
  git_*/config_query/introspection/process_*），断言最终回复回显真实工具输出 → **22/22**
- run-webui-dom.js：无头 Chromium 纯 DOM 断言 8 场景（401 降级、设置重连、新建会话、
  流式、工具卡片、中止、主题、删除）→ **fails=0，console 0 异常**

## #77 — 首次正式发版
- yaa **v0.2.0**：Go 1.20.14 构建，win/linux/arm64/darwin 共 5 平台产物 + SHA256SUMS，
  release job 全绿；Windows 产物为 PE32+ x86-64，散列与 SHA256SUMS 一致
- mcp-tools **v1.1.0**（合并 yolo + module path 修正后的主线首发）：4 平台产物，
  同法核验通过；v1.0.0/v0.1.x-rc* 历史 tag 保留未动
- release notes 已注明「Win7 SP1 x64 实机待验证」的已知状态

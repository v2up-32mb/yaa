# Plugin RPC 接口

> 文档路径: `docs/plugin/interface.md`
> 上级: [README.md](README.md)
> 目标 IDL: `api/plugin/v1/plugin.proto`

---

## 1. 边界

Runtime 只作为 gRPC Client 连接 Plugin 进程提供的 gRPC Server。没有反向 Runtime gRPC Server；Plugin 不接收 Runtime 指针、Manager、数据库连接或内部 Go interface。

```text
Runtime Plugin Manager
  └─ gRPC Client ── local IPC ── Plugin gRPC Server
       └─ Tool Proxy                 └─ Tool handler
```

Unix 使用 Unix Socket；Windows 7 使用仅绑定 loopback 的 TCP。MVP 不接受远程 Plugin endpoint。

## 2. 生命周期

```text
Handshake(runtime_protocol="1", expected_plugin_id)
  → HandshakeResponse(protocol_version, plugin_id, plugin_version, startup_nonce)
Init(expanded_config)
  → Ack
Ready()
  → ReadyResponse(capabilities)
Health()
  → HealthResponse
Stop()
  → Ack
```

Runtime 为每次启动生成 32-byte random nonce，并只通过 `YAA_PLUGIN_STARTUP_NONCE` 子进程环境传入。Plugin SDK 的 HandshakeResponse 必须原样回显 `startup_nonce`；Runtime 使用 constant-time compare 同时校验 nonce、身份和 RPC major。Handshake 不返回 capability。Init 成功后 Runtime 调用 Ready 并缓存 capability；Manifest `provides[]` 与 Ready 结果的 type/name/description/schema 集合必须精确一致，之后才注册 Proxy。

```go
type Plugin interface {
    ID() string
    Version() string
    Init(ctx context.Context, cfg map[string]any) error
    Capabilities() []CapabilityDescriptor
    Health(ctx context.Context) HealthStatus
    Stop(ctx context.Context) error
}

type CapabilityDescriptor struct {
    Type   string         `json:"type"` // v1 只有 tool
    Name   string         `json:"name"`
    Description string    `json:"description"`
    Schema map[string]any `json:"schema,omitempty"`
}
```

Plugin SDK 可把该进程内接口适配到生成的 gRPC Server，但 Runtime 不依赖此 Go interface。

## 3. IDL 服务

**当前状态：已落地 `api/plugin/v1/plugin.proto`，该文件是唯一 wire contract。下面代码块降为非权威镜像，仅用于阅读上下文；不一致时以 proto 文件为准。CI 必须校验 proto、生成物和保留镜像的一致性。仓库模块路径 `github.com/v2up-32mb/yaa`。**

```proto
syntax = "proto3";

package yaa.plugin.v1;

option go_package = "github.com/v2up-32mb/yaa/pkg/pluginrpc/gen;pluginv1";

import "google/protobuf/struct.proto";
import "google/protobuf/timestamp.proto";

service PluginService {
  rpc Handshake(HandshakeRequest) returns (HandshakeResponse);
  rpc Init(InitRequest) returns (Ack);
  rpc Ready(ReadyRequest) returns (ReadyResponse);
  rpc Health(HealthRequest) returns (HealthResponse);
  rpc Stop(StopRequest) returns (Ack);

  rpc InvokeTool(ToolRequest) returns (ToolResponse);
}

message Ack {}

message HandshakeRequest {
  string runtime_protocol = 1;
  string expected_plugin_id = 2;
}

message HandshakeResponse {
  string protocol_version = 1;
  string plugin_id = 2;
  string plugin_version = 3;
  string startup_nonce = 4;
}

message InitRequest {
  google.protobuf.Struct config = 1;
}

message ReadyRequest {}

enum CapabilityType {
  CAPABILITY_TYPE_UNSPECIFIED = 0;
  CAPABILITY_TYPE_TOOL = 1;
}

message CapabilityDescriptor {
  CapabilityType type = 1;
  string name = 2;
  google.protobuf.Struct schema = 3;
  string description = 4;
}

message ReadyResponse {
  repeated CapabilityDescriptor capabilities = 1;
}

message HealthRequest {}

enum HealthLevel {
  HEALTH_LEVEL_UNSPECIFIED = 0;
  HEALTH_LEVEL_HEALTHY = 1;
  HEALTH_LEVEL_DEGRADED = 2;
  HEALTH_LEVEL_UNHEALTHY = 3;
}

message HealthResponse {
  HealthLevel level = 1;
  string message = 2;
  google.protobuf.Timestamp observed_at = 3;
}

message StopRequest {}

message ToolRequest {
  string plugin_id = 1;
  string request_id = 2;
  string agent_id = 3;
  string session_id = 4;
  string name = 5;
  google.protobuf.Struct arguments = 6;
}

message ToolResult {
  string content = 1;
  bool is_error = 2;
  google.protobuf.Struct meta = 3;
}

enum ToolErrorCode {
  TOOL_ERROR_CODE_UNSPECIFIED = 0;
  TOOL_ERROR_CODE_INVALID_ARGUMENT = 1;
  TOOL_ERROR_CODE_TIMEOUT = 2;
  TOOL_ERROR_CODE_UNAVAILABLE = 3;
  TOOL_ERROR_CODE_INTERNAL = 4;
}

message ToolError {
  ToolErrorCode code = 1;
  string message = 2;
  bool retryable = 3;
}

message ToolResponse {
  string request_id = 1;
  oneof outcome {
    ToolResult result = 2;
    ToolError error = 3;
  }
}
```

每个业务请求都包含 `plugin_id` 和 `request_id`；ToolRequest 还必须携带 Tool Manager 已验证的 `agent_id` 和真实 `session_id`（非 Session 调用为空）。deadline 与取消只由 gRPC context 传播，不复制成可能漂移的载荷字段。`ToolResponse.request_id` 必须精确回显请求值，且 `outcome` 恰有一个分支；缺失、错 ID 或未知 enum 都是 `ErrPluginProtocolIncompatible`，Proxy 必须使当前 handle unavailable 并通过 `RPCClient.Terminate()` 回收进程和 transport。响应只能返回可序列化数据，不返回 Go type name 或进程内地址。

## 4. Capability 载荷

### 4.1 Tool

```json
{
  "plugin_id": "weather",
  "request_id": "call_01",
  "agent_id": "default",
  "session_id": "ses_01J...",
  "name": "weather",
  "arguments": {"city": "Shanghai"}
}
```

Tool response 的 result 映射为统一 `tool.ToolResult`；error 映射为 typed hard error。`retryable=true` 只允许在 Plugin 能保证尚未产生外部副作用时设置，Proxy 再实现 Tool Manager 的 `RetryableError`；timeout、结果不确定或已经产生副作用时必须为 false。error message 使用稳定、脱敏且不超过 512 UTF-8 bytes 的文本。Plugin 不能从 arguments 推导或覆盖 execution scope。

## 5. 健康与错误

```go
type HealthStatus struct {
    Level     string    `json:"level"` // healthy | degraded | unhealthy | unknown
    Message   string    `json:"message"`
    Timestamp time.Time `json:"timestamp"`
}
```

Health 必须在 `plugins.health_timeout` 内返回。单次失败只标记 degraded，不杀进程；自动重启只由运行期间意外进程退出触发。

gRPC status 到 Runtime error 的唯一映射见 [errors.md](errors.md)。错误 message 必须脱敏并限制长度；Plugin 堆栈只写 Plugin 自己的日志。

## 6. 版本兼容

`protocol_version` 是 RPC major 字符串。MVP Runtime 和 Plugin 都只支持 `"1"`，不相等即拒绝启动。同一 major 只允许新增可忽略的 Protobuf 字段；v1 Handshake 没有 supported-capability 输入，且 Runtime 要求 Manifest/Ready capability 精确相等，因此新增 capability type、删除字段、复用 field number 或改变语义都必须升级 major。

Plugin 的业务 `version` 与 `requires_runtime` 使用 SemVer，但不替代 RPC major 检查。SDK 不提供 `PluginV2` 类型断言。

## 7. 目录

```text
api/plugin/v1/plugin.proto       # 目标：落地后成为唯一 wire contract（当前待创建）
pkg/pluginrpc/gen/               # 目标：由 protoc 生成（当前待创建）
pkg/pluginrpc/client.go          # Runtime client adapter
pkg/pluginrpc/server.go          # Plugin SDK server adapter
pkg/pluginrpc/transport.go       # Unix Socket / loopback TCP
internal/plugin/                 # Runtime Manager/Loader/Proxy
```

---

*最后更新: 2025-07-17*

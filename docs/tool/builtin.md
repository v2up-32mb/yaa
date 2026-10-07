# 内置 Tool — 通用执行类

> 文档路径: `docs/tool/builtin.md`
> 上级: `docs/tool/README.md` §6

---

## 6. 内置 Tool

Yaa! 的内置 Tool 分为三大类：

| 类别 | Tool 列表 | 说明 | 文件 |
|------|-----------|------|------|
| **通用执行** | `shell`, `http`, `file_read`, `file_write`, `file_list`, `file_delete` | 基础操作能力 | 本文件 |
| **配置管理** | `config_query`, `config_reload` | 读取脱敏配置、按文件 watcher 同一流程 reload | [config-tools.md](config-tools.md) |
| **内视** | `runtime_status`, `agent_list`, `agent_inspect`, `session_list`, `session_inspect`, `tool_list`, `skill_list`, `provider_list`, `mcp_list` | 固定的只读运行态视图 | [introspection.md](introspection.md) |

### 6.1 Shell

执行 Shell 命令，最强大的通用 Tool。

```go
type ShellTool struct {
    options EffectiveShellOptions
}

// EffectiveShellOptions 由 config.ToolConfig.Options 严格解码；不参与 YAML 解码。
type EffectiveShellOptions struct {
    AllowedCommands []string
    BlockedCommands []string
    WorkingDir      string
    Env             map[string]string
    MaxOutputBytes  int
}
```

**Parameters Schema：**

```json
{
  "type": "object",
  "properties": {
    "command": {
      "type": "string",
      "description": "The shell command to execute"
    }
  },
  "required": ["command"]
}
```

**安全策略：**

| 配置 | 说明 |
|------|------|
| `allowed_commands` | 前缀白名单；**空列表 = 不限制**（仅 `blocked_commands` 生效） |
> **统一语义**：shell/http/file 及各工具的 `allowed_*` 白名单「空 = 放行」，非空才收紧；`blocked_*` 永远优先。相对路径在 `allowed_*` 非空时基于首个允许根解析，为空时基于 `base_dir`（默认 `.`）解析。
| `blocked_commands` | 黑名单模式，禁止列出的命令前缀（如 `rm -rf`, `mkfs`） |
| 两者同时配置 | 黑名单优先 |

**输出处理：**

- stdout + stderr 合并返回
- 超过 `max_output_bytes` 时截断，尾部附加 `[output truncated]`
- 退出码非 0 时 `IsError=true`，Content 包含退出码和错误输出

### 6.2 HTTP

发送 HTTP 请求。

```go
type HTTPTool struct {
    options EffectiveHTTPOptions
}

// EffectiveHTTPOptions 由 config.ToolConfig.Options 严格解码；不参与 YAML 解码。
type EffectiveHTTPOptions struct {
    MaxRedirects     int
    AllowedHosts     []string
    BlockedHosts     []string
    MaxResponseBytes int
}
```

每次初始请求和重定向都对 `url.Hostname()` 的小写结果做精确匹配；`blocked_hosts` 优先。**`allowed_hosts` 为空 = 放行任意 host**（仅查 blocked）；非空时作为精确 allowlist。达到 `max_redirects` 或目标不允许时停止，不向目标发送下一跳请求。

**Parameters Schema：**

```json
{
  "type": "object",
  "properties": {
    "method": {
      "type": "string",
      "enum": ["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD"],
      "default": "GET"
    },
    "url": {
      "type": "string",
      "description": "The request URL"
    },
    "headers": {
      "type": "object",
      "description": "Request headers"
    },
    "body": {
      "type": "string",
      "description": "Request body (for POST/PUT/PATCH)"
    }
  },
  "required": ["url"]
}
```

**返回内容：**

```json
{
  "status_code": 200,
  "headers": {"content-type": "application/json"},
  "body": "...",
  "elapsed_ms": 342
}
```

### 6.3 File

文件操作 Tool，实际拆分为 4 个子 Tool：

| Tool 名称 | 功能 | Schema 关键参数 |
|-----------|------|----------------|
| `file_read` | 读取文件内容 | `path`, `encoding` (utf-8/base64), `max_bytes` |
| `file_write` | 写入文件 | `path`, `content`, `create_dirs` |
| `file_list` | 列出目录内容 | `path`, `recursive` |
| `file_delete` | 删除文件或空目录 | `path` |

**安全策略：**

```yaml
tools:
  builtin:
    file:
      enabled: true
      options:
        allowed_paths: ["/tmp", "/workspace"]  # 允许访问的路径根目录
        blocked_paths: ["/etc", "/root/.ssh"]   # 禁止访问的路径根目录
        max_file_size: "10MB"                    # 单次读写上限
```

**路径校验：**

```go
func canonicalPath(path string) (string, error) {
    current, err := filepath.Abs(path)
    if err != nil {
        return "", err
    }
    current = filepath.Clean(current)

    // 写入目标可能尚不存在；先解析最近的已有祖先，再接回缺失部分。
    var tail []string
    for {
        if _, err = os.Lstat(current); err == nil {
            break
        }
        if !errors.Is(err, os.ErrNotExist) {
            return "", err
        }
        parent := filepath.Dir(current)
        if parent == current {
            return "", err
        }
        tail = append(tail, filepath.Base(current))
        current = parent
    }
    current, err = filepath.EvalSymlinks(current)
    if err != nil {
        return "", err
    }
    for i := len(tail) - 1; i >= 0; i-- {
        current = filepath.Join(current, tail[i])
    }
    return filepath.Clean(current), nil
}

func within(path, root string) bool {
    rel, err := filepath.Rel(root, path)
    return err == nil && rel != ".." && !filepath.IsAbs(rel) &&
        !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func validatePath(path string, allowed, blocked []string) (string, error) {
    target, err := canonicalPath(path)
    if err != nil {
        return "", err
    }
    for _, root := range blocked { // root 在启动时同样 canonicalize。
        if within(target, root) {
            return "", fmt.Errorf("path is blocked")
        }
    }
    if len(allowed) > 0 {
        for _, root := range allowed {
            if within(target, root) {
                return target, nil
            }
        }
        return "", fmt.Errorf("path is not in allowed paths")
    }
    return target, nil
}
```

所有 configured roots 必须是绝对路径，并在启动时通过同一 `canonicalPath` 解析；失败即配置错误。每次操作只使用返回的 canonical target，不能在校验后重新使用原始 path。`filepath.Rel` 提供目录边界，因此 `/tmpfoo` 不属于 `/tmp`；解析最近已有祖先则同时覆盖 symlink escape 和新建文件场景。

### 6.4 file_search（mcp-tools 互补）

子串或正则全文搜索，对齐 mcp-tools `fs_search_text` 能力：

| 参数 | 类型 | 说明 |
|------|------|------|
| `path` | string | 必填；文件或目录，相对路径：`allowed_paths` 非空时基于第一个 allowed 根，否则基于 `base_dir`（默认 `.`） |
| `query` | string | 必填；子串，或 `regex=true` 时的 Go 正则 |
| `regex` | bool | 编译失败返回 `invalid regex` |
| `use_gitignore` | bool | 默认 true；读取根目录 `.gitignore`（精确路径 / `*` / `**/` 前缀 / 尾部 `/`；`!` 取反、`?`、`[abc]` 不支持） |
| `limit` | int | 1..1000，默认取自 `max_matches`（200） |

Config（`tools.builtin.file_search.*`）：`allowed_paths` / `blocked_paths`（复用 file 策略，`validatePath` 同源）、`max_matches`、`max_line_bytes`、`enable_gitignore`。**只读**，默认启用。

### 6.5 git_*（mcp-tools 互补）

`git_status` / `git_diff` / `git_log` / `git_branch` / `git_add` / `git_restore` / `git_commit` / `git_switch` / `git_pull`，对齐 mcp-tools `git.*` 白名单设计：

- 只允许上述固定子命令；`push` / `rebase` / 任意透传**不在白名单**（构造期即无该工具）
- 参数形态固定、无 shell；`repo_path` 相对路径：`allowed_paths` 非空时基于第一个 allowed 根，否则基于 `base_dir`（默认 `.`）
- 防注入：`message` / `branch` 拒绝 `-` 开头；`paths` 拒绝 `-` 开头；`restore` 显式加 `--`
- `commit` 用单 `-m` 参数传消息；`pull` 仅 `--ff-only`；`log` 限 `-n ≤100`
- Config（`tools.builtin.git.*`）：`allowed_paths`/`blocked_paths`（空 = 放行，blocked 优先）、`base_dir`、`allowed_subcommands`（覆盖默认白名单）、`max_output_bytes`
- 输出 ≤128KB 截断；非零退出 `IsError=true`

### 6.6 process_*（mcp-tools 互补）

`process_start` / `process_list` / `process_logs` / `process_stop`：后台进程管理。

- **默认禁用**：`tools.builtin.process.enabled` 必须显式 `true`（进程脱离 turn 生命周期长期运行）
- `process_start`：异步启动，立即返回 `id=proc_N pid=…`；stdout/stderr 独立缓冲（≤`max_output_bytes`）
- `process_stop`：进程树终止——Unix 先进程组 `SIGTERM`，5s 宽限期后 `SIGKILL`；Windows `taskkill /T`（`force=true` 加 `/F`）
- `process_list` / `process_logs`：按 `id` 查询
- 平台差异收敛在 `process_unix.go` / `process_windows.go`

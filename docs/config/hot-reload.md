# 配置热更新

> 文档路径: `docs/config/hot-reload.md`
> 依赖: [loading.md](loading.md)、[validation.md](validation.md)

---

## 1. 契约

热更新/在线改配置的生效分类见 §4：读时配置在快照发布后即生效（结构性子系统由 Runtime 重建交换）；持有持续性状态（数据库文件、监听端口、日志文件句柄、子进程、长连接、索引管线）的字段保持旧 snapshot，并在 `ReloadResult` 中标记 `restart_required`（文件已落盘，重启后生效）。

固定流程：

```text
file event
  -> debounce
  -> serialize reload
  -> config.Load(path, flags)
  -> validate bindings
  -> diff(old, candidate)
  -> classify allowlist/restart-required
  -> atomic.Value.Store(candidate)  (only when no restart path)
```

`config.Load` 已完成解析、迁移、环境变量展开、presence-aware 解码和基础校验；热更新不得重复实现其中任何阶段。`ReloadManager` 是 watcher 与 `config_reload` Tool 共用的唯一发布入口。

## 2. 文件监听

监听配置文件所在目录，以覆盖编辑器的临时文件 + Rename 保存方式。只处理目标路径的 `Write|Create|Rename|Remove`，300ms 防抖；文件暂时不存在时保留旧配置，下一次 Create 重新尝试。

```go
type Watcher struct {
    fs        *fsnotify.Watcher
    path      string
    debounce  time.Duration
    reload    func() (ReloadResult, error)
    onReload  func(ReloadResult)
    onError   func(error)
}

func (w *Watcher) Run(ctx context.Context) error {
    defer w.fs.Close()
    timer := time.NewTimer(time.Hour)
    if !timer.Stop() {
        <-timer.C
    }
    defer timer.Stop()
    var timerC <-chan time.Time

    for {
        select {
        case <-ctx.Done():
            return context.Cause(ctx)
        case err, ok := <-w.fs.Errors:
            if !ok {
                return nil
            }
            if err != nil && w.onError != nil {
                w.onError(fmt.Errorf("config watcher: %w", err))
            }
        case event, ok := <-w.fs.Events:
            if !ok {
                return nil
            }
            if filepath.Clean(event.Name) != w.path ||
                event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) == 0 {
                continue
            }
            if !timer.Stop() && timerC != nil {
                select {
                case <-timer.C:
                default:
                }
            }
            timer.Reset(w.debounce)
            timerC = timer.C
        case <-timerC:
            timerC = nil
            result, err := w.reload()
            if err != nil {
                if w.onError != nil {
                    w.onError(err)
                }
                continue
            }
            if w.onReload != nil {
                w.onReload(result)
            }
        }
    }
}
```

`Run` 对两个 fsnotify channel 都检查 `ok`，统一负责关闭 watcher 和 timer；reload 在同一个 goroutine 内执行，不会在 `Run` 返回后遗留 callback。Watcher 错误由 Runtime supervisor 记录并重建 watcher，当前 snapshot 不清空。

## 3. 原子发布

```go
var ErrConfigNotActive = errors.New("config reload manager not active")

type ReloadResult struct {
    Applied         bool     `json:"applied"`
    Changed         []string `json:"changed"`
    RestartRequired bool     `json:"restart_required"`
    Paths           []string `json:"paths"` // 仅 restart-required paths
}

type ReloadManager struct {
    path             string
    flags            map[string]any
    validateBindings func(*Config) error
    value            atomic.Value // stores *Config; snapshots are immutable
    reload           sync.Mutex   // serializes watcher/tool requests
    active           bool         // protected by reload
}

func NewReloadManager(initial *Config, path string, flags map[string]any, validateBindings func(*Config) error) (*ReloadManager, error)
func (m *ReloadManager) Activate() error
func (m *ReloadManager) Current() *Config
func (m *ReloadManager) Reload() (ReloadResult, error)
```

Runtime 只执行一次 `initial := config.Load(path, flags)`。`NewReloadManager` 拒绝 nil，并立即保存这个已经通过基础校验的不可变 snapshot，因此 bootstrap 组件可以读取 `Current()`，且不会对配置文件做第二次读取。构造后 `path`、`flags` 和 validator 不变，`flags` 的 map 在构造时深拷贝。

Provider/Tool/Plugin/MCP/Skill catalog 使用同一个 `initial` 建立后，Runtime 调用一次 `Activate()`。它在 `reload` mutex 下对当前初始 snapshot 执行 `validateBindings`，成功后设置 `active=true`；失败保持 inactive 并触发 Runtime 逆序 rollback。`Reload()` 在 active 前返回 `ErrConfigNotActive`。Watcher、`config_query`、`config_reload` 和 Remote API 只能在 Activate 成功后启动，因此未完成 binding 的 bootstrap snapshot 不会成为外部可见配置。

`Reload` 的核心语义：

1. 持有 `reload` mutex，要求 `active=true`，读取旧 snapshot。
2. `Load` 候选文件并执行 `validateBindings`。
3. 计算并按字典序排序 `Changed`。
4. 计算 restart-required paths；非空时返回 `Applied=false, RestartRequired=true, Paths=...`，不调用 `Store`，且 `error=nil`。
5. 没有 restart path 时原子 `Store` 候选，返回 `Applied=true, RestartRequired=false`。

Load、绑定校验或内部发布错误返回非 nil error，旧 snapshot 保持不变。调用方不得修改 `Current()` 返回的字段、slice 或 map；需要可变数据的模块必须复制自己的字段。

## 4. 生效分类：读时生效 vs 重启生效

规则：持有持续性状态（数据库文件、监听端口、日志文件句柄、子进程、长连接、
向量索引管线）的路径必须重启；其余读时配置改完即生效（快照交换后下次读取
即新值，结构性子系统由 Runtime 按依赖顺序重建交换，见 `Runtime.applyConfig`）。

| 路径 | 生效时机 |
|------|----------|
| `providers.*`（新增/删除/改模型） | provider 通道原地刷新后下一次模型请求 |
| `agents.*`（新增/删除/改模型等）、根 `planner.*` | agent 绑定重建后下一次 turn |
| `skills.*`（含 `skills.dir` 重扫） | skill 重建后下一次 Skill 解析 |
| `tools.*`（含 builtin 开关/options） | tool 通道原地刷新后下一次 Tool 调用 |
| `runtime.auth.*` | 认证对象重建后下一次请求 |
| `log.level` | 下一条日志（进程共享 LevelVar） |
| `session.*`（含 `cleanup_interval`） | 新建会话即用新策略；cleanup 间隔下个 tick 自适应 |
| `context.*`、`agents[].context.*`、`agents[].session.*` | 下一次 Context 构建 / 新建 Session |
| `memory` 标量策略（`max_items`、`default_ttl`、`eviction_policy`、`expire_interval` 等） | 下一次 Agent turn 或 Remote Memory 请求 |

以下分组持有持续性状态，必须重启（`restart_required`，文件已落盘）：

- `runtime.storage.*`（SQLite 文件）、`runtime.api.*`（监听地址与服务参数）
- `log.output`、`log.format`（文件句柄与 handler 形态）
- `memory.enabled`、`memory.storage.*`、`memory.vector.*`、`memory.embedding.*`（ContentStore 生命周期与索引重建语义）
- `plugins.*`（子进程生命周期）、`mcp.*`（上游连接与本地 Serve）

若一批变更同时包含可热更新和需重启路径，整批不应用。已创建 Session 使用 snapshot 中持久化的 resolved policy；reload 不扫描、不改写现有 Session，也不改变其 TTL、max lifetime 或 persist 语义。Context/Session 的 `validateBindings` 必须在 `Store` 前完成所有 Agent 的有效配置检查。

## 5. 结果与可观测性

文件监听失败只记录结构化日志并保留旧快照。`config_reload` Tool 直接返回 `ReloadResult`；成功或 restart-required 结果只包含路径，不包含旧/新 Secret 值。失败记录 `config.reload_failed`（错误分类、路径和 request ID），不通过 Remote SSE 广播；Remote API 不提供 `/api/v1/config/reload`。

---

*最后更新: 2026-07-22*

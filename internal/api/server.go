package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"runtime/debug"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/v2up-32mb/yaa/internal/auth"
	"github.com/v2up-32mb/yaa/internal/config"
	"github.com/v2up-32mb/yaa/internal/mcp"
	"github.com/v2up-32mb/yaa/internal/provider"
	"github.com/v2up-32mb/yaa/internal/session"
	"github.com/v2up-32mb/yaa/internal/skill"
	"github.com/v2up-32mb/yaa/internal/tool"
	"golang.org/x/exp/slog"
)

// BuildInfo 由 ldflags 注入；缺省值为占位符。
var (
	Version   = "0.0.0-dev"
	GitCommit = "unknown"
	BuildTime = "unknown"
)

// AgentCounts 是 health 响应中的 Agent 运行态快照。
type AgentCounts struct {
	Total   int `json:"total"`
	Running int `json:"running"`
	Paused  int `json:"paused"`
	Stopped int `json:"stopped"`
}

// HealthData 是 /api/v1/health 响应 data 字段。
type HealthData struct {
	Status        string            `json:"status"`
	Ready         bool              `json:"ready"`
	UptimeSeconds int64             `json:"uptime_seconds"`
	Agents        AgentCounts       `json:"agents"`
	Components    map[string]string `json:"components"`
}

// HealthProvider 由 Runtime 注入，返回当前运行态快照。
// 未注入时 Server 视为 Not Ready。
type HealthProvider interface {
	Health() HealthData
}

// Server 是 Remote API 的 HTTP 服务。
type Server struct {
	addr             string
	logger           *slog.Logger
	server           *http.Server
	router           *mux.Router
	registeredRoutes []routeSpec
	health           HealthProvider
	sessions         SessionProvider
	agentExists      AgentExistsProvider
	agents           AgentProvider
	sessionMgr       *session.Manager
	tools            *tool.Manager
	skills           *skill.Manager
	cfgSnapshot      *config.Config
	reloadMgr        *config.ReloadManager
	providers        *provider.Manager
	mcpServers       MCPServerProvider
	memoryProvider   MemoryProvider
	memoryResolver   MemoryPolicyResolver
	// v1 Auth：由 Runtime 在 Start 时经 SetAuth 注入；nil 或 disabled 表示
	// 整体绕过 AuthN/AuthZ（仅 loopback 或已强校验的回环监听场景下）。
	authz       auth.Authorizer
	authn       auth.Authenticator
	authEnabled bool
	publicPaths map[string]bool // 规范化后只读
	started     time.Time
	mu          sync.Mutex
	listener    *http.Server
}

// NewServer 创建 Remote API Server。addr 为监听地址；health 可为 nil（视为未就绪）。
func NewServer(addr string, health HealthProvider, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{
		addr:    addr,
		logger:  logger,
		health:  health,
		started: time.Now(),
	}
	r := mux.NewRouter()
	s.router = r
	s.register(r)
	s.server = &http.Server{
		Addr:    addr,
		Handler: requestIDMiddleware(recoverMiddleware(r)),
	}
	return s
}

// SetSessionProvider 注入 Session 管理器（在 Runtime Start 时注册）。
func (s *Server) SetSessionProvider(sp SessionProvider, ae AgentExistsProvider) {
	s.mu.Lock()
	s.sessions = sp
	s.agentExists = ae
	s.mu.Unlock()
}

// SetAgentProvider 注入 Agent 管理器（在 Runtime Start 时注册）。
func (s *Server) SetAgentProvider(ap AgentProvider) {
	s.mu.Lock()
	s.agents = ap
	s.mu.Unlock()
}

// SetSessionManager 注入 Session 管理器用于 SSE/WS 事件订阅。
// 与 SetSessionProvider 同时注入（二者都来自 *session.Manager）。
func (s *Server) SetSessionManager(sm *session.Manager) {
	s.mu.Lock()
	s.sessionMgr = sm
	s.mu.Unlock()
}

// MCPServerProvider 由 Runtime 注入，提供 MCP server 只读状态投影给 Remote API。
// 与 Provider/Tool 等具体 *Manager 注入不同：MCP 作为新模块通过接口隔离，
// 便于 API 包 handler 测试 mock（详见 docs/mcp/README.md §2 List/Get/Detail 契约）。
// Detail 返回 ServerDetail（嵌入 ServerStatus + Tools []tool.ToolInfo）供 /:name 端点。
type MCPServerProvider interface {
	List() []mcp.ServerStatus
	Get(name string) (mcp.ServerStatus, bool)
	Detail(name string) (mcp.ServerDetail, bool)
}

// SetMemoryProvider 注入 Memory Manager + policy resolver（docs/remote-api/memory.md §1）。
// Runtime 在 Memory Manager 构造完成 + 配置 snapshot 可得时调用。
// nil mp 表示 Memory subsystem 未启用，Memory 8 端点统一 50301。
func (s *Server) SetMemoryProvider(mp MemoryProvider, resolver MemoryPolicyResolver) {
	s.mu.Lock()
	s.memoryProvider = mp
	s.memoryResolver = resolver
	s.mu.Unlock()
}

// SetToolManager 注入 Tool Manager 供 Tool Remote API 使用。
func (s *Server) SetToolManager(tm *tool.Manager) {
	s.mu.Lock()
	s.tools = tm
	s.mu.Unlock()
}

// SetSkillManager 注入 Skill Manager 供 Skill Remote API 使用。
func (s *Server) SetSkillManager(sm *skill.Manager) {
	s.mu.Lock()
	s.skills = sm
	s.mu.Unlock()
}

// SetProviderManager 注入 Provider Manager 供 Provider Remote API 使用。
func (s *Server) SetProviderManager(pm *provider.Manager) {
	s.mu.Lock()
	s.providers = pm
	s.mu.Unlock()
}

// SetConfigSnapshot 注入当前 Config 快照供 GET /api/v1/config 调用 config.RedactedView。
// 热更新未来通过 ReloadManager 替换同 snapshot 实现；v1 单次注入。
func (s *Server) SetConfigSnapshot(cfg *config.Config) {
	s.mu.Lock()
	s.cfgSnapshot = cfg
	s.mu.Unlock()
}

// SetReloadManager 注入 ReloadManager：GET /api/v1/config 优先读 Current()
// （热更新后不陈旧），PUT /api/v1/config 经它落盘 + 重载。nil 表示未启用。
func (s *Server) SetReloadManager(rm *config.ReloadManager) {
	s.mu.Lock()
	s.reloadMgr = rm
	s.mu.Unlock()
}

// SetMCPServerProvider 注入 MCP Manager 给 Remote API mcp/servers 端点使用。
// 未注入时端点返 50301；运维不应在未启用 MCP 子系统时调用只读状态端点。
func (s *Server) SetMCPServerProvider(mp MCPServerProvider) {
	s.mu.Lock()
	s.mcpServers = mp
	s.mu.Unlock()
}

// SetAuth 注入 Remote API AuthN/AuthZ 与 public paths（docs/auth/integration.md §1）。
//
// 当 enabled=false 或 authn/authz=nil 时 registerProtected 全部 bypass。
// publicPaths 必须由 Config Validator 已规范化（顺手转成 map 便于 O(1) 精确匹配）。
func (s *Server) SetAuth(enabled bool, authn auth.Authenticator, authz auth.Authorizer, publicPaths []string) {
	pp := make(map[string]bool, len(publicPaths))
	for _, p := range publicPaths {
		if p != "" {
			pp[p] = true
		}
	}
	s.mu.Lock()
	s.authEnabled = enabled
	s.authn = authn
	s.authz = authz
	s.publicPaths = pp
	s.mu.Unlock()
}

// register 注册全部 v1 路由到 router（37 条 RouteSpec，docs/remote-api/INDEX.md §3）。
// 每条受保护路由统一用 registerProtected 绑定 RouteSpec（docs/auth/integration.md §3
// 唯一 wrapper），不再有第二套 dispatcher 入口。
func (s *Server) register(r *mux.Router) {
	s.registerRoutes(r)
	// gorilla/mux 默认 405 是 text/plain；用 MethodNotAllowedHandler 包成 envelope 40501
	// （docs/auth/integration.md §3：唯一 route wrapper 的错误回 envelope）。
	r.MethodNotAllowedHandler = http.HandlerFunc(s.methodNotAllowed)
	// 未匹配兜底：gorilla/mux NotFoundHandler 返回的响应包成 envelope。
	r.NotFoundHandler = http.HandlerFunc(s.notFound)
	// 默认 WebUI 作为用户入口 (最后注册, 保证 /api/v1/* 优先).
	registerWebUI(r)
}

// RegisteredRoutes 返回 register 时收集的全部 RouteSpec metadata。
// 供路由注册测试与 AuthZ 依赖枚举使用（AD-004：37 路由 metadata 必须可审计）。
func (s *Server) RegisteredRoutes() []routeSpec {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]routeSpec, len(s.registeredRoutes))
	copy(out, s.registeredRoutes)
	return out
}

// methodNotAllowed 是 gorilla/mux 在 path 命中但 method 无匹配时调用的兜底
// （同 Pattern 多 Method 已经在 registerRoutes 里 .Methods() 精细注册）。
// 与 registerProtected 内 method 校验返回完全一致：envelope 40501。
func (s *Server) methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	s.writeError(w, r, http.StatusMethodNotAllowed, 40501, "method not allowed")
}

// Start 同步启动 HTTP 监听并在后台阻塞 serve。
func (s *Server) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.listener != nil {
		s.mu.Unlock()
		return errors.New("api: server already started")
	}
	s.started = time.Now()
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("api: listen %s: %w", s.addr, err)
	}
	s.server.Addr = ln.Addr().String()
	s.listener = s.server
	s.mu.Unlock()
	go func() {
		if err := s.server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Error("api server stopped with error", err)
		}
	}()
	return nil
}

// Shutdown 优雅关闭 HTTP 服务。
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	srv := s.listener
	s.listener = nil
	s.mu.Unlock()
	if srv == nil {
		return nil
	}
	return srv.Shutdown(ctx)
}

// handleHealth 返回 readiness；未就绪返回 503 + 50301。
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	data := s.currentHealth()
	data.Status = normalizeStatus(data.Status, data.Ready)
	if !data.Ready || data.Status == "unhealthy" {
		s.writeError(w, r, http.StatusServiceUnavailable, 50301, "runtime not ready")
		return
	}
	writeOK(w, RequestIDFromContext(r.Context()), http.StatusOK, data)
}

// handleVersion 返回构建信息。
func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeOK(w, RequestIDFromContext(r.Context()), http.StatusOK, map[string]string{
		"version":    Version,
		"git_commit": GitCommit,
		"build_time": BuildTime,
		"go_version": "go1.20.14",
	})
}

// currentHealth 取注入 provider 的快照，否则 Not Ready。
func (s *Server) currentHealth() HealthData {
	if s.health == nil {
		return HealthData{
			Status:        "not_ready",
			Ready:         false,
			UptimeSeconds: int64(time.Since(s.started).Seconds()),
			Agents:        AgentCounts{},
			Components:    map[string]string{},
		}
	}
	d := s.health.Health()
	d.UptimeSeconds = int64(time.Since(s.started).Seconds())
	return d
}

// normalizeStatus 按文档语义规整 status 文本。
func normalizeStatus(status string, ready bool) string {
	switch {
	case !ready:
		return "not_ready"
	case status == "":
		return "healthy"
	default:
		return status
	}
}

// methodGet 包装仅接受 GET 的 handler，其余返回 40513。
func (s *Server) methodGet(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			s.writeError(w, r, http.StatusMethodNotAllowed, 40501, "method not allowed")
			return
		}
		next(w, r)
	}
}

// recoverMiddleware 捕获 panic 并返回 50011。
func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				debug.PrintStack()
				rid := RequestIDFromContext(r.Context())
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.Header().Set("X-Request-ID", rid)
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(Envelope{Code: 50011, Message: "internal error", Data: nil, RequestID: rid})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// notFound 处理未匹配路由，返回 40401。
func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	s.writeError(w, r, http.StatusNotFound, 40401, "resource not found")
}

// Addr 返回实际监听地址；未启动时返回配置的 addr。
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.server != nil && s.server.Addr != "" {
		return s.server.Addr
	}
	return s.addr
}

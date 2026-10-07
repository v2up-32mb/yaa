package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/exp/slog"

	"github.com/v2up-32mb/yaa/internal/agent"
	"github.com/v2up-32mb/yaa/internal/session"
)

// turns 按 turnID 跟踪在途 turn，cancel 路径据此定位 HandleTurn 的 ctx。
const (
	wsReadLimit = 1 << 20 // 1 MiB
	wsWriteWait = 10 * time.Second
	wsPingEvery = 30 * time.Second
	wsReadIdle  = 120 * time.Second
)

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// 文档：握手需 Authorization Header。v1 只校验存在；具体 auth 校验由后续 Auth 中间件接入。
	// Allow all origins within sandbox；用 Header 校验在 upgrade 前。
	CheckOrigin: func(r *http.Request) bool { return true },
}

// wsClientFrame 是客户端发送的应用层 frame：message 或 cancel。
type wsClientFrame struct {
	Type     string         `json:"type"`
	TurnID   string         `json:"turn_id"`
	Content  string         `json:"content"`
	Metadata map[string]any `json:"metadata"`
}

// handleWSStream 实现 GET /api/v1/sessions/:id/stream (WebSocket Upgrade)。
// 握手必须使用 Authorization Header。
// 连接断开取消该连接发起的全部非终态 turn。每个连接最多一个运行中 turn，其余由 Session FIFO 排队。
func (s *Server) handleWSStream(w http.ResponseWriter, r *http.Request, sp SessionProvider, sessionID string) {
	// Auth wrapper 已在 registerProtected 完成 AuthN/AuthZ 并注入 Identity 到 request context
	// （docs/auth/integration.md §5）。这里读回 Identity：
	//   - 启用 Auth 且非 public：无 Identity 表示 wrapper 已拦截但防止 wrapper 漏绑 → 拒绝握手（40101）
	//   - disabled 或 public：identity 为 nil 也允许 anonymous
	identity, ok := s.authIdentityForWebSocket(r)
	_ = identity
	if !ok {
		s.writeError(w, r, http.StatusUnauthorized, 40101, "unauthorized")
		return
	}

	s.mu.Lock()
	ag := s.agents
	sessionMgr := s.sessionMgr
	s.mu.Unlock()
	if ag == nil || sessionMgr == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, 50301, "runtime not ready")
		return
	}

	// 校验 Session 存在（拿到 AgentID 用于发起 turn）。
	sess, err := sp.Get(r.Context(), sessionID)
	if err != nil {
		s.writeSessionError(w, r, err)
		return
	}

	// 取该 Session Hub 订阅，WS 是 SSE 的双向版本：server 推 Hub 帧给 client。
	h, herr := sessionMgr.Hub(sessionID)
	if herr != nil {
		s.writeSessionError(w, r, herr)
		return
	}
	sub := h.Subscribe()

	// Upgrade 在所有前置 503/404 之后：避免误升级失败再写竞态响应。
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade 失败时 gorilla 已写过 http response，无需再写。
		h.Unsubscribe(sub)
		if s.logger != nil {
			s.logger.Error("ws upgrade", err, "session", sessionID)
		}
		return
	}

	// 连接级 ctx：断开或服务端回写完成都会触发 cancel。
	connCtx, cancelConn := context.WithCancel(r.Context())
	defer cancelConn()

	ws := &wsConn{
		conn:       conn,
		wmu:        sync.Mutex{},
		logger:     s.logger,
		agentID:    sess.AgentID,
		agents:     ag,
		sessionMgr: sessionMgr,
		sessionID:  sessionID,
		hub:        h,
		sub:        sub,
		turns:      make(map[string]context.CancelFunc),
	}

	ws.run(connCtx)
}

// wsConn 封装一条 WebSocket 对话连接。
type wsConn struct {
	conn       *websocket.Conn
	wmu        sync.Mutex // gorilla 不支持并发写，所有写互斥
	logger     *slog.Logger
	agentID    string
	agents     AgentProvider
	sessionMgr *session.Manager
	sessionID  string
	hub        *session.Hub
	sub        *session.Subscriber

	turnsMu sync.Mutex
	turns   map[string]context.CancelFunc // turnID -> cancel 本连接发起的 turn
}

// run 在 conn 生命周期内同时跑 writer(reader-driven + hub-pump) 与 reader。
// 在两者任一退出后取消 ctx 并等待对方，然后关闭 conn。
func (w *wsConn) run(parent context.Context) {
	// reader 与 writer goroutine 都共享 parent ctx；任一结束触发 cancel。
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	// 读循环结束后，关闭 conn 并停止 hub 订阅。
	readerDone := make(chan struct{})
	go w.readLoop(ctx, readerDone)

	// 写循环从 hub 订阅者推送 ConversationFrame 到客户端。
	// 当 reader 因客户端断开退出，cancel 让 writer 在下一次 select 时 select<-ctx.Done() 退出。
	writerDone := make(chan struct{})
	go w.writeLoop(ctx, writerDone)

	// 启动定期 ping，避免代理/网关空闲断流；transport 层 ping 不进入 Session 或 SSE。
	pingDone := make(chan struct{})
	go w.pingLoop(ctx, pingDone)

	<-readerDone
	// reader 退出（客户端断开/errframe/error）；关闭连接，并取消本连接发起的非终态 turn。
	w.cancelAllTurns()
	cancel()
	w.conn.Close()
	<-writerDone
	<-pingDone
	w.hub.Unsubscribe(w.sub)
}

// readLoop 读客户端应用层 frame；message → 发起 HandleTurn；cancel → 取消对应 turn。
// ping/pong control frame 交给 gorilla 的 SetPingHandler/SetPongHandler，不需要在此处理。
func (w *wsConn) readLoop(ctx context.Context, done chan<- struct{}) {
	defer close(done)
	_ = w.conn.SetReadDeadline(time.Now().Add(wsReadIdle))
	w.conn.SetPongHandler(func(string) error {
		_ = w.conn.SetReadDeadline(time.Now().Add(wsReadIdle))
		return nil
	})
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		_, data, err := w.conn.ReadMessage()
		if err != nil {
			return
		}
		var f wsClientFrame
		if jerr := json.Unmarshal(data, &f); jerr != nil {
			w.sendError("", "40001", "invalid frame")
			continue
		}
		switch f.Type {
		case "message":
			// 异步执行: readLoop 必须继续读后续 frame, 否则运行中 turn 期间无法收到 cancel.
			go w.handleMessage(ctx, f)
		case "cancel":
			w.handleCancel(f.TurnID)
		default:
			w.sendError(f.TurnID, "40001", "unknown frame type")
		}
	}
}

// handleMessage 接受 message frame，校验后同步发起 HandleTurn（writer goroutine 会将 queued/... 等 frame 透传到 WS）。
func (w *wsConn) handleMessage(parent context.Context, f wsClientFrame) {
	if f.TurnID == "" {
		w.sendError("", "40001", "turn_id required")
		return
	}
	if f.Content == "" {
		w.sendError(f.TurnID, "40001", "content required")
		return
	}
	if !isValidTurnID(f.TurnID) {
		w.sendError(f.TurnID, "40001", "invalid turn_id")
		return
	}

	// turnID 重复检查与注册必须在同一临界区, 避免并发同 ID frame 互相覆盖 cancel.
	w.turnsMu.Lock()
	if _, dup := w.turns[f.TurnID]; dup {
		w.turnsMu.Unlock()
		w.sendError(f.TurnID, "40001", "turn id already used")
		return
	}
	turnCtx, cancel := context.WithCancel(parent)
	w.turns[f.TurnID] = cancel
	w.turnsMu.Unlock()
	defer func() {
		cancel()
		w.turnsMu.Lock()
		delete(w.turns, f.TurnID)
		w.turnsMu.Unlock()
	}()

	req := agent.TurnRequest{
		SessionID: w.sessionID,
		TurnID:    f.TurnID,
		Content:   f.Content,
		Metadata:  f.Metadata,
		Stream:    true,
		Emit: func(e agent.TurnEvent) {
			w.hub.Publish(turnEventToFrame(e, f.TurnID))
		},
	}
	_, err := w.agents.HandleTurn(turnCtx, w.agentID, req)
	if err != nil {
		// 若是被 cancel 帧/连接断开触发，cause 是 context.Canceled -> "canceled" frame。
		w.sendErrorFromTurn(f.TurnID, err)
	}
}

// handleCancel 取消匹配的 queued/running turn。
func (w *wsConn) handleCancel(turnID string) {
	w.turnsMu.Lock()
	cancel, ok := w.turns[turnID]
	w.turnsMu.Unlock()
	if !ok {
		w.sendError(turnID, "40001", "turn not active")
		return
	}
	cancel()
	// Session 侧 cancel 透出 context.Canceled 为 cause；HandleTurn 返回 context.Canceled。
	// error frame code "canceled" 由 reader 中的 dispatcher.path 负责发送。
	// 这里主动同步 cancel 后立刻退出本调；否管 client 可能重发同一个 turn_id。
}

// writeLoop 把 Hub 上 ConversationFrame 写到 WS；直到 ctx 取消或连接关闭。
func (w *wsConn) writeLoop(ctx context.Context, done chan<- struct{}) {
	defer close(done)
	events := w.sub.Events()
	for {
		select {
		case ev, open := <-events:
			if !open {
				return
			}
			var frame ConversationFrame
			switch e := ev.(type) {
			case ConversationFrame:
				frame = e
			case *session.SessionEndEvent:
				frame = sessionEndToFrame(e)
			default:
				continue
			}
			w.writeFrame(frame)
			if frame.Type == "session_end" {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// pingLoop 周期性发送 ws ping（transport 层，不是应用 ConversationFrame）。
func (w *wsConn) pingLoop(ctx context.Context, done chan<- struct{}) {
	defer close(done)
	t := time.NewTicker(wsPingEvery)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			w.wmu.Lock()
			_ = w.conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			err := w.conn.WriteMessage(websocket.PingMessage, nil)
			w.wmu.Unlock()
			if err != nil {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// writeFrame 加锁写 JSON ConversationFrame；写失败认定连接终止。
func (w *wsConn) writeFrame(f ConversationFrame) {
	w.wmu.Lock()
	defer w.wmu.Unlock()
	_ = w.conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
	if err := w.conn.WriteJSON(f); err != nil {
		// 写错误仅记录，由 reader 收到断开自然退出；避免日志噪声。
		if w.logger != nil && !errors.Is(err, websocket.ErrCloseSent) {
			w.logger.Error("ws write frame", err)
		}
	}
}

// sendError 直接构造并写一个 ConversationFrame error，绕过 Hub；用于输入校验失败或 turn 早期失败。
func (w *wsConn) sendError(turnID, code, msg string) {
	w.writeFrame(ConversationFrame{Type: "error", TurnID: turnID, Code: code, Message: msg})
}

// sendErrorFromTurn 复用 REST 错误映射把 turn 失败转成 error frame code/message。
func (w *wsConn) sendErrorFromTurn(turnID string, err error) {
	frame := errorFrameFromTurnError(err, turnID)
	w.writeFrame(frame)
}

// cancelAllTurns 取消本连接发起的所有非终态 turn。
func (w *wsConn) cancelAllTurns() {
	w.turnsMu.Lock()
	cs := make([]context.CancelFunc, 0, len(w.turns))
	for _, c := range w.turns {
		cs = append(cs, c)
	}
	w.turns = make(map[string]context.CancelFunc)
	w.turnsMu.Unlock()
	for _, c := range cs {
		c()
	}
}

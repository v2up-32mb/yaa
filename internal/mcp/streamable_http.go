package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/exp/slog"
)

// Streamable HTTP transport 常量 (docs/mcp/transport.md §3.3 / checklist §6).
const (
	streamableMessageMaxBytes  = 4 * 1024 * 1024 // 单条 JSON-RPC body 上限 (与 stdio/SSE 一致 docs §2)
	streamableRespBodyCap      = 16 * 1024       // 错误 body 最多有界丢弃 16 KiB (docs §3.3)
	streamableRecvChanCapacity = 256             // Post → Recv channel cap; 单次 POST 含 request 时响应立刻投递
)

// StreamableHTTPClient 是 MCP 2025-03-26 Streamable HTTP 传输实现 (docs/mcp/transport.md §3.3).
// wire 上 Client 类型 "streamable_http"; 兼容 MCP 2025-03-26 协议版本.
// 每 JSON-RPC 走一次 HTTP POST; 接 MCP-Session-Id header (initialize 响应可能带);
// 收到后后续 POST 必带. v1 不发 GET (Server-to-Client SSE 流) / DELETE 终止 (简化状态为
// stateless + session header 复用; docs 明示 GET 405 仅关 SSE 流 POST 仍可用, 与 stateless Client 一致).
type StreamableHTTPClient struct {
	url     string
	headers map[string]string
	client  *http.Client
	logger  *slog.Logger

	mu        sync.Mutex
	started   bool
	closed    bool
	sessionID string // Mcp-Session-Id (server 在 init 响应返回; 空表示 stateless)
	info      TransportInfo
	recvReady chan struct{} // Start 完成 close; Recv 在此阻塞
	closeOnce sync.Once
	recvCh    chan recvItem // Post 投递的响应或事件

	// Server-to-Client SSE 流字段. 仅当 initialize 拿到 Mcp-Session-Id 后启动一次 GET 试探.
	// 200 + text/event-stream → 启动 SSE recvLoop goroutine 投递 recvCh; 405/其他 → graceful 关掉, 不影响 POST 模式.
	sseStarted  uint32          // atomic: 0 未尝试 GET, 1 已尝试 (不管成功与否)
	sseCtx      context.Context // 由 openServerToClientStream 创建, 与 Start 同生命周期 (Close cancel)
	sseCancel   context.CancelFunc
	sseLoopDone chan struct{} // SSE recvLoop 退出信号; nil 表示未启动
}

// recvItem 是 Send 后投递到 recvCh 的中间结构.
// msg != nil 表示成功帧; err != nil 表示 transport 应 fail 的 error.
type recvItem struct {
	msg *Message
	err error
}

// NewStreamableHTTPClient 构造未启动的 StreamableHTTPClient. headers 注入到 POST (Authorization 等).
// httpClient 可为 nil → 默认 &http.Client{} (无超时, 调用方应在 ctx 控制 deadline).
// logger nil → slog.Default().
func NewStreamableHTTPClient(rawurl string, httpClient *http.Client, headers map[string]string, logger *slog.Logger) *StreamableHTTPClient {
	if logger == nil {
		logger = slog.Default()
	}
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	// CheckRedirect 拒绝所有 3xx (docs §3.3: 防 endpoint/auth 被带到其他地址).
	httpClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &StreamableHTTPClient{
		url:       rawurl,
		headers:   headers,
		client:    httpClient,
		logger:    logger,
		info:      TransportInfo{Type: "streamable_http", Endpoint: rawurl, Connected: false},
		recvReady: make(chan struct{}),
		recvCh:    make(chan recvItem, streamableRecvChanCapacity),
	}
}

// Start 启动 transport: URL academy性 + 接收流就绪. Streamable HTTP 无持久连接,
// Start 仅占位状态 + 关闭 recvReady 让 recvLoop 进入. 真正握手在第一次 POST (Initialize) 时发生.
func (c *StreamableHTTPClient) Start(startupCtx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		return fmt.Errorf("%w: streamable_http already started", ErrMCPProtocolError)
	}
	if c.closed {
		return fmt.Errorf("%w: streamable_http closed", ErrMCPTransportClosed)
	}
	c.started = true
	c.info.Connected = true
	close(c.recvReady)
	return nil
}

// Send POST 一条 JSON-RPC Message. docs §3.3:
//   - 每条 JSON-RPC 单独一个 POST body (数组/批量 → HTTP 400 + -32600).
//   - Accept: application/json, text/event-stream; Content-Type: application/json.
//   - 含 request 的 POST 返回单个 JSON 响应或 text/event-stream; 通知 POST 成功返 202 空 body.
//   - 收到 Mcp-Session-Id 后续 POST 必带.
//   - 不自动重试 / 不重发已发的 tools/call.
//
// 注: 响应通过 recvCh 投递给 Recv (保持 Recv/Send 分离模型与 SSE/stdio 一致).
func (c *StreamableHTTPClient) Send(ctx context.Context, msg *Message) error {
	c.mu.Lock()
	if c.closed || !c.started {
		c.mu.Unlock()
		return fmt.Errorf("%w: streamable_http not started or closed", ErrMCPTransportClosed)
	}
	sessionID := c.sessionID
	c.mu.Unlock()

	// 先检查 caller ctx (docs §3.3 先检查 caller context; 已结束 → context.Cause(ctx)).
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}

	raw, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("%w: marshal: %v", ErrMCPProtocolError, err)
	}
	if len(raw) > streamableMessageMaxBytes {
		return fmt.Errorf("%w: send body too long %d", ErrMCPProtocolError, len(raw))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("%w: build post: %v", ErrMCPProtocolError, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		// 启动 dial 拒绝 / TLS / DNS → conn refused; 流写出时网络中断 → transport write
		if isConnRefusedErr(err) {
			return fmt.Errorf("%w: %v", ErrMCPConnRefused, err)
		}
		return fmt.Errorf("%w: %v", ErrMCPTransportWrite, err)
	}
	defer func() { _ = resp.Body.Close() }()

	// HTTP 状态映射 (docs §3.3 错误表).
	if err := c.mapStatusError(resp, msg); err != nil {
		// 错误路径: 非 2xx 已映射. 通过 recvCh 投递给 recvLoop (让 Client fail 一致, 不双线返错).
		c.recvCh <- recvItem{err: err}
		return nil
	}

	// 2xx 路径: body 可能是 application/json (单响应) / text/event-stream (多帧) / 空 (202 notification).
	ctype := resp.Header.Get("Content-Type")
	// 提取 Mcp-Session-Id 首次响应返回则保存.
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		c.mu.Lock()
		first := c.sessionID == ""
		c.sessionID = sid
		c.mu.Unlock()
		if first {
			// Async 试探 GET 打开 Server-to-Client SSE 流 (docs §3.3 "可选 GET"; 405 → graceful);
			// 不阻塞 Send; 失败不影响 POST 模式.
			go c.tryOpenServerToClientStream(context.Background())
		}
	}

	// 检查是否请求类 (request Id + method) 或 notification; 通知无 body 直接成功.
	isNotification := msg.ID == nil || len(msg.ID) == 0 || isNullJSON(msg.ID)
	if isNotification {
		// docs §3.3: 通知 POST 成功返 202 空 body; 不投递 recv.
		return nil
	}

	// 响应 body 限界读取 (避免 server 发大 body 内存炸).
	body := io.LimitReader(resp.Body, streamableMessageMaxBytes+1)
	if strings.Contains(ctype, "text/event-stream") {
		// SSE frame 解析多条 message event; 每条投递 recvCh.
		if err := c.drainSSEResponse(body); err != nil {
			c.recvCh <- recvItem{err: err}
			return nil
		}
		return nil
	}
	// 默认 application/json (含其他 ctype 也按 JSON 尝试; docs "错误 Content-Type → ProtocolError" 在错误路径已处理,
	// 这里 2xx + 非 SSE 视为 JSON).
	rawBody, err := io.ReadAll(body)
	if err != nil {
		c.recvCh <- recvItem{err: fmt.Errorf("%w: read post body: %v", ErrMCPTransportWrite, err)}
		return nil
	}
	if len(rawBody) > streamableMessageMaxBytes {
		c.recvCh <- recvItem{err: fmt.Errorf("%w: post body too long", ErrMCPProtocolError)}
		return nil
	}
	if len(rawBody) == 0 {
		// 含 request 的 POST 不应返空 body; 状态码已 2xx 说明是误返 → 协议错.
		c.recvCh <- recvItem{err: fmt.Errorf("%w: empty response body for request", ErrMCPProtocolError)}
		return nil
	}
	// 校验是否单对象 (数组/批量 → -32600 + HTTP 400 已在 mapStatusError 处理); 这里保险再 check:
	trimmed := bytes.TrimSpace(rawBody)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		c.recvCh <- recvItem{err: fmt.Errorf("%w: batch array response rejected", ErrMCPProtocolError)}
		return nil
	}
	var m Message
	if err := json.Unmarshal(rawBody, &m); err != nil {
		c.recvCh <- recvItem{err: fmt.Errorf("%w: decode post body: %v", ErrMCPProtocolError, err)}
		return nil
	}
	c.recvCh <- recvItem{msg: &m}
	return nil
}

// drainSSEResponse 把 SSE 响应 body 内每条 message event 反序列化 Message 投递 recvCh.
// 复用 readSSEFrame (sse.go) parser. 非 message event 跳过; EOF 视为结束.
func (c *StreamableHTTPClient) drainSSEResponse(body io.Reader) error {
	reader := bufio.NewReaderSize(body, streamableMessageMaxBytes+4096)
	for {
		frame, err := readSSEFrame(reader)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: sse response frame: %v", ErrMCPProtocolError, err)
		}
		if frame.event != "" && frame.event != "message" {
			continue
		}
		if len(frame.data) == 0 {
			continue
		}
		var m Message
		if err := json.Unmarshal(frame.data, &m); err != nil {
			return fmt.Errorf("%w: decode sse frame: %v", ErrMCPProtocolError, err)
		}
		c.recvCh <- recvItem{msg: &m}
	}
}

// mapStatusError 把非 2xx HTTP 响应按 docs §3.3 错误表映射到 mcp sentinel.
// 入参 msg 用于判断当前 POST 阶段 (init / 业务 / 通知). 已分配 session 用 e.sessionID != "" 检查.
// 2xx 返 nil 表示成功 (调用方继续解析 body).
func (c *StreamableHTTPClient) mapStatusError(resp *http.Response, msg *Message) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	// 错误 body 最多有界丢弃 16 KiB 不进入稳定 Error/log (docs §3.3).
	_, _ = io.ReadAll(io.LimitReader(resp.Body, streamableRespBodyCap))

	status := resp.StatusCode
	c.mu.Lock()
	hasSession := c.sessionID != ""
	c.mu.Unlock()
	isInit := msg != nil && msg.Method == "initialize"

	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return fmt.Errorf("%w: http %d", ErrMCPAuthFailed, status)
	case isInit && (status == http.StatusNotFound || status == http.StatusMethodNotAllowed):
		return fmt.Errorf("%w: initialize http %d", ErrMCPConfig, status)
	case isInit && (status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout):
		return fmt.Errorf("%w: initialize http %d", ErrMCPConnTimeout, status)
	case isInit && (status == http.StatusTooManyRequests || status >= 500):
		return fmt.Errorf("%w: initialize http %d", ErrMCPUnavailable, status)
	case hasSession && (status == http.StatusBadRequest || status == http.StatusNotFound || status == 410):
		return fmt.Errorf("%w: post http %d after session", ErrMCPTransportClosed, status)
	case !isInit && resp.Request != nil && resp.Request.Method == http.MethodPost && (status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500):
		return fmt.Errorf("%w: post http %d business", ErrMCPTransportWrite, status)
	case status == http.StatusRequestEntityTooLarge || status >= 300 && status < 400:
		return fmt.Errorf("%w: http %d", ErrMCPProtocolError, status)
	case isErrContentType(resp):
		return fmt.Errorf("%w: bad content-type %q", ErrMCPProtocolError, resp.Header.Get("Content-Type"))
	default:
		return fmt.Errorf("%w: http %d", ErrMCPProtocolError, status)
	}
}

// isErrContentType 检查响应对应期望的不合法 Content-Type (用于错误表 "错误 Content-Type → ProtocolError").
// 2xx 错 contentType (既非 application/json 也非 text/event-stream 且 body 非空) 视作协议错.
// ponytail: 仅状态码已 2xx 时调用 (否则跳过 ctype 判断).
func isErrContentType(resp *http.Response) bool {
	ctype := resp.Header.Get("Content-Type")
	if ctype == "" {
		return false
	}
	return !strings.Contains(ctype, "application/json") && !strings.Contains(ctype, "text/event-stream")
}

// Recv 阻塞读下一条 Message (来自 Send 投递的 recvCh). 每个 transport 实例唯一 dispatcher goroutine 调用.
// ctx 取消时优先返 ctx.Err() (但已 in-flight 的 Send 投递响应不影响; Recv 不读网络).
func (c *StreamableHTTPClient) Recv(ctx context.Context) (*Message, error) {
	select {
	case <-c.recvReady:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	for {
		select {
		case item := <-c.recvCh:
			if item.err != nil {
				return nil, item.err
			}
			return item.msg, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// Close 幂等关闭. 拿到 session ID 后 (stateful client):
//   - cancel SSE recvLoop + 等 sseLoopDone 退出;
//   - 发一次 DELETE 终止 session (docs §3.3 DELETE 成功 200/204, 404/405 幂等忽略);
//   - 失败或无 session (stateless mode) 不返错.
//
// DELETE 由独立的短超时 ctx 控制; 不阻塞 Close 主路径过久.
func (c *StreamableHTTPClient) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		sid := c.sessionID
		sseCancel := c.sseCancel
		sseLoopDone := c.sseLoopDone
		c.info.Connected = false
		c.mu.Unlock()
		if sseCancel != nil {
			sseCancel()
		}
		if sseLoopDone != nil {
			<-sseLoopDone
		}
		if sid != "" {
			c.sendDelete(context.Background(), sid)
		}
	})
	return nil
}

// tryOpenServerToClientStream 试探性发送一次 GET 打开可选 Server-to-Client SSE 流 (docs §3.3 "可选 GET").
// 仅在 initialize 拿到 Mcp-Session-Id 后调用一次 (atomic CAS 保证); 200 + text/event-stream → 启动 SSE recvLoop
// goroutine 读取 server push 帧投递 recvCh; 405 → graceful 关闭, 不影响 POST 模式.
func (c *StreamableHTTPClient) tryOpenServerToClientStream(parent context.Context) {
	if !atomic.CompareAndSwapUint32(&c.sseStarted, 0, 1) {
		return // 已 tried.
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	sid := c.sessionID
	if sid == "" {
		c.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	c.sseCtx = ctx
	c.sseCancel = cancel
	c.sseLoopDone = make(chan struct{})
	url := c.url
	headers := c.headers
	client := c.client
	logger := c.logger
	c.mu.Unlock()

	go c.runSSERecvLoop(ctx, url, sid, headers, client, logger)
}

// runSSERecvLoop 发 GET 请求打开 SSE 流并持续 readSSEFrame → 投递 recvCh;
// 405/非 event-stream → graceful 退出 (serverPushActive=false, POST 模式继续); ctx 取消或连接中断则退出.
func (c *StreamableHTTPClient) runSSERecvLoop(ctx context.Context, url, sid string, headers map[string]string, client *http.Client, logger *slog.Logger) {
	defer close(c.sseLoopDone)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		logger.Info("mcp streamable_http sse open build req", "err", err)
		return
	}
	req.Header.Set("Accept", "text/event-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Mcp-Session-Id", sid)
	resp, err := client.Do(req)
	if err != nil {
		// ctx 取消或连接断; 不投错到 recvCh (Close 流程触发).
		if ctx.Err() == nil {
			logger.Info("mcp streamable_http sse open dial", "err", err)
		}
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		// 405 / 其他: docs §3.3 "可选 GET 405 - 只关闭 SSE 不影响 POST"; 不报错.
		return
	}
	ctype := resp.Header.Get("Content-Type")
	if !strings.Contains(ctype, "text/event-stream") {
		// 非 SSE: 也 graceful 退出.
		return
	}
	reader := bufio.NewReaderSize(resp.Body, streamableMessageMaxBytes+4096)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		frame, ferr := readSSEFrame(reader)
		if ferr != nil {
			// EOF / 错: server 关流或读流断; graceful 不报 ErrMCPProtocolError (与 SSEClient 一致易处理).
			return
		}
		if frame.event != "" && frame.event != "message" {
			continue
		}
		if len(frame.data) == 0 {
			continue
		}
		var m Message
		if jerr := json.Unmarshal(frame.data, &m); jerr != nil {
			continue
		}
		select {
		case c.recvCh <- recvItem{msg: &m}:
		case <-ctx.Done():
			return
		}
	}
}

// sendDelete 发一次 DELETE 终止 session. docs §3.3:
//   - 成功: 200 / 204 (Client 不区分; 都是干净退出);
//   - 404 / 405: 幂等忽略 (Close 的 DELETE 404/405 幂等忽略 docs §3.3 错误表最后一行);
//   - 其他非 2xx/超时: 仅日志, 不返错 (Close 不应因为 DELETE 失败 panic).
func (c *StreamableHTTPClient) sendDelete(parent context.Context, sid string) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.url, nil)
	if err != nil {
		return
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Mcp-Session-Id", sid)
	resp, err := c.client.Do(req)
	if err != nil {
		return
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, streamableRespBodyCap))
}

// Info 返当前 transport 元数据.
func (c *StreamableHTTPClient) Info() TransportInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.info
}

// ensure errors import used
var _ = errors.Is

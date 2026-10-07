package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"golang.org/x/exp/slog"
)

// SSE transport 常量 (docs/mcp/transport.md §3.2 / checklist §5).
const (
	sseMessageMaxBytes = 4 * 1024 * 1024             // 单条 JSON-RPC body 上限 4 MiB (与 stdio 一致 docs §2)
	sseFrameMaxBytes   = sseMessageMaxBytes + 4*1024 // SSE frame 含 event:/id:/data: 字段头开销上限
)

// SSEClient 是 legacy MCP SSE 传输实现 (docs/mcp/transport.md §3.2).
// wire 上 Client 类型为 "sse" (config transport="sse"); 兼容 MCP 2024-11-05 协议版本.
// 接 GET 事件流 + POST 消息, 首帧必须是 event:endpoint data:<post-path>.
type SSEClient struct {
	url     string
	headers map[string]string
	tls     *tlsConfig // docs §5 tls.ca_file; v1 仅记录, 真实 tls.Config 由外部 http.Client 提供
	client  *http.Client
	logger  *slog.Logger

	mu        sync.Mutex
	started   bool
	closed    bool
	resp      *http.Response
	body      io.ReadCloser // == resp.Body; 关流时单独引用方便 reader 抽出
	reader    *bufio.Reader // 跨 Recv 复用; SSE frame 解析的累计状态在 reader 之上
	endpoint  string        // 解析后的 POST endpoint (绝对 URL, 已校验同 host)
	lastID    string        // Last-Event-ID (用于未来重连续传, v1 仅记录不续传)
	info      TransportInfo
	recvReady chan struct{} // Start 完成关闭; Recv 在此阻塞直到 Start ok
	closeOnce sync.Once
	closeErr  error
	// procCtx 是 Start 持有的请求级 ctx; Close cancel 它强制断流
	procCtx    context.Context
	procCancel context.CancelFunc
}

// tlsConfig 是 SSE/HTTP transport 配置快照; v1 仅 ca_file 路径占位, 真实 TLS 由 caller 在 http.Client 配置.
// ponytail: 不引入 insecure_skip_verify (docs §5 明示不提供).
type tlsConfig = struct {
	caFile string
}

// NewSSEClient 构造未连接的 SSEClient. headers 注入到 GET 与 POST (Authorization 等).
// httpClient 可为 nil → 用默认 &http.Client{} (无超时). 若调用方希望 connect 超时控制,
// 应在 httpClient 中外层 ctx 取消驱动调用方法 (Start ctx). logger nil → slog.Default().
func NewSSEClient(rawurl string, httpClient *http.Client, headers map[string]string, logger *slog.Logger) *SSEClient {
	if logger == nil {
		logger = slog.Default()
	}
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &SSEClient{
		url:       rawurl,
		headers:   headers,
		client:    httpClient,
		logger:    logger,
		info:      TransportInfo{Type: "sse", Endpoint: rawurl, Connected: false},
		recvReady: make(chan struct{}),
	}
}

// Start 发起 GET 事件流并解析首帧 endpoint. docs §3.2: 首个事件必须是
//
//	event: endpoint
//	data: /message?session_id=...
//
// data 是相对路径, 用 url.ResolveReference 解析 (拒绝跨 host/scheme).
// 拨号超时由 startupCtx 控制 (通过 ctx.Done() + http.Client transport RTT 协同).
func (c *SSEClient) Start(startupCtx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		return fmt.Errorf("%w: sse already started", ErrMCPProtocolError)
	}
	if c.closed {
		return fmt.Errorf("%w: sse closed", ErrMCPTransportClosed)
	}

	// procCtx 是 GET 流的生命周期; Close 取消它强制断流. 与 startupCtx.cascade:
	// 启动阶段 startupCtx 取消也应终止 procCtx. 用 MergeContext 把 startupCtx 取消传播进 procCtx.
	procCtx, procCancel := context.WithCancel(context.Background())
	c.procCtx = procCtx
	c.procCancel = procCancel
	// 启动期间若 startupCtx 取消, 同步取消 procCtx
	go func() {
		select {
		case <-startupCtx.Done():
			procCancel()
		case <-procCtx.Done():
		}
	}()

	req, err := http.NewRequestWithContext(procCtx, http.MethodGet, c.url, nil)
	if err != nil {
		procCancel()
		return fmt.Errorf("%w: invalid sse url: %v", ErrMCPConfig, err)
	}
	req.Header.Set("Accept", "text/event-stream")
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}

	// 启动通过 startupCtx 控制; httpClient 用其 transport 内部超时与 procCtx cancel.
	// 启动超时 (~dial timeout) 会通过 procCtx cancel 传播到 req context.
	resp, err := c.client.Do(req)
	if err != nil {
		procCancel()
		if startupCtx.Err() != nil {
			return fmt.Errorf("%w: %v", ErrMCPConnTimeout, startupCtx.Err())
		}
		// 连接拒绝 / DNS / TLS 等区分简单二分
		if isConnRefusedErr(err) {
			return fmt.Errorf("%w: %v", ErrMCPConnRefused, err)
		}
		return fmt.Errorf("%w: %v", ErrMCPTransportClosed, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_ = resp.Body.Close()
		procCancel()
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return fmt.Errorf("%w: http %d", ErrMCPAuthFailed, resp.StatusCode)
		default:
			return fmt.Errorf("%w: http status %d", ErrMCPConfig, resp.StatusCode)
		}
	}
	ctype := resp.Header.Get("Content-Type")
	if !strings.Contains(ctype, "text/event-stream") {
		_ = resp.Body.Close()
		procCancel()
		return fmt.Errorf("%w: expected text/event-stream got %q", ErrMCPProtocolError, ctype)
	}

	c.resp = resp
	c.body = resp.Body
	c.reader = bufio.NewReaderSize(resp.Body, sseFrameMaxBytes)
	c.started = true

	// 解析首帧 endpoint. 在 mu 锁外解析避免阻塞 Send 路径; 但本 commit 单线程顺序启动,
	// 锁内同步解析也安全 (其他 goroutine 还未拿到 c). 用同步 + 锁内解析, 失败回滚.
	if err := c.parseFirstFrameLocked(); err != nil {
		// 回滚已建立的流
		c.started = false
		_ = c.body.Close()
		c.resp = nil
		c.body = nil
		c.reader = nil
		procCancel()
		return err
	}

	c.info.Connected = true
	close(c.recvReady)
	return nil
}

// parseFirstFrameLocked 在 Start 内已持锁时同步解析首个 SSE frame 拿到 endpoint.
// docs §3.2: 首帧 event:endpoint data:<relative path>; 用 ResolveReference 解析.
// 跨 host/scheme 拒绝.
func (c *SSEClient) parseFirstFrameLocked() error {
	frame, err := readSSEFrame(c.reader)
	if err != nil {
		return fmt.Errorf("%w: read endpoint frame: %v", ErrMCPProtocolError, err)
	}
	if frame.event != "endpoint" {
		return fmt.Errorf("%w: first frame event=%q want endpoint", ErrMCPProtocolError, frame.event)
	}
	if len(frame.data) == 0 {
		return fmt.Errorf("%w: endpoint frame missing data", ErrMCPProtocolError)
	}
	data := strings.TrimRight(string(frame.data), "\n")
	base, err := url.Parse(c.url)
	if err != nil {
		return fmt.Errorf("%w: base url parse: %v", ErrMCPProtocolError, err)
	}
	ref, err := url.Parse(data)
	if err != nil {
		return fmt.Errorf("%w: endpoint data parse: %v", ErrMCPProtocolError, err)
	}
	resolved := base.ResolveReference(ref)
	if resolved.Host != base.Host {
		return fmt.Errorf("%w: endpoint cross host: %q vs %q", ErrMCPProtocolError, resolved.Host, base.Host)
	}
	if resolved.Scheme != base.Scheme {
		return fmt.Errorf("%w: endpoint cross scheme: %q vs %q", ErrMCPProtocolError, resolved.Scheme, base.Scheme)
	}
	c.endpoint = resolved.String()
	return nil
}

// Send POST 一条 JSON-RPC Message 到 SSE endpoint. docs §3.2: POST body 的响应
// 通过 GET 事件流推回, 不在 POST 的 response body 中处理.
// POST 返回非 2xx 视为 send 失败关闭连接.
func (c *SSEClient) Send(ctx context.Context, msg *Message) error {
	c.mu.Lock()
	if c.closed || !c.started {
		c.mu.Unlock()
		return fmt.Errorf("%w: sse not started or closed", ErrMCPTransportClosed)
	}
	endpoint := c.endpoint
	c.mu.Unlock()
	if endpoint == "" {
		return fmt.Errorf("%w: no endpoint", ErrMCPTransportClosed)
	}

	raw, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("%w: marshal: %v", ErrMCPProtocolError, err)
	}
	if len(raw) > sseMessageMaxBytes {
		return fmt.Errorf("%w: send body too long %d", ErrMCPProtocolError, len(raw))
	}

	// POST 用 ctx (caller 可控制 deadline); 走独立 http client (procCtx 不用, 因 POST 独立请求).
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("%w: build post: %v", ErrMCPProtocolError, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("%w: post: %v", ErrMCPTransportWrite, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return fmt.Errorf("%w: http %d", ErrMCPAuthFailed, resp.StatusCode)
		default:
			return fmt.Errorf("%w: post http status %d", ErrMCPTransportWrite, resp.StatusCode)
		}
	}
	// POST body 可能是空 (202 Accepted) 或 application/json 同步结果. docs §3.2 简化:
	// 忽略 POST body; 真实结果通过 Recv (GET event stream) 投递.
	return nil
}

// Recv 阻塞读下一条 message 事件 (event == "" 或 "message"); data 是 JSON-RPC.
// 其他 event 类型 (endpoint, 自定义) 忽略掉回到循环读下一条; comment 行忽略.
// docs §3.2: parser 必须支持多行 data:, 空行结束 frame, heartbeat comment (":").
func (c *SSEClient) Recv(ctx context.Context) (*Message, error) {
	select {
	case <-c.recvReady:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	for {
		// ctx 取消: 与 frame read 并存. reader.ReadBytes 会阻塞直到下个分隔符; ctx 取消无法打断
		// 已开始的 read, 但 GET 流上的 procCtx cancel 会关流触发 reader EOF. ctx 单独关心优先级.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		frame, err := readSSEFrame(c.reader)
		if err != nil {
			// 流关闭
			c.mu.Lock()
			closed := c.closed
			c.mu.Unlock()
			if closed {
				return nil, fmt.Errorf("%w: stream closed", ErrMCPTransportClosed)
			}
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			return nil, fmt.Errorf("%w: read frame: %v", ErrMCPTransportClosed, err)
		}
		// 仅 message 事件 是 JSON-RPC; endpoint / 其他 event / 无 event 默认 message 也并入.
		if frame.event != "" && frame.event != "message" {
			continue
		}
		if len(frame.data) == 0 {
			// 空帧 (heartbeat); 跳过.
			continue
		}
		// 更新 Last-Event-ID (仅记录, v1 不续传).
		if frame.id != "" {
			c.mu.Lock()
			c.lastID = frame.id
			c.mu.Unlock()
		}
		var msg Message
		if err := json.Unmarshal(frame.data, &msg); err != nil {
			return nil, fmt.Errorf("%w: decode: %v", ErrMCPProtocolError, err)
		}
		return &msg, nil
	}
}

// Close 幂等关闭: 取消 GET 流 procCtx 强制 reader EOF + 关 resp.Body.
func (c *SSEClient) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		if c.procCancel != nil {
			c.procCancel()
		}
		body := c.body
		c.mu.Unlock()
		if body != nil {
			_ = body.Close()
		}
		c.mu.Lock()
		c.info.Connected = false
		c.mu.Unlock()
	})
	return nil
}

// Info 返当前 transport 元数据.
func (c *SSEClient) Info() TransportInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.info
}

// ── SSE frame 解析 ──────────────────────────────────────────────────────────

// sseFrame 是单条 SSE event 解析后的中间结构.
type sseFrame struct {
	event string // event: 行的值; 空表示默认 message (SSE)
	id    string // id: 行的值
	data  []byte // data: 行累积 (多行以 \n 连接, 末尾 \n 去掉)
}

// readSSEFrame 读取单条 SSE frame 直到空行结束.
// 支持: comment line (以 ':' 开头) 忽略; field: value 多行 data; 空行结束 frame.
// docs §3.2 / W3C SSE spec. bufio 上限 sseFrameMaxBytes 防 OOM.
// 注意: 仅返 frame, 由调用方决定是否作为 message 投递.
func readSSEFrame(reader *bufio.Reader) (sseFrame, error) {
	var (
		f     sseFrame
		datas []string
	)
	for {
		line, err := reader.ReadString('\n')
		if err == io.EOF && len(line) == 0 {
			// 流关闭且缓冲空
			return sseFrame{}, io.EOF
		}
		if err != nil && err != io.EOF {
			// bufio.ErrBufferFull → frame 过长
			if err == bufio.ErrBufferFull {
				return sseFrame{}, fmt.Errorf("frame too long")
			}
			return sseFrame{}, err
		}
		// 去掉末尾 \n (line 含 \n 或 io.EOF 的最后一行无 \n)
		line = strings.TrimRight(line, "\n")
		// 去掉 \r (CRLF)
		line = strings.TrimRight(line, "\r")

		if line == "" {
			// 空行: frame 结束
			if len(datas) > 0 {
				f.data = []byte(strings.Join(datas, "\n"))
			}
			return f, nil
		}

		if strings.HasPrefix(line, ":") {
			// comment / heartbeat, ignore but 继续 accumulate frame (heartbeat 不算 frame 边界)
			continue
		}

		// field: value 分割
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			// 无冒号: field 无 value; 忽略
			continue
		}
		field := line[:colon]
		// value: 若 colon+1 是 ' ' 跳过单个空格 (SSE spec)
		val := ""
		if colon+1 <= len(line) {
			val = line[colon+1:]
			if strings.HasPrefix(val, " ") {
				val = val[1:]
			}
		}
		switch field {
		case "event":
			f.event = val
		case "id":
			f.id = val
		case "data":
			datas = append(datas, val)
		case "retry":
			// SSE spec: 重连间隔; v1 忽略 (不重连)
			continue
		default:
			// 未知字段忽略 (SSE spec)
		}
		if err == io.EOF {
			// 末行 (无 \n) 已处理; 之后必然 EOF
			return sseFrame{}, io.EOF
		}
	}
}

// isConnRefusedErr 启发式区分连接被拒 (端口未开 / DNS / TCP RST).
// ponytail: 不引入 net.OpError 深 unwrap; 用 strings 匹配常见特征.
func isConnRefusedErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "connection refused") || strings.Contains(msg, "no such host")
}

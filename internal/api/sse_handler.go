package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/v2up-32mb/yaa/internal/session"
)

// handleSSEEvents 实现 GET /api/v1/sessions/:id/events（SSE 订阅）。
// 文档协议：text/event-stream，每 15s ": heartbeat"，业务帧 `event: conversation\ndata: <json>\n\n`。
// v1 不保存 frame replay buffer，忽略 Last-Event-ID，不实现 sequence cursor。
func (s *Server) handleSSEEvents(w http.ResponseWriter, r *http.Request, sp SessionProvider, sessionID string) {
	s.mu.Lock()
	sm := s.sessionMgr
	s.mu.Unlock()
	if sm == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, 50301, "runtime not ready")
		return
	}

	// 预校验 Session 存在；同时暴露错误映射符合 session rest 路径。
	if _, err := sp.Get(r.Context(), sessionID); err != nil {
		s.writeSessionError(w, r, err)
		return
	}

	h, err := sm.Hub(sessionID)
	if err != nil {
		s.writeSessionError(w, r, err)
		return
	}
	sub := h.Subscribe()

	// SSE 响应头
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	flusher, ok := w.(http.Flusher)
	if !ok {
		flusher = noopFlusher{}
	}
	// 立即 flush，让客户端 Read 响应头 begun body 并开始读 events。
	flusher.Flush()

	enc := json.NewEncoder(w)

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	events := sub.Events()
	done := r.Context().Done()

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

			// 写 SSE 帧：event: conversation\ndata: <json>\n\n
			_, _ = w.Write([]byte("event: conversation\n"))
			_, _ = w.Write([]byte("data: "))
			_ = enc.Encode(frame)
			_, _ = w.Write([]byte("\n"))
			flusher.Flush()
			// session_end 帧为订阅终止终态；写完即退出来保证客户端不再等待。
			if frame.Type == "session_end" {
				return
			}
		case <-ticker.C:
			_, _ = w.Write([]byte(": heartbeat\n\n"))
			flusher.Flush()
		case <-done:
			return
		}
	}
}

// noopFlusher 在 ResponseWriter 不实现 http.Flusher 时兜底。
type noopFlusher struct{}

func (noopFlusher) Flush() {}

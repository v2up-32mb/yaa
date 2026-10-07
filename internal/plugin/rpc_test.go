package plugin

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// fakeRPC 是 pluginRPCInterface 的测试 mock.
type fakeRPC struct {
	closeMu      sync.Mutex
	closeCalls   int
	handshakeErr error
	closeErr     error
}

func (f *fakeRPC) Handshake(ctx context.Context, pv, id string) (HandshakeResponse, error) {
	return HandshakeResponse{ProtocolVersion: "1", PluginID: id}, f.handshakeErr
}
func (f *fakeRPC) Init(ctx context.Context, cfg map[string]any) error { return nil }
func (f *fakeRPC) Ready(ctx context.Context) (ReadyResponse, error) {
	return ReadyResponse{Capabilities: nil}, nil
}
func (f *fakeRPC) Health(ctx context.Context) (HealthResponse, error) {
	return HealthResponse{Level: "healthy"}, nil
}
func (f *fakeRPC) Stop(ctx context.Context) error { return nil }
func (f *fakeRPC) InvokeTool(ctx context.Context, req ToolRequest) (ToolResponse, error) {
	return ToolResponse{Result: map[string]any{}}, nil
}
func (f *fakeRPC) Close() error {
	f.closeMu.Lock()
	defer f.closeMu.Unlock()
	f.closeCalls++
	return f.closeErr
}

func TestRPCClientCloseTransportIdempotent(t *testing.T) {
	rpc := &fakeRPC{closeErr: nil}
	c := &RPCClient{rpc: rpc, Exited: closedChan()}
	if err := c.CloseTransport(); err != nil {
		t.Fatal(err)
	}
	if err := c.CloseTransport(); err != nil {
		t.Fatal(err)
	}
	// CloseTransport 只调用一次.
	if rpc.closeCalls != 1 {
		t.Fatalf("expected 1 close call, got %d", rpc.closeCalls)
	}
}

func TestRPCClientTerminateIdempotent(t *testing.T) {
	rpc := &fakeRPC{}
	cleanupCalled := 0
	c := &RPCClient{
		rpc:     rpc,
		Exited:  closedChan(),
		cleanup: func() { cleanupCalled++ },
	}
	if err := c.Terminate(); err != nil {
		t.Fatal(err)
	}
	if cleanupCalled != 1 {
		t.Fatalf("expected 1 cleanup call, got %d", cleanupCalled)
	}
	if rpc.closeCalls != 1 {
		t.Fatalf("expected 1 close call, got %d", rpc.closeCalls)
	}
}

func TestProxyHandleLoad(t *testing.T) {
	h := &ProxyHandle{}
	_, err := h.Load()
	if !errors.Is(err, ErrPluginUnavailable) {
		t.Fatalf("expected ErrPluginUnavailable, got %v", err)
	}

	c := &RPCClient{Exited: closedChan()}
	h.Store(c)
	loaded, err := h.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded != c {
		t.Fatal("load should return stored client")
	}
}

func TestProxyHandleInvalidate(t *testing.T) {
	h := &ProxyHandle{}
	c := &RPCClient{Exited: closedChan()}
	h.Store(c)
	// CAS old=c → nil: 成功
	if !h.Invalidate(c) {
		t.Fatal("invalidate CAS should succeed when client matches")
	}
	// 再试: current=nil, old=c → 失败
	if h.Invalidate(c) {
		t.Fatal("second invalidate should fail")
	}
	_, err := h.Load()
	if !errors.Is(err, ErrPluginUnavailable) {
		t.Fatalf("expected ErrPluginUnavailable after invalidate, got %v", err)
	}
}

// closedChan 返回一个已关闭的 channel, 用于测试.
func closedChan() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

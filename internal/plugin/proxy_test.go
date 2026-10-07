package plugin

import (
	"context"
	"testing"

	"github.com/v2up-32mb/yaa/internal/tool"
)

// pluginToolProxyTest mocks the underlying RPCClient.InvokeTool to return an injected response.
type invokeMockClient struct {
	invokeResp ToolResponse
	invokeErr  error
}

func (m *invokeMockClient) Handshake(ctx context.Context, pv, id string) (HandshakeResponse, error) {
	return HandshakeResponse{}, nil
}
func (m *invokeMockClient) Init(ctx context.Context, cfg map[string]any) error { return nil }
func (m *invokeMockClient) Ready(ctx context.Context) (ReadyResponse, error) {
	return ReadyResponse{}, nil
}
func (m *invokeMockClient) Health(ctx context.Context) (HealthResponse, error) {
	return HealthResponse{}, nil
}
func (m *invokeMockClient) Stop(ctx context.Context) error { return nil }
func (m *invokeMockClient) InvokeTool(ctx context.Context, req ToolRequest) (ToolResponse, error) {
	return m.invokeResp, m.invokeErr
}
func (m *invokeMockClient) Close() error { return nil }

// fakeRPCForPluginToolProxy wraps invokeMock as rpc field; but PluginToolProxy uses *RPCClient, which wraps rpc interface.
func newMockRPCClient(mock pluginRPCInterface) *RPCClient {
	return &RPCClient{rpc: mock, Exited: closedChan()}
}

func TestPluginToolProxyExecuteUnavailable(t *testing.T) {
	hdl := &ProxyHandle{}
	// no client stored → Load returns unavailable.
	ptp, err := NewPluginToolProxy("p", CapabilityDescriptor{Type: "tool", Name: "x", Description: "x", Schema: map[string]any{"type": "object"}}, hdl)
	if err != nil {
		t.Fatal(err)
	}
	_, execErr := ptp.Execute(context.Background(), tool.ExecutionScope{AgentID: "a1", SessionID: "s1"}, nil)
	if execErr == nil {
		t.Fatal("expected ErrPluginUnavailable")
	}
	if execErr.Error() != ErrPluginUnavailable.Error() {
		t.Fatalf("expected ErrPluginUnavailable, got %v", execErr)
	}
}

// TestPluginToolProxyExecuteProtocolErrorInvalidatesHandle UNSPECIFIED error → handle.invalidate + Terminate.
func TestPluginToolProxyExecuteProtocolErrorInvalidatesHandle(t *testing.T) {
	mock := &invokeMockClient{
		// return UNSPECIFIED error with matching RequestID for record
		invokeResp: ToolResponse{
			RequestID: "a1:x", // match construct
			Error:     &ToolError{Code: "UNSPECIFIED", Message: "bad"},
		},
	}
	client := newMockRPCClient(mock)
	hdl := &ProxyHandle{}
	hdl.Store(client)
	ptp, err := NewPluginToolProxy("p", CapabilityDescriptor{Type: "tool", Name: "x", Description: "x", Schema: map[string]any{"type": "object"}}, hdl)
	if err != nil {
		t.Fatal(err)
	}
	_, execErr := ptp.Execute(context.Background(), tool.ExecutionScope{AgentID: "a1", SessionID: "s1"}, nil)
	if execErr == nil {
		t.Fatal("expected error")
	}
	// handle should be invalidated (Load returns ErrPluginUnavailable).
	_, lErr := hdl.Load()
	if lErr == nil {
		t.Fatal("handle should now be unavailable")
	}
}

func TestPluginToolProxyExecuteWrongRequestIDInvalidates(t *testing.T) {
	mock := &invokeMockClient{
		invokeResp: ToolResponse{
			RequestID: "WRONG",
			Result:    map[string]any{"content": "ok"},
		},
	}
	client := newMockRPCClient(mock)
	hdl := &ProxyHandle{}
	hdl.Store(client)
	ptp, err := NewPluginToolProxy("p", CapabilityDescriptor{Type: "tool", Name: "x", Description: "x", Schema: map[string]any{"type": "object"}}, hdl)
	if err != nil {
		t.Fatal(err)
	}
	_, execErr := ptp.Execute(context.Background(), tool.ExecutionScope{AgentID: "a1"}, nil)
	if execErr == nil {
		t.Fatal("expected error for wrong request_id")
	}
	if _, lErr := hdl.Load(); lErr == nil {
		t.Fatal("handle should be invalidated")
	}
}

func TestPluginToolProxyExecuteSuccess(t *testing.T) {
	mock := &invokeMockClient{
		invokeResp: ToolResponse{
			RequestID: "a1:x",
			Result:    map[string]any{"content": "hello", "is_error": false},
		},
	}
	client := newMockRPCClient(mock)
	hdl := &ProxyHandle{}
	hdl.Store(client)
	ptp, err := NewPluginToolProxy("p", CapabilityDescriptor{Type: "tool", Name: "x", Description: "x", Schema: map[string]any{"type": "object"}}, hdl)
	if err != nil {
		t.Fatal(err)
	}
	res, execErr := ptp.Execute(context.Background(), tool.ExecutionScope{AgentID: "a1"}, nil)
	if execErr != nil {
		t.Fatalf("unexpected error: %v", execErr)
	}
	if res.Content != "hello" {
		t.Fatalf("expected content hello, got %q", res.Content)
	}
	// handle should still be valid
	if _, lErr := hdl.Load(); lErr != nil {
		t.Fatal("handle should remain valid after success")
	}
}

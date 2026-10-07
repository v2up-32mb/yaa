// Package pluginrpc client adapter: 把 grpc.ClientConn + pluginv1 client 适配为
// Manager 内部使用的生命周期接口.
// docs/plugin/loader.md §3: pluginRPC interface.
package pluginrpc

import (
	"context"
	"fmt"
	"net"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/v2up-32mb/yaa/pkg/pluginrpc/gen"
)

// Client 是 Plugin gRPC client 的封装, 提供 Handshake/Init/Ready/Health/Stop/InvokeTool 生命周期方法.
// docs/plugin/interface.md §2.
type Client struct {
	conn   *grpc.ClientConn
	client pluginv1.PluginServiceClient
}

// NewClient Dial plugin endpoint 并构造 Client.
// endpoint format: "unix://path" 或 "tcp://host:port".
func NewClient(ctx context.Context, endpoint string) (*Client, error) {
	network, addr, err := parseEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	dialer := func(ctx context.Context, target string) (net.Conn, error) {
		// ponytail: target 通常忽略, 用 parseEndpoint 已知值.
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	conn, err := grpc.DialContext(ctx, endpoint,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(dialer),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", endpoint, err)
	}
	return &Client{
		conn:   conn,
		client: pluginv1.NewPluginServiceClient(conn),
	}, nil
}

// parseEndpoint 分辨 unix:// or tcp:// scheme, 返回 network 和 address.
func parseEndpoint(endpoint string) (network, addr string, err error) {
	if strings.HasPrefix(endpoint, "unix://") {
		return "unix", endpoint[len("unix://"):], nil
	}
	if strings.HasPrefix(endpoint, "tcp://") {
		return "tcp", endpoint[len("tcp://"):], nil
	}
	return "", "", fmt.Errorf("unknown endpoint scheme: %s", endpoint)
}

// Handshake 调 Plugin.Handshake RPC.
func (c *Client) Handshake(ctx context.Context, runtimeProtocol, expectedPluginID string) (*pluginv1.HandshakeResponse, error) {
	return c.client.Handshake(ctx, &pluginv1.HandshakeRequest{
		RuntimeProtocol:  runtimeProtocol,
		ExpectedPluginId: expectedPluginID,
	})
}

// Init 调 Plugin.Init RPC (传 expanded config).
func (c *Client) Init(ctx context.Context, cfg map[string]any) error {
	st, err := structpb.NewStruct(cfg)
	if err != nil {
		return fmt.Errorf("init config to struct: %w", err)
	}
	_, err = c.client.Init(ctx, &pluginv1.InitRequest{Config: st})
	return err
}

// Ready 调 Plugin.Ready RPC, 获取 capability list.
func (c *Client) Ready(ctx context.Context) (*pluginv1.ReadyResponse, error) {
	return c.client.Ready(ctx, &pluginv1.ReadyRequest{})
}

// Health 调 Plugin.Health RPC.
func (c *Client) Health(ctx context.Context) (*pluginv1.HealthResponse, error) {
	return c.client.Health(ctx, &pluginv1.HealthRequest{})
}

// Stop 调 Plugin.Stop RPC.
func (c *Client) Stop(ctx context.Context) error {
	_, err := c.client.Stop(ctx, &pluginv1.StopRequest{})
	return err
}

// InvokeTool 调 Plugin.InvokeTool RPC.
func (c *Client) InvokeTool(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
	return c.client.InvokeTool(ctx, req)
}

// Close 关闭底层 gRPC connection.
func (c *Client) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

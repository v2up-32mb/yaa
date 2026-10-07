// Package plugin adapter: 将 pkg/pluginrpc.Client (*pluginv1 gen client wrapper) 适配到 internal pluginRPCInterface.
// docs/plugin/loader.md §3 pluginRPC interface.
package plugin

import (
	"context"
	"fmt"

	"google.golang.org/protobuf/types/known/structpb"

	"github.com/v2up-32mb/yaa/pkg/pluginrpc"
	pluginv1 "github.com/v2up-32mb/yaa/pkg/pluginrpc/gen"
)

// rpcAdapter 把 *pluginrpc.Client 适配为 pluginRPCInterface (internal type 形状).
type rpcAdapter struct {
	c *pluginrpc.Client
}

func newRPCAdapter(c *pluginrpc.Client) *rpcAdapter { return &rpcAdapter{c: c} }

func (a *rpcAdapter) Handshake(ctx context.Context, pv, id string) (HandshakeResponse, error) {
	r, err := a.c.Handshake(ctx, pv, id)
	if err != nil {
		return HandshakeResponse{}, err
	}
	return HandshakeResponse{
		ProtocolVersion: r.GetProtocolVersion(),
		PluginID:        r.GetPluginId(),
		PluginVersion:   r.GetPluginVersion(),
		StartupNonce:    r.GetStartupNonce(),
	}, nil
}

func (a *rpcAdapter) Init(ctx context.Context, cfg map[string]any) error {
	return a.c.Init(ctx, cfg)
}

func (a *rpcAdapter) Ready(ctx context.Context) (ReadyResponse, error) {
	r, err := a.c.Ready(ctx)
	if err != nil {
		return ReadyResponse{}, err
	}
	caps, err := convertCaps(r.GetCapabilities())
	if err != nil {
		return ReadyResponse{}, err
	}
	return ReadyResponse{Capabilities: caps}, nil
}

func (a *rpcAdapter) Health(ctx context.Context) (HealthResponse, error) {
	r, err := a.c.Health(ctx)
	if err != nil {
		return HealthResponse{}, err
	}
	hr := HealthResponse{
		Level:   healthLevelString(r.GetLevel()),
		Message: r.GetMessage(),
	}
	if t := r.GetObservedAt(); t != nil {
		hr.Timestamp = t.AsTime()
	}
	return hr, nil
}

func (a *rpcAdapter) Stop(ctx context.Context) error {
	return a.c.Stop(ctx)
}

func (a *rpcAdapter) InvokeTool(ctx context.Context, req ToolRequest) (ToolResponse, error) {
	args, err := structpb.NewStruct(req.Args)
	if err != nil {
		return ToolResponse{}, fmt.Errorf("InvokeTool args: %w", err)
	}
	out, err := a.c.InvokeTool(ctx, &pluginv1.ToolRequest{
		RequestId: req.RequestID,
		AgentId:   req.AgentID,
		SessionId: req.SessionID,
		Name:      req.Name,
		Arguments: args,
	})
	if err != nil {
		return ToolResponse{}, err
	}
	resp := ToolResponse{RequestID: out.GetRequestId()}
	if res := out.GetResult(); res != nil {
		m := map[string]any{
			"content":  res.GetContent(),
			"is_error": res.GetIsError(),
		}
		if meta := res.GetMeta(); meta != nil {
			m["meta"] = meta.AsMap()
		}
		resp.Result = m
		return resp, nil
	}
	if e := out.GetError(); e != nil {
		resp.Error = &ToolError{
			Code:      toolErrorString(e.GetCode()),
			Message:   e.GetMessage(),
			Retryable: e.GetRetryable(),
		}
		return resp, nil
	}
	return resp, ErrPluginProtocolIncompatible
}

// toolErrorString 把 gen ToolErrorCode enum 映射为稳定字符串.
func toolErrorString(c pluginv1.ToolErrorCode) string {
	switch c {
	case pluginv1.ToolErrorCode_TOOL_ERROR_CODE_INVALID_ARGUMENT:
		return "INVALID_ARGUMENT"
	case pluginv1.ToolErrorCode_TOOL_ERROR_CODE_TIMEOUT:
		return "TIMEOUT"
	case pluginv1.ToolErrorCode_TOOL_ERROR_CODE_UNAVAILABLE:
		return "UNAVAILABLE"
	case pluginv1.ToolErrorCode_TOOL_ERROR_CODE_INTERNAL:
		return "INTERNAL"
	default:
		return "UNSPECIFIED"
	}
}

func (a *rpcAdapter) Close() error { return a.c.Close() }

// healthLevelString 把 gen HealthLevel enum 映射为字符串.
func healthLevelString(l pluginv1.HealthLevel) string {
	switch l {
	case pluginv1.HealthLevel_HEALTH_LEVEL_HEALTHY:
		return HealthLevelHealthy
	case pluginv1.HealthLevel_HEALTH_LEVEL_DEGRADED:
		return HealthLevelDegraded
	case pluginv1.HealthLevel_HEALTH_LEVEL_UNHEALTHY:
		return HealthLevelUnhealthy
	default:
		return HealthLevelUnknown
	}
}

// capTypeString 把 gen CapabilityType enum 映射为 type 字符串.
func capTypeString(t pluginv1.CapabilityType) string {
	if t == pluginv1.CapabilityType_CAPABILITY_TYPE_TOOL {
		return "tool"
	}
	return "unspecified"
}

// convertCaps 把 gen CapabilityDescriptor 列表转换为 internal CapabilityDescriptor.
func convertCaps(genCaps []*pluginv1.CapabilityDescriptor) ([]CapabilityDescriptor, error) {
	if len(genCaps) == 0 {
		return nil, nil
	}
	result := make([]CapabilityDescriptor, 0, len(genCaps))
	for _, gc := range genCaps {
		var schema map[string]any
		if s := gc.GetSchema(); s != nil {
			schema = s.AsMap()
		}
		result = append(result, CapabilityDescriptor{
			Type:        capTypeString(gc.GetType()),
			Name:        gc.GetName(),
			Description: gc.GetDescription(),
			Schema:      schema,
		})
	}
	return result, nil
}

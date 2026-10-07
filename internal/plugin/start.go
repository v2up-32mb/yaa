// Package plugin Start: 进程启动 + RPC 序列.
// docs/plugin/loader.md §3: Start 依次 exec/Dial/Handshake/Init/Ready.
package plugin

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/v2up-32mb/yaa/pkg/pluginrpc"
)

// filteredPluginEnv 返回当前环境的子进程用副本, 去除敏感/不应回显的变量.
// docs/plugin/loader.md §3 + config-ref.md §3: 不包含 Secret 配置, 仅 Runtime 自身的环境传递.
// ponytail: 不禁白 ≤ 默认行为 = pass through, 但过滤 YAA_SECRET_*/YAA_PRIVATE_* 内部 prefix 防止泄漏.
func filteredPluginEnv() []string {
	env := os.Environ()
	var out []string
	for _, e := range env {
		// 跳过 YAA_SECRET_/YAA_PRIVATE_ 前缀, 防止 Secret 通过 env 直接传 plugin.
		if strings.HasPrefix(e, "YAA_SECRET_") || strings.HasPrefix(e, "YAA_PRIVATE_") {
			continue
		}
		out = append(out, e)
	}
	return out
}

// newStartupNonce 生成 32-byte crypto/rand 随机 nonce, base64.RawURLEncoding 编码.
// docs/plugin/loader.md §3 + interface.md §2: 长度 32 bytes.
func newStartupNonce(byteLen int) (string, error) {
	if byteLen <= 0 {
		byteLen = 32
	}
	b := make([]byte, byteLen)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand nonce: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// validateDescriptor 校验 Manifest ID/protocol_version 和 entry 文件存在性.
func validateDescriptor(d PluginDescriptor, protocolVersion string) error {
	if protocolVersion == "" {
		protocolVersion = "1"
	}
	if d.Manifest.ProtocolVersion != protocolVersion {
		return fmt.Errorf("%w: manifest protocol %q != supported %q",
			ErrPluginProtocolIncompatible, d.Manifest.ProtocolVersion, protocolVersion)
	}
	if d.Manifest.ID == "" {
		return fmt.Errorf("%w: manifest ID empty", ErrPluginManifestInvalid)
	}
	info, err := os.Lstat(d.EntryPath)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPluginEntryNotFound, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: not regular file", ErrPluginEntryNotFound)
	}
	if info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%w: no execute permission", ErrPluginEntryNotFound)
	}
	return nil
}

// newRPCClientDialer 注入测试时的 dial fake (默认 = pluginrpc.DialContext+NewClient).
// ponytail: tests override 通过脚改设置 (loaded.l =...); 默认 l.dial = defaultDialPlugin
type dialerFunc func(ctx context.Context, endpoint string) (pluginRPCInterface, error)

var defaultDialPlugin dialerFunc = func(ctx context.Context, endpoint string) (pluginRPCInterface, error) {
	c, err := pluginrpc.NewClient(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	return newRPCAdapter(c), nil
}

// Start 启动 plugin 进程并完成 handshake/init/ready 序列.
// docs/plugin/loader.md §3 + checklist.md 行25-30.
// ctx 只覆盖 exec/Dial/Handshake/Init/Ready, cancel 不终止进程 (非 CommandContext).
func (l *Loader) Start(ctx context.Context, d PluginDescriptor, cfg map[string]any) (*RPCClient, error) {
	if err := validateDescriptor(d, l.protocolVersion); err != nil {
		return nil, err
	}
	// config_schema 校验 (非 nil 才校验)
	if d.Manifest.ConfigSchema != nil {
		if err := validateConfigSchema(d.Manifest.ConfigSchema, cfg); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrPluginConfigInvalid, err)
		}
	}

	// 分配 endpoint
	endpoint, cleanup, err := pluginrpc.AllocateLocalEndpoint(d.Manifest.ID)
	if err != nil {
		return nil, fmt.Errorf("allocate plugin endpoint: %w", err)
	}
	// nonce (32 bytes)
	nonce, err := newStartupNonce(32)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("generate plugin nonce: %w", err)
	}
	if ctx.Err() != nil {
		cleanup()
		return nil, ctx.Err()
	}

	// startup ctx 取消不应该终止进程; exec.Command (非 Context) 满足约束.
	cmd := exec.Command(d.EntryPath, "--yaa-plugin-endpoint", endpoint)
	cmd.Dir = filepath.Dir(d.EntryPath)
	cmd.Env = append(filteredPluginEnv(), "YAA_PLUGIN_STARTUP_NONCE="+nonce)

	if err := cmd.Start(); err != nil {
		cleanup()
		return nil, fmt.Errorf("%w: %v", ErrPluginProcessStart, err)
	}

	exited := make(chan struct{})
	client := &RPCClient{cmd: cmd, Exited: exited, cleanup: cleanup}
	// 唯一 cmd.Wait goroutine; 其他模块通过 WaitErr/ch 关闭读取.
	go func() {
		client.waitErr = cmd.Wait()
		close(exited)
	}()

	// Dial plugin endpoint
	dial := l.dial
	if dial == nil {
		dial = defaultDialPlugin
	}
	rpc, err := dial(ctx, endpoint)
	if err != nil {
		_ = client.Terminate()
		return nil, fmt.Errorf("%w: %v", ErrPluginConnectionTimeout, err)
	}
	client.rpc = rpc

	// 失败统一路径: Terminate + return.
	fail := func(err error) (*RPCClient, error) {
		_ = client.Terminate()
		return nil, err
	}

	// Handshake
	hello, err := rpc.Handshake(ctx, "1", d.Manifest.ID)
	if err != nil {
		return fail(fmt.Errorf("%w: %v", ErrPluginProtocolIncompatible, err))
	}
	// 校验 response: ProtocolVersion/PluginID/PluginVersion/StartupNonce 全部匹配.
	if hello.ProtocolVersion != "1" ||
		hello.PluginID != d.Manifest.ID ||
		hello.PluginVersion != d.Manifest.Version ||
		subtle.ConstantTimeCompare([]byte(hello.StartupNonce), []byte(nonce)) != 1 {
		return fail(ErrPluginProtocolIncompatible)
	}

	// Init
	if err := rpc.Init(ctx, cfg); err != nil {
		return fail(fmt.Errorf("%w: %v", ErrPluginInitFailed, err))
	}

	// Ready
	ready, err := rpc.Ready(ctx)
	if err != nil {
		return fail(fmt.Errorf("%w: %v", ErrPluginInitFailed, err))
	}
	if err := matchCapabilities(d.Manifest.Provides, ready.Capabilities); err != nil {
		return fail(err)
	}
	client.Capabilities = ready.Capabilities
	return client, nil
}

// validateConfigSchema 是 JSON Schema 子集校验: 当前只做 schema{type:object, properties: <required>} 必填字段校验.
// ponytail: 完整 JSON Schema 实现 forthcoming; v1 MVP 只要求 required 字段存在, 不做 full type/format 校验.
func validateConfigSchema(schema map[string]any, cfg map[string]any) error {
	// TODO: 引入完整 JSON Schema validator (后续 commit)
	// 当前只做 required 字段校验
	requiredAny, ok := schema["required"]
	if !ok {
		return nil
	}
	// requiredAny should be []any
	reqList, ok := requiredAny.([]any)
	if !ok {
		return nil
	}
	cfg = ensureMap(cfg)
	for _, r := range reqList {
		name, ok := r.(string)
		if !ok {
			continue
		}
		if _, ok := cfg[name]; !ok {
			return fmt.Errorf("config missing required field %q", name)
		}
	}
	return nil
}

func ensureMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

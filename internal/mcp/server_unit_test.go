package mcp

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"
)

// encodeListCursor / decodeListCursor round-trip 一致性 (docs §3).
func TestListCursorRoundTrip(t *testing.T) {
	var digest [16]byte
	for i := range digest {
		digest[i] = byte(i)
	}
	for total := 1; total < 250; total += 13 {
		for offset := 0; offset < total; offset += listPageSize {
			cursor := encodeListCursor(digest, offset)
			raw, _ := json.Marshal(ListToolsParams{Cursor: cursor})
			got, err := decodeListCursor(raw, digest, total)
			if err != nil {
				t.Errorf("decode cursor total=%d offset=%d: %v", total, offset, err)
				continue
			}
			if got.Offset != offset {
				t.Errorf("offset decode total=%d offset=%d: got %d", total, offset, got.Offset)
			}
		}
	}
}

// decodeListCursor 拒绝 digest 不匹配 / offset 非页边界 / version 错误.
func TestListCursorRejectsTamperedCursor(t *testing.T) {
	var digest [16]byte
	for i := range digest {
		digest[i] = byte(i)
	}
	total := 150 // listPageSize=100, 第二页 offset=100
	cases := []struct {
		name   string
		cursor string
	}{
		{"digest mismatch", makeCursor([16]byte{0xff, 0xff, 0xff}, byte(1), 100)},
		{"version wrong", makeCursor(digest, byte(2), 100)},
		{"offset not page aligned", makeCursor(digest, byte(1), 50)},
		{"offset >= total", makeCursor(digest, byte(1), 150)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw, _ := json.Marshal(ListToolsParams{Cursor: c.cursor})
			_, err := decodeListCursor(raw, digest, total)
			if err == nil {
				t.Errorf("expected error, got nil")
			}
		})
	}
}

// decodeListCursor 接受合法 offset=100 total=150 (第二页也是合法).
func TestListCursorAcceptsAlignedSecondPage(t *testing.T) {
	var digest [16]byte
	for i := range digest {
		digest[i] = byte(i + 1)
	}
	cursor := encodeListCursor(digest, 100)
	raw, _ := json.Marshal(ListToolsParams{Cursor: cursor})
	got, err := decodeListCursor(raw, digest, 150)
	if err != nil {
		t.Errorf("valid second page cursor: %v", err)
	}
	if got.Offset != 100 {
		t.Errorf("expected offset=100, got %d", got.Offset)
	}
}

// decodeListCursor 接受空 cursor (offset 0).
func TestListCursorEmptyDefaultsOffsetZero(t *testing.T) {
	var digest [16]byte
	for _, raw := range []json.RawMessage{nil, []byte(`{}`), []byte(`{"cursor":""}`)} {
		got, err := decodeListCursor(raw, digest, 10)
		if err != nil || got.Offset != 0 {
			t.Errorf("empty cursor raw=%s: got %v offset=%d", raw, err, got.Offset)
		}
	}
}

// makeCursor 直接构造 21 bytes base64 RawURL cursor (绕过 encodeListCursor 以测试篡改形态).
func makeCursor(digest [16]byte, version byte, offset uint32) string {
	buf := make([]byte, listCursorBytes)
	buf[0] = version
	copy(buf[1:], digest[:])
	binary.BigEndian.PutUint32(buf[1+16:], offset)
	return base64.RawURLEncoding.EncodeToString(buf)
}

// ServerSession 状态机覆盖 (docs §4).
func TestServerSessionStateMachine(t *testing.T) {
	s := &ServerSession{ID: "t", Transport: "stdio", state: SessionNew}
	if s.State() != SessionNew {
		t.Fatalf("init state: got %v", s.State())
	}
	if s.Ready() || s.CanPing() {
		t.Errorf("new state: Ready=%v CanPing=%v (want both false)", s.Ready(), s.CanPing())
	}
	if err := s.MarkInitialized(); err == nil {
		// new → 直接 MarkInitialized 必须失败 (越序).
		t.Errorf("MarkInitialized from new: expected error")
	}
	if err := s.Negotiate(ProtocolVersion); err != nil {
		t.Fatalf("Negotiate: %v", err)
	}
	if s.State() != SessionNegotiated || s.Ready() {
		t.Errorf("negotiated: State=%v Ready=%v", s.State(), s.Ready())
	}
	if !s.CanPing() {
		t.Errorf("negotiated CanPing=false")
	}
	if err := s.Negotiate(ProtocolVersion); err == nil {
		// 重复 Negotiate 必须失败.
		t.Errorf("duplicate Negotiate: expected error")
	}
	if err := s.MarkInitialized(); err != nil {
		t.Fatalf("MarkInitialized: %v", err)
	}
	if !s.Ready() || !s.CanPing() {
		t.Errorf("ready: Ready=%v CanPing=%v", s.Ready(), s.CanPing())
	}
	if err := s.MarkInitialized(); err == nil {
		t.Errorf("duplicate MarkInitialized: expected error")
	}
	s.Close()
	if s.State() != SessionClosed || s.Ready() {
		t.Errorf("closed: State=%v Ready=%v", s.State(), s.Ready())
	}
}

// serverVersion 选择逻辑 (docs §2).
func TestServerVersion(t *testing.T) {
	if got := serverVersion("streamable_http", ""); got != ProtocolVersion {
		t.Errorf("streamable_http: got %q want %q", got, ProtocolVersion)
	}
	if got := serverVersion("sse", ""); got != LegacyProtocolVersion {
		t.Errorf("sse: got %q want %q", got, LegacyProtocolVersion)
	}
	if got := serverVersion("stdio", LegacyProtocolVersion); got != LegacyProtocolVersion {
		t.Errorf("stdio legacy: got %q want %q", got, LegacyProtocolVersion)
	}
	if got := serverVersion("stdio", ProtocolVersion); got != ProtocolVersion {
		t.Errorf("stdio modern: got %q want %q", got, ProtocolVersion)
	}
	if got := serverVersion("stdio", "invalid"); got != ProtocolVersion {
		t.Errorf("stdio invalid client version: got %q want ProtocolVersion", got)
	}
}

// catalogDigest 两次计算同 catalog 返回相同 digest; 空 catalog 与非空 digest 不同.
func TestCatalogDigestStable(t *testing.T) {
	catalog := []MCPTool{
		{Name: "echo", Description: "d", InputSchema: json.RawMessage(`{}`)},
		{Name: "shell", Description: "d2", InputSchema: json.RawMessage(`{}`)},
	}
	d1 := catalogDigest(catalog)
	d2 := catalogDigest(cloneTools(catalog))
	if d1 != d2 {
		t.Errorf("digest not stable across cloneTools")
	}
	dEmpty := catalogDigest(nil)
	if d1 == dEmpty {
		t.Errorf("digest of non-empty equals digest of empty")
	}
	// 不同 catalog 返不同 digest.
	catalog2 := []MCPTool{
		{Name: "echo", Description: "different", InputSchema: json.RawMessage(`{}`)},
		{Name: "shell", Description: "d2", InputSchema: json.RawMessage(`{}`)},
	}
	if d1 == catalogDigest(catalog2) {
		t.Errorf("digest identical for different description")
	}
}

// trimLine 去行尾换行, 不动行内空白 (docs §3.1).
func TestTrimLine(t *testing.T) {
	cases := map[string]string{
		"abc\n":      "abc",
		"abc\r\n":    "abc",
		"abc\r":      "abc",
		"  a\tb  \n": "  a\tb  ",
		"":           "",
	}
	for in, want := range cases {
		if got := trimLine(in); got != want {
			t.Errorf("trimLine(%q): got %q want %q", in, got, want)
		}
	}
}

// 确保 trimLine 不误删 JSON 包含字符串字面值中的换行转义 (json 字符串内 \n).
// json.Unmarshal 前先 trimLine: 行内不应包含裸 \n (已 trim), 但若 json 解析此处只去尾部 \n.
func TestTrimLineKeepsEscapedNewline(t *testing.T) {
	// json.Marshal 的 \\n (两字节) 是文字字面量, trim 不动.
	in := `{"jsonrpc":"2.0","method":"x","params":{"s":"a\nb"}}` + "\n"
	out := trimLine(in)
	if strings.Contains(out, "\n") {
		t.Errorf("trimLine left trailing newline in %q", out)
	}
	if !strings.Contains(out, `a\nb`) {
		t.Errorf("trimLine munged escaped \\n inside JSON string: %q", out)
	}
}

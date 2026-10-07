package session

import (
	"encoding/json"
	"fmt"

	"github.com/v2up-32mb/yaa/internal/provider"
	"github.com/v2up-32mb/yaa/internal/storage"
)

// validateMessageRole 校验单条消息 role 与字段组合，返回 wand 违规的稳定错误。
func validateMessageRole(msg provider.Message) error {
	switch msg.Role {
	case "user":
		if msg.Content == "" {
			return fmt.Errorf("%w: user content empty", ErrInvalidMessage)
		}
		if len(msg.ToolCalls) > 0 || msg.ToolCallID != "" {
			return fmt.Errorf("%w: user cannot have tool_calls or tool_call_id", ErrInvalidMessage)
		}
	case "assistant":
		if msg.ToolCallID != "" {
			return fmt.Errorf("%w: assistant cannot have tool_call_id", ErrInvalidMessage)
		}
		if len(msg.ToolCalls) > 0 {
			// tool call 模式：每个 ID 唯一，arguments 是合法 JSON
			seen := make(map[string]bool, len(msg.ToolCalls))
			for _, tc := range msg.ToolCalls {
				if tc.ID == "" {
					return fmt.Errorf("%w: tool call id empty", ErrInvalidMessage)
				}
				if seen[tc.ID] {
					return fmt.Errorf("%w: duplicate tool call id %q", ErrInvalidMessage, tc.ID)
				}
				seen[tc.ID] = true
				if tc.Function.Name == "" {
					return fmt.Errorf("%w: tool call function name empty", ErrInvalidMessage)
				}
				if tc.Type != "" && tc.Type != "function" {
					return fmt.Errorf("%w: tool call type must be function", ErrInvalidMessage)
				}
				if !isValidJSON(tc.Function.Arguments) {
					return fmt.Errorf("%w: tool call arguments not valid JSON", ErrInvalidMessage)
				}
				// canonical tool name 格式：非空、无空格、无特殊非法字符
				if !isValidCanonicalName(tc.Function.Name) {
					return fmt.Errorf("%w: tool call function name not canonical", ErrInvalidMessage)
				}
			}
		} else {
			// final assistant：content/reasoning/refusal 至少一个非空
			if msg.Content == "" && msg.ReasoningContent == "" && msg.Refusal == "" {
				return fmt.Errorf("%w: assistant final needs content/reasoning/refusal", ErrInvalidMessage)
			}
		}
	case "tool":
		if msg.ToolCallID == "" {
			return fmt.Errorf("%w: tool message needs tool_call_id", ErrInvalidMessage)
		}
		if len(msg.ToolCalls) > 0 {
			return fmt.Errorf("%w: tool message cannot have tool_calls", ErrInvalidMessage)
		}
		if msg.Name != "" && !isValidCanonicalName(msg.Name) {
			return fmt.Errorf("%w: tool message name not canonical", ErrInvalidMessage)
		}
	case "system":
		return fmt.Errorf("%w: system messages are not persisted", ErrInvalidMessage)
	default:
		return fmt.Errorf("%w: unknown role %q", ErrInvalidMessage, msg.Role)
	}
	return nil
}

func isValidJSON(s string) bool {
	if s == "" {
		return false
	}
	var v any
	return json.Unmarshal([]byte(s), &v) == nil
}

// isValidCanonicalName 检查 canonical tool name：非空、仅 ASCII 字母数字下划线连字符。
// ponytail: 不查注册表，只验格式；修复历史一词的破折号需另议。
func isValidCanonicalName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// validateBatchSequence 校验一个追加批次后的完整候选序列：
// - 非空时首条必须是 user
// - tool 的 ToolCallID 必须对应此前或同批 assistant 的 tool call
// - 非空 tool 的 Name 必须等于其 ToolCallID 对应 call 的 function name
// - 不存在悬空 tool（无对应 assistant call）
// - assistant.tool_calls 与其 tool results 须作为 unit 出现
func validateBatchSequence(messages []SessionMessage) error {
	if len(messages) == 0 {
		return nil
	}
	if messages[0].Payload.Role != "user" {
		return fmt.Errorf("%w: first message must be user", ErrInvalidMessageSequence)
	}

	// pendingCalls: tool_call_id -> function_name（来自已提交的 assistant tool calls，
	// 未被 tool result 匹配的）
	pending := make(map[string]string) // call_id -> func_name
	for i, m := range messages {
		switch m.Payload.Role {
		case "user":
			if len(pending) > 0 {
				return fmt.Errorf("%w: user message before tool results", ErrInvalidMessageSequence)
			}
		case "assistant":
			if len(m.Payload.ToolCalls) > 0 {
				for _, tc := range m.Payload.ToolCalls {
					pending[tc.ID] = tc.Function.Name
				}
			} else {
				// final assistant 不引入 pending
				if len(pending) > 0 {
					return fmt.Errorf("%w: final assistant before tool results", ErrInvalidMessageSequence)
				}
			}
		case "tool":
			fn, ok := pending[m.Payload.ToolCallID]
			if !ok {
				return fmt.Errorf("%w: tool result %q at index %d has no matching assistant call", ErrInvalidMessageSequence, m.Payload.ToolCallID, i)
			}
			if m.Payload.Name != "" && m.Payload.Name != fn {
				return fmt.Errorf("%w: tool result name %q does not match call function name %q", ErrInvalidMessageSequence, m.Payload.Name, fn)
			}
			delete(pending, m.Payload.ToolCallID)
		}
	}
	if len(pending) > 0 {
		return fmt.Errorf("%w: dangling assistant tool calls without results", ErrInvalidMessageSequence)
	}
	return nil
}

// messageBytes 返回单条 provider.Message 的 JSON 序列化字节数。
func messageBytes(msg provider.Message) (int, error) {
	b, err := json.Marshal(msg)
	if err != nil {
		return 0, err
	}
	return len(b), nil
}

// messageBytesLimit 检查每条消息不超过 maxMessageBytes。
// 根据文档 Storage 上限 16 MiB，复用 storage.MaxValueBytes 作为最终 fallback。
func messageBytesLimit(msg provider.Message, max int) error {
	n, err := messageBytes(msg)
	if err != nil {
		return err
	}
	if max <= 0 {
		max = storage.MaxValueBytes
	}
	if n > max {
		return fmt.Errorf("%w: message %d bytes > %d", ErrMessageTooLarge, n, max)
	}
	return nil
}

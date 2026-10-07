package session

import (
	"testing"

	"github.com/v2up-32mb/yaa/internal/provider"
)

func TestValidateMessageRole(t *testing.T) {
	cases := []struct {
		name    string
		msg     provider.Message
		wantErr error
	}{
		{"user ok", provider.Message{Role: "user", Content: "hi"}, nil},
		{"user empty content", provider.Message{Role: "user"}, ErrInvalidMessage},
		{"user with tool calls", provider.Message{Role: "user", Content: "hi", ToolCalls: []provider.ToolCall{{ID: "c1"}}}, ErrInvalidMessage},
		{"assistant final ok", provider.Message{Role: "assistant", Content: "answer"}, nil},
		{"assistant refusal", provider.Message{Role: "assistant", Refusal: "no"}, nil},
		{"assistant tool call unbalanced no opts", provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "c1", Function: provider.ToolCallFunction{Name: "weather", Arguments: "{}"}}}}, nil},
		{"assistant tool call dup id", provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "c1"}, {ID: "c1"}}}, ErrInvalidMessage},
		{"assistant tool call empty id", provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{{}}}, ErrInvalidMessage},
		{"assistant tool call bad args", provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "c1", Function: provider.ToolCallFunction{Name: "w", Arguments: "not json"}}}}, ErrInvalidMessage},
		{"assistant tool call bad name", provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "c1", Function: provider.ToolCallFunction{Name: "bad name", Arguments: "{}"}}}}, ErrInvalidMessage},
		{"tool ok", provider.Message{Role: "tool", ToolCallID: "c1", Content: "result"}, nil},
		{"tool missing call id", provider.Message{Role: "tool"}, ErrInvalidMessage},
		{"tool has calls", provider.Message{Role: "tool", ToolCallID: "c1", ToolCalls: []provider.ToolCall{{}}}, ErrInvalidMessage},
		{"system rejected", provider.Message{Role: "system", Content: "sys"}, ErrInvalidMessage},
		{"unknown role", provider.Message{Role: "other", Content: "x"}, ErrInvalidMessage},
		{"assistant tool_call_id", provider.Message{Role: "assistant", ToolCallID: "c1", Content: "x"}, ErrInvalidMessage},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateMessageRole(c.msg)
			if c.wantErr == nil {
				if err != nil {
					t.Fatalf("expected nil, got %v", err)
				}
			} else {
				if err == nil {
					t.Fatalf("expected %v, got nil", c.wantErr)
				}
			}
		})
	}
}

func TestValidateBatchSequence(t *testing.T) {
	cases := []struct {
		name string
		msgs []SessionMessage
		ok   bool
	}{
		{"empty", nil, true},
		{"starts with assistant", []SessionMessage{{Payload: provider.Message{Role: "assistant", Content: "x"}}}, false},
		{"simple user then assistant", []SessionMessage{
			{Payload: provider.Message{Role: "user", Content: "hi"}},
			{Payload: provider.Message{Role: "assistant", Content: "answer"}},
		}, true},
		{"tool unit complete", []SessionMessage{
			{Payload: provider.Message{Role: "user", Content: "what"}},
			{Payload: provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "c1", Function: provider.ToolCallFunction{Name: "w", Arguments: "{}"}}}}},
			{Payload: provider.Message{Role: "tool", ToolCallID: "c1", Name: "w", Content: "ok"}},
		}, true},
		{"tool unit dangling", []SessionMessage{
			{Payload: provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "c1", Function: provider.ToolCallFunction{Name: "w", Arguments: "{}"}}}}},
		}, false},
		{"tool result no call", []SessionMessage{
			{Payload: provider.Message{Role: "tool", ToolCallID: "c1"}},
		}, false},
		{"final before tool results", []SessionMessage{
			{Payload: provider.Message{Role: "user", Content: "x"}},
			{Payload: provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "c1", Function: provider.ToolCallFunction{Name: "w", Arguments: "{}"}}}}},
			{Payload: provider.Message{Role: "assistant", Content: "final"}},
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateBatchSequence(c.msgs)
			if c.ok && err != nil {
				t.Fatalf("expected ok, got %v", err)
			}
			if !c.ok && err == nil {
				t.Fatalf("expected error, got nil")
			}
		})
	}
}

func TestStateAllowed(t *testing.T) {
	cases := []struct {
		op   string
		st   State
		want error
	}{
		{"run_turn", StateActive, nil},
		{"run_turn", StatePaused, ErrSessionPaused},
		{"run_turn", StateClosed, ErrSessionClosed},
		{"pause", StateActive, nil},
		{"pause", StateCreated, ErrInvalidStateTransition},
		{"pause", StateClosed, ErrSessionClosed},
		{"resume", StatePaused, nil},
		{"resume", StateActive, ErrInvalidStateTransition},
		{"resume", StateClosed, ErrSessionClosed},
		{"close", StateClosed, nil},
		{"delete", StateClosed, nil},
		{"clear_messages", StateClosed, ErrSessionClosed},
		{"clear_messages", StateActive, nil},
	}
	for _, c := range cases {
		got := stateAllowed(c.op, c.st)
		if got == nil && c.want != nil {
			t.Errorf("op=%s st=%s expected %v, got nil", c.op, c.st, c.want)
		}
		if got != nil && c.want == nil {
			t.Errorf("op=%s st=%s expected nil, got %v", c.op, c.st, got)
		}
	}
}

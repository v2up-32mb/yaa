package session

import (
	"errors"
	"fmt"
)

// 稳定错误集合，调用方与 Remote API 通过 errors.Is 分类。
var (
	ErrSessionNotFound         = errors.New("session: session not found")
	ErrMessageNotFound         = errors.New("session: message not found")
	ErrAgentNotFound           = errors.New("session: agent not found")
	ErrSessionClosed           = errors.New("session: session closed")
	ErrSessionPaused           = errors.New("session: session paused")
	ErrSessionExpired          = errors.New("session: session expired")
	ErrInvalidStateTransition  = errors.New("session: invalid state transition")
	ErrInvalidMessage          = errors.New("session: invalid message")
	ErrInvalidMessageSequence  = errors.New("session: invalid message sequence")
	ErrMessageTooLarge         = errors.New("session: message too large")
	ErrMessageLimitExceeded    = errors.New("session: message limit exceeded")
	ErrSessionSnapshotTooLarge = errors.New("session: snapshot too large")
	ErrCapacityExceeded        = errors.New("session: capacity exceeded")
	ErrSessionConfigInvalid    = errors.New("session: invalid config")
	ErrPersistenceFailed       = errors.New("session: persistence failed")
	ErrRestoreFailed           = errors.New("session: restore failed")
	ErrSchemaUnsupported       = errors.New("session: unsupported schema")
	ErrManagerClosed           = errors.New("session: manager closed")
	ErrInvalidModelOverride    = errors.New("session: invalid model override")
	ErrInvalidTurnID           = errors.New("session: invalid turn id")
	ErrTurnIDConflict          = errors.New("session: turn id already used")
	ErrTurnNotActive           = errors.New("session: turn not active")
)

// OpError 带操作上下文包装底层稳定错误。
type OpError struct {
	Op        string
	SessionID string
	Err       error
}

func (e *OpError) Error() string {
	if e.SessionID == "" {
		return fmt.Sprintf("session %s: %v", e.Op, e.Err)
	}
	return fmt.Sprintf("session %s %s: %v", e.SessionID, e.Op, e.Err)
}

func (e *OpError) Unwrap() error { return e.Err }

package store

import "fmt"

type ErrorKind string

const (
	ErrNotFound         ErrorKind = "not_found"
	ErrConflict         ErrorKind = "conflict"
	ErrInvalidInput     ErrorKind = "invalid_input"
	ErrClosed           ErrorKind = "task_closed"
	ErrUnauthorized     ErrorKind = "unauthorized"
	ErrForbidden        ErrorKind = "forbidden" // 任务级权限（#175 B2）：调用方不是这条任务的创建者 / 承接者 / 管理者
	ErrOperationFailed  ErrorKind = "operation_failed"
	ErrIdempotencyClash ErrorKind = "idempotency_clash"
)

type AppError struct {
	Kind            ErrorKind
	Message         string
	CurrentRevision int64
}

func (e *AppError) Error() string {
	if e.CurrentRevision > 0 {
		return fmt.Sprintf("%s: %s (current revision: %d)", e.Kind, e.Message, e.CurrentRevision)
	}
	return fmt.Sprintf("%s: %s", e.Kind, e.Message)
}

func NewNotFoundError(msg string) *AppError {
	return &AppError{Kind: ErrNotFound, Message: msg}
}

func NewConflictError(msg string, currentRev int64) *AppError {
	return &AppError{Kind: ErrConflict, Message: msg, CurrentRevision: currentRev}
}

func NewInvalidInputError(msg string) *AppError {
	return &AppError{Kind: ErrInvalidInput, Message: msg}
}

func NewTaskClosedError(msg string) *AppError {
	return &AppError{Kind: ErrClosed, Message: msg}
}

// NewForbiddenError 表示任务级权限不允许这次操作；消息写给智能体看：谁能做、该怎么做。
func NewForbiddenError(msg string) *AppError {
	return &AppError{Kind: ErrForbidden, Message: msg}
}

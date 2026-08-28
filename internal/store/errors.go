package store

import "fmt"

type ErrorKind string

const (
	ErrNotFound          ErrorKind = "not_found"
	ErrConflict          ErrorKind = "conflict"
	ErrInvalidInput      ErrorKind = "invalid_input"
	ErrClosed            ErrorKind = "task_closed"
	ErrUnauthorized      ErrorKind = "unauthorized"
	ErrOperationFailed   ErrorKind = "operation_failed"
	ErrIdempotencyClash  ErrorKind = "idempotency_clash"
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

package cqrs

import (
	"context"
	"time"
)

type CommandExecutionStatus string

const (
	CommandExecutionStatusPending CommandExecutionStatus = "pending"
	CommandExecutionStatusSuccess CommandExecutionStatus = "success"
	CommandExecutionStatusFailed  CommandExecutionStatus = "failed"
)

type CommandExecution struct {
	CommandID   string
	HandlerName string
	CommandName string
	Payload     []byte
	Status      CommandExecutionStatus
	ErrorData   []byte // JSON serialized CommandError
	StartedAt   time.Time
	FinishedAt  *time.Time
}

type CommandExecutionStore interface {
	RecordPending(ctx context.Context, tx Tx, commandID string, commandName string, payload []byte) error
	RecordStarted(ctx context.Context, tx Tx, commandID string, handlerName string) error
	RecordSuccess(ctx context.Context, tx Tx, commandID string) error
	RecordFailure(ctx context.Context, tx Tx, commandID string, errorData []byte) error
	GetStatus(ctx context.Context, tx Tx, commandID string) (CommandExecutionStatus, error)
	GetExecution(ctx context.Context, tx Tx, commandID string) (*CommandExecution, error)
	GetNextPending(ctx context.Context, tx Tx) (*CommandExecution, error)
}

// PendingCounter is an optional interface implemented by stores that can count pending commands for backpressure.
type PendingCounter interface {
	CountPending(ctx context.Context, tx Tx) (int, error)
}

// DialectAware is an optional interface implemented by stores that expose their database dialect.
type DialectAware interface {
	Dialect() Dialect
}

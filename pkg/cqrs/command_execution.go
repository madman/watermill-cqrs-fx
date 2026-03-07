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
	Status      CommandExecutionStatus
	ErrorData   []byte // JSON serialized CommandError
	StartedAt   time.Time
	FinishedAt  *time.Time
}

type CommandExecutionStore interface {
	RecordStarted(ctx context.Context, tx Tx, commandID string, handlerName string) error
	RecordSuccess(ctx context.Context, tx Tx, commandID string) error
	RecordFailure(ctx context.Context, tx Tx, commandID string, errorData []byte) error
	GetStatus(ctx context.Context, tx Tx, commandID string) (CommandExecutionStatus, error)
	GetExecution(ctx context.Context, tx Tx, commandID string) (*CommandExecution, error)
}

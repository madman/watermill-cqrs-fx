package cqrs

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Command is an interface for all commands.
// Every command must have a unique identifier.
type Command interface {
	CommandID() string
}

// BaseCommand provides a default implementation of the Command interface.
type BaseCommand struct {
	ID string `json:"command_id"`
}

func (c BaseCommand) CommandID() string {
	return c.ID
}

func NewBaseCommand() BaseCommand {
	return BaseCommand{ID: uuid.NewString()}
}

// Query is a marker interface for all queries.
type Query any

// Event is a marker interface for all events.
type Event any

// CommandBus defines the interface for dispatching commands.
type CommandBus interface {
	Send(ctx context.Context, cmd Command) error
	Wait(ctx context.Context, commandID string, timeout time.Duration) (*CommandExecution, error)
}

// QueryBus defines the interface for executing queries.
type QueryBus interface {
	Execute(ctx context.Context, query Query, result any) error
}

// EventBus defines the interface for publishing events.
type EventBus interface {
	Publish(ctx context.Context, tx Tx, events ...Event) error
}

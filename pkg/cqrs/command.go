package cqrs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ThreeDotsLabs/watermill/components/cqrs"
	"github.com/madman/watermill-cqrs-fx/pkg/cqrs/cmderr"
)

// CommandHandler defines the interface for handling commands with an explicit transaction.
type CommandHandler interface {
	HandlerName() string
	NewCommand() any
	Handle(ctx context.Context, tx Tx, cmd any) error
}

// NewCommandHandler creates a new CommandHandler from a function with a specific command type and transaction type.
func NewCommandHandler[C any, T Tx](name string, handler func(ctx context.Context, tx T, cmd *C) error) CommandHandler {
	return &genericCommandHandler[C, T]{
		name:    name,
		handler: handler,
	}
}

type genericCommandHandler[C any, T Tx] struct {
	name    string
	handler func(ctx context.Context, tx T, cmd *C) error
}

func (h *genericCommandHandler[C, T]) HandlerName() string {
	return h.name
}

func (h *genericCommandHandler[C, T]) NewCommand() any {
	return new(C)
}

func (h *genericCommandHandler[C, T]) Handle(ctx context.Context, tx Tx, cmd any) error {
	var t T
	if tx != nil {
		var ok bool
		t, ok = tx.(T)
		if !ok {
			// This should generally not happen if types are consistent
			return fmt.Errorf("invalid transaction type: expected %T, got %T", t, tx)
		}
	}
	return h.handler(ctx, t, cmd.(*C))
}

// CommandBusConfig defines the configuration for the command bus.
type CommandBusConfig struct {
	// WaitTimeout is the default timeout for the Wait method.
	WaitTimeout time.Duration
	// WaitTickerInterval is the interval between checks for command status in the Wait method.
	WaitTickerInterval time.Duration
}

type commandBus struct {
	bus       *cqrs.CommandBus
	execStore CommandExecutionStore
	config    CommandBusConfig
}

func NewCommandBus(bus *cqrs.CommandBus, execStore CommandExecutionStore, config CommandBusConfig) CommandBus {
	if config.WaitTickerInterval == 0 {
		config.WaitTickerInterval = 200 * time.Millisecond
	}
	return &commandBus{
		bus:       bus,
		execStore: execStore,
		config:    config,
	}
}

func (b *commandBus) Send(ctx context.Context, cmd Command) error {
	return b.bus.Send(ctx, cmd)
}

func (b *commandBus) Wait(ctx context.Context, commandID string, timeout time.Duration) (*CommandExecution, error) {
	if b.execStore == nil {
		return nil, fmt.Errorf("command execution store not configured")
	}

	if timeout == 0 {
		timeout = b.config.WaitTimeout
	}

	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	ticker := time.NewTicker(b.config.WaitTickerInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			exec, err := b.execStore.GetExecution(ctx, nil, commandID)
			if err != nil {
				return nil, err
			}

			if exec != nil && (exec.Status == CommandExecutionStatusSuccess || exec.Status == CommandExecutionStatusFailed) {
				if exec.Status == CommandExecutionStatusFailed && len(exec.ErrorData) > 0 {
					ce, err := cmderr.Decode(exec.ErrorData)
					if err == nil {
						return exec, ce
					}
					// Fallback if decode fails
					return exec, errors.New("command failed (serialized error decode error)")
				} else if exec.Status == CommandExecutionStatusFailed {
					return exec, errors.New("command failed (no error data)")
				}
				return exec, nil
			}
		}
	}
}

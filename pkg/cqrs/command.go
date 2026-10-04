package cqrs

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ThreeDotsLabs/watermill/components/cqrs"
	"github.com/madman/cmderr"
	"golang.org/x/sync/singleflight"
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

// ErrQueueFull is returned by CommandBus.Send when pending commands reach or exceed MaxPending.
var ErrQueueFull = errors.New("command queue is full")

// SQLQueueConfig defines the configuration for SQL-backed command queueing and workers.
type SQLQueueConfig struct {
	// PollInterval is the fallback polling interval. 0 -> default (30s), <0 -> disabled.
	PollInterval time.Duration
	// Notifier is the wake-up signal source. nil -> NewChannelNotifier().
	Notifier Notifier
	// DisableWakeup disables wake-up signaling (pure polling mode).
	DisableWakeup bool
	// Concurrency is the number of worker goroutines. 0 -> default (1).
	Concurrency int
	// MaxPending is the backpressure limit for pending commands. 0 -> default (1000), <0 -> unlimited.
	// Note: Because capacity checking is not atomic with command insertion and may use cached counts,
	// MaxPending acts as a soft limit under concurrent load.
	MaxPending int
	// PendingCountCacheTTL is the TTL for caching COUNT(*) pending commands. 0 -> default (1s), <0 -> disabled.
	PendingCountCacheTTL time.Duration
	// ErrorBackoff is the pause after a database error in the worker. 0 -> default (1s), <0 -> disabled.
	ErrorBackoff time.Duration
	// Dialect optionally specifies or overrides the database dialect.
	Dialect Dialect
}

// Normalize applies default values for zero fields while preserving negative values as disabled.
func (cfg *SQLQueueConfig) Normalize() {
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 30 * time.Second
	}
	if cfg.Notifier == nil {
		cfg.Notifier = NewChannelNotifier()
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 1
	}
	if cfg.MaxPending == 0 {
		cfg.MaxPending = 1000
	}
	if cfg.PendingCountCacheTTL == 0 {
		cfg.PendingCountCacheTTL = 1 * time.Second
	}
	if cfg.ErrorBackoff == 0 {
		cfg.ErrorBackoff = 1 * time.Second
	}
}

// CommandBusConfig defines the configuration for the command bus.
type CommandBusConfig struct {
	// WaitTimeout is the default timeout for the Wait method.
	WaitTimeout time.Duration
	// WaitTickerInterval is the interval between checks for command status in the Wait method.
	WaitTickerInterval time.Duration
	// UseSQLQueue indicates if we should queue commands directly in the database table.
	UseSQLQueue bool
	// SQLQueue contains options for SQL command queueing and worker behavior.
	SQLQueue SQLQueueConfig
}

type commandBus struct {
	bus               *cqrs.CommandBus
	execStore         CommandExecutionStore
	marshaler         cqrs.CommandEventMarshaler
	config            CommandBusConfig
	pendingCountCache pendingCountCache
	countGroup        singleflight.Group
}

type pendingCountCache struct {
	mu       sync.Mutex
	count    int
	cachedAt time.Time
}

func NewCommandBus(
	bus *cqrs.CommandBus,
	execStore CommandExecutionStore,
	marshaler cqrs.CommandEventMarshaler,
	config CommandBusConfig,
) CommandBus {
	if config.WaitTickerInterval == 0 {
		config.WaitTickerInterval = 200 * time.Millisecond
	}
	config.SQLQueue.Normalize()
	return &commandBus{
		bus:       bus,
		execStore: execStore,
		marshaler: marshaler,
		config:    config,
	}
}

func (b *commandBus) checkQueueCapacity(ctx context.Context) error {
	if b.config.SQLQueue.MaxPending <= 0 {
		return nil
	}

	b.pendingCountCache.mu.Lock()
	now := time.Now()
	ttl := b.config.SQLQueue.PendingCountCacheTTL
	if ttl > 0 && !b.pendingCountCache.cachedAt.IsZero() && now.Sub(b.pendingCountCache.cachedAt) < ttl {
		count := b.pendingCountCache.count
		b.pendingCountCache.mu.Unlock()
		if count >= b.config.SQLQueue.MaxPending {
			return ErrQueueFull
		}
		return nil
	}
	b.pendingCountCache.mu.Unlock()

	// Use singleflight to deduplicate concurrent COUNT(*) queries across parallel Sends when cache expires.
	val, err, _ := b.countGroup.Do("count_pending", func() (any, error) {
		b.pendingCountCache.mu.Lock()
		now := time.Now()
		if ttl > 0 && !b.pendingCountCache.cachedAt.IsZero() && now.Sub(b.pendingCountCache.cachedAt) < ttl {
			cached := b.pendingCountCache.count
			b.pendingCountCache.mu.Unlock()
			return cached, nil
		}
		b.pendingCountCache.mu.Unlock()

		count, err := b.execStore.CountPending(ctx, nil)
		if err != nil {
			return 0, fmt.Errorf("failed to count pending commands: %w", err)
		}

		b.pendingCountCache.mu.Lock()
		b.pendingCountCache.count = count
		b.pendingCountCache.cachedAt = time.Now()
		b.pendingCountCache.mu.Unlock()

		return count, nil
	})
	if err != nil {
		return err
	}

	count := val.(int)
	if count >= b.config.SQLQueue.MaxPending {
		return ErrQueueFull
	}
	return nil
}

func (b *commandBus) Send(ctx context.Context, cmd Command) error {
	if b.config.UseSQLQueue {
		if b.execStore == nil {
			return errors.New("command execution store not configured for SQL queueing")
		}
		if b.marshaler == nil {
			return errors.New("marshaler not configured for SQL queueing")
		}

		if err := b.checkQueueCapacity(ctx); err != nil {
			return err
		}

		msg, err := b.marshaler.Marshal(cmd)
		if err != nil {
			return fmt.Errorf("failed to marshal command for SQL queue: %w", err)
		}

		commandName := b.marshaler.Name(cmd)
		if err := b.execStore.RecordPending(ctx, nil, cmd.CommandID(), commandName, msg.Payload); err != nil {
			return err
		}

		b.pendingCountCache.mu.Lock()
		b.pendingCountCache.count++
		b.pendingCountCache.mu.Unlock()

		if !b.config.SQLQueue.DisableWakeup && b.config.SQLQueue.Notifier != nil {
			b.config.SQLQueue.Notifier.Notify()
		}

		return nil
	}

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
					ce, err := cmderr.DecodeJSON(exec.ErrorData)
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

package cqrs

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	watermill_cqrs "github.com/ThreeDotsLabs/watermill/components/cqrs"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/madman/cmderr"
)

type SQLCommandQueueWorker struct {
	execStore      CommandExecutionStore
	txManager      TransactionManager
	marshaler      watermill_cqrs.CommandEventMarshaler
	logger         watermill.LoggerAdapter
	config         SQLQueueConfig
	outboxNotifier Notifier
	handlers       map[string]CommandHandler
	stopChan       chan struct{}
	wg             sync.WaitGroup
}

// NewSQLCommandQueueWorkerWithConfig creates a new SQLCommandQueueWorker with custom queue config and optional outbox notifier.
func NewSQLCommandQueueWorkerWithConfig(
	execStore CommandExecutionStore,
	txManager TransactionManager,
	marshaler watermill_cqrs.CommandEventMarshaler,
	logger watermill.LoggerAdapter,
	rawHandlers []any,
	cfg SQLQueueConfig,
	outboxNotifier Notifier,
) (*SQLCommandQueueWorker, error) {
	cfg.Normalize()

	// Enforce single worker concurrency for non-MySQL dialects (e.g. SQLite lacks SKIP LOCKED)
	dialect := cfg.Dialect
	if dialect == "" {
		if da, ok := execStore.(DialectAware); ok {
			dialect = da.Dialect()
		}
	}
	if dialect != DialectMySQL {
		cfg.Concurrency = 1
	}

	handlers := make(map[string]CommandHandler)
	for _, h := range rawHandlers {
		if ch, ok := h.(CommandHandler); ok {
			cmdName := marshaler.Name(ch.NewCommand())
			handlers[cmdName] = ch
		}
	}

	return &SQLCommandQueueWorker{
		execStore:      execStore,
		txManager:      txManager,
		marshaler:      marshaler,
		logger:         logger,
		config:         cfg,
		outboxNotifier: outboxNotifier,
		handlers:       handlers,
		stopChan:       make(chan struct{}),
	}, nil
}

// NewSQLCommandQueueWorker creates a new SQLCommandQueueWorker with default configuration.
func NewSQLCommandQueueWorker(
	execStore CommandExecutionStore,
	txManager TransactionManager,
	marshaler watermill_cqrs.CommandEventMarshaler,
	logger watermill.LoggerAdapter,
	rawHandlers []any,
) (*SQLCommandQueueWorker, error) {
	return NewSQLCommandQueueWorkerWithConfig(
		execStore,
		txManager,
		marshaler,
		logger,
		rawHandlers,
		SQLQueueConfig{},
		nil,
	)
}

func (w *SQLCommandQueueWorker) Start(ctx context.Context) error {
	w.logger.Info("Starting SQL Command Queue Worker", watermill.LogFields{
		"registered_handlers": len(w.handlers),
		"concurrency":         w.config.Concurrency,
		"poll_interval":       w.config.PollInterval.String(),
		"disable_wakeup":      w.config.DisableWakeup,
	})

	for i := 0; i < w.config.Concurrency; i++ {
		w.wg.Add(1)
		workerID := i
		go func() {
			defer w.wg.Done()
			w.runWorker(ctx, workerID)
		}()
	}
	return nil
}

func (w *SQLCommandQueueWorker) runWorker(ctx context.Context, workerID int) {
	// Immediate recovery drain upon worker start to pick up any pending commands from restart
	w.drain(ctx)

	var tickerChan <-chan time.Time
	if w.config.PollInterval > 0 {
		ticker := time.NewTicker(w.config.PollInterval)
		defer ticker.Stop()
		tickerChan = ticker.C
	}

	var notifierChan <-chan struct{}
	if !w.config.DisableWakeup && w.config.Notifier != nil {
		notifierChan = w.config.Notifier.C()
	}

	for {
		select {
		case <-w.stopChan:
			w.logger.Info("Stopping SQL Command Queue Worker", watermill.LogFields{"worker_id": workerID})
			return
		case <-ctx.Done():
			w.logger.Info("SQL Command Queue Worker context cancelled, stopping", watermill.LogFields{"worker_id": workerID})
			return
		case <-tickerChan:
			w.drain(ctx)
		case <-notifierChan:
			w.drain(ctx)
		}
	}
}

func (w *SQLCommandQueueWorker) drain(ctx context.Context) {
	for {
		select {
		case <-w.stopChan:
			return
		case <-ctx.Done():
			return
		default:
		}

		processed, err := w.processNext(ctx)
		if err != nil {
			w.logger.Error("Failed processing SQL command", err, nil)
			if w.config.ErrorBackoff > 0 {
				select {
				case <-w.stopChan:
					return
				case <-ctx.Done():
					return
				case <-time.After(w.config.ErrorBackoff):
				}
			}
			break
		}
		if !processed {
			break
		}
	}
}

func (w *SQLCommandQueueWorker) Stop() error {
	close(w.stopChan)
	w.wg.Wait()
	return nil
}

func (w *SQLCommandQueueWorker) processNext(ctx context.Context) (bool, error) {
	var processed bool
	var processErr error
	var commandID string
	var commandName string

	// We start a transaction for the entire fetch-and-process step
	err := w.txManager.WithinTransaction(ctx, func(ctx context.Context, tx Tx) error {
		// 1. Fetch next pending command (using skip locked)
		exec, err := w.execStore.GetNextPending(ctx, tx)
		if err != nil {
			return err
		}
		if exec == nil {
			return nil // No pending command
		}

		processed = true
		commandID = exec.CommandID
		commandName = exec.CommandName

		w.logger.Info("SQL command worker: started processing command", watermill.LogFields{
			"command_id":   commandID,
			"command_name": commandName,
		})

		// 2. Find the handler
		handler, ok := w.handlers[commandName]
		if !ok {
			return fmt.Errorf("no handler registered for command: %s", commandName)
		}

		// 3. Update status to started/in_progress
		if err := w.execStore.RecordStarted(ctx, tx, commandID, handler.HandlerName()); err != nil {
			return err
		}

		// 4. Decode the command payload
		msg := message.NewMessage(commandID, exec.Payload)
		cmd := handler.NewCommand()
		if err := w.marshaler.Unmarshal(msg, cmd); err != nil {
			return fmt.Errorf("failed to unmarshal command payload: %w", err)
		}

		// 5. Execute handler in transaction
		err = handler.Handle(ctx, tx, cmd)
		if err != nil {
			processErr = err
			// Return error to force transaction ROLLBACK of all aggregate changes!
			return fmt.Errorf("handler error: %w", err)
		}

		// 6. Record success
		if err := w.execStore.RecordSuccess(ctx, tx, commandID); err != nil {
			return err
		}

		w.logger.Info("SQL command worker: successfully processed command", watermill.LogFields{
			"command_id": commandID,
		})

		return nil
	})

	if err != nil {
		if processed && processErr != nil {
			// The handler failed. Aggregate changes were fully rolled back.
			// Now we write the failure record to the DB using a separate transaction.
			var ce *cmderr.CommandError
			if !errors.As(processErr, &ce) {
				ce = cmderr.Wrap("COMMAND_FAILED", processErr, processErr.Error())
			}
			data, _ := ce.EncodeJSON()
			if recErr := w.execStore.RecordFailure(ctx, nil, commandID, data); recErr != nil {
				w.logger.Error("Failed to record command failure status", recErr, watermill.LogFields{
					"command_id": commandID,
				})
			}
			w.logger.Error("SQL command worker: command processing failed (reverted all changes, recorded failure)", processErr, watermill.LogFields{
				"command_id": commandID,
			})
			return true, nil
		}
		return processed, err
	}

	if processed && w.outboxNotifier != nil {
		w.outboxNotifier.Notify()
	}

	return processed, nil
}

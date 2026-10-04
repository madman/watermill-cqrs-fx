package cqrs

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/message"
)

// SQLOutboxWorkerConfig defines the configuration for the SQL outbox worker.
type SQLOutboxWorkerConfig struct {
	// TableName is the outbox database table name. "" -> default ("events").
	TableName string
	// PollInterval is the fallback polling interval. 0 -> default (30s), <0 -> disabled.
	PollInterval time.Duration
	// BatchSize is the batch size for querying outbox records. 0 -> default (50).
	BatchSize int
	// Notifier is the wake-up signal source. nil -> NewChannelNotifier().
	Notifier Notifier
	// ErrorBackoff is the pause after a database error in the worker. 0 -> default (1s), <0 -> disabled.
	ErrorBackoff time.Duration
}

// Normalize sets default values for zero fields while preserving negative values as disabled.
func (cfg *SQLOutboxWorkerConfig) Normalize() {
	if cfg.TableName == "" {
		cfg.TableName = "events"
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 30 * time.Second
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 50
	}
	if cfg.ErrorBackoff == 0 {
		cfg.ErrorBackoff = 1 * time.Second
	}
}

type SQLOutboxWorker struct {
	db        *sql.DB
	publisher message.Publisher
	logger    watermill.LoggerAdapter
	config    SQLOutboxWorkerConfig
	stopChan  chan struct{}
	stopOnce  sync.Once
	wg        sync.WaitGroup
}

// NewSQLOutboxWorkerWithConfig creates a new SQLOutboxWorker with parameterized configuration.
func NewSQLOutboxWorkerWithConfig(
	db *sql.DB,
	publisher message.Publisher,
	logger watermill.LoggerAdapter,
	cfg SQLOutboxWorkerConfig,
) *SQLOutboxWorker {
	cfg.Normalize()
	return &SQLOutboxWorker{
		db:        db,
		publisher: publisher,
		logger:    logger,
		config:    cfg,
		stopChan:  make(chan struct{}),
	}
}

// NewSQLOutboxWorker creates a new SQLOutboxWorker with default configuration and custom table name.
func NewSQLOutboxWorker(
	db *sql.DB,
	tableName string,
	publisher message.Publisher,
	logger watermill.LoggerAdapter,
) *SQLOutboxWorker {
	return NewSQLOutboxWorkerWithConfig(
		db,
		publisher,
		logger,
		SQLOutboxWorkerConfig{TableName: tableName},
	)
}

func (w *SQLOutboxWorker) Start(ctx context.Context) error {
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		w.logger.Info("Starting SQL Outbox Worker", watermill.LogFields{
			"table":         w.config.TableName,
			"poll_interval": w.config.PollInterval.String(),
			"batch_size":    w.config.BatchSize,
		})

		// Immediate recovery drain on startup
		w.drain(ctx)

		var tickerChan <-chan time.Time
		if w.config.PollInterval > 0 {
			ticker := time.NewTicker(w.config.PollInterval)
			defer ticker.Stop()
			tickerChan = ticker.C
		}

		var notifierChan <-chan struct{}
		if w.config.Notifier != nil {
			notifierChan = w.config.Notifier.C()
		}

		for {
			select {
			case <-w.stopChan:
				w.logger.Info("Stopping SQL Outbox Worker", nil)
				return
			case <-ctx.Done():
				w.logger.Info("SQL Outbox Worker context cancelled, stopping", nil)
				return
			case <-tickerChan:
				w.drain(ctx)
			case <-notifierChan:
				w.drain(ctx)
			}
		}
	}()
	return nil
}

func (w *SQLOutboxWorker) drain(ctx context.Context) {
	for {
		select {
		case <-w.stopChan:
			return
		case <-ctx.Done():
			return
		default:
		}

		count, err := w.processBatch(ctx)
		if err != nil {
			w.logger.Error("Failed to process outbox records", err, nil)
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

		if count < w.config.BatchSize {
			break
		}
	}
}

func (w *SQLOutboxWorker) Stop() error {
	w.stopOnce.Do(func() {
		close(w.stopChan)
	})
	w.wg.Wait()
	return nil
}

// Notifier returns the wake-up Notifier configured for this outbox worker, or nil if none.
func (w *SQLOutboxWorker) Notifier() Notifier {
	return w.config.Notifier
}

func (w *SQLOutboxWorker) processBatch(ctx context.Context) (int, error) {
	query := fmt.Sprintf(`
		SELECT id, topic, payload, metadata FROM %s ORDER BY occurred_at ASC LIMIT %d
	`, w.config.TableName, w.config.BatchSize)

	rows, err := w.db.QueryContext(ctx, query)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()

	type record struct {
		id       string
		topic    string
		payload  []byte
		metadata string
	}

	var records []record
	for rows.Next() {
		var r record
		if err := rows.Scan(&r.id, &r.topic, &r.payload, &r.metadata); err != nil {
			return 0, err
		}
		records = append(records, r)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	_ = rows.Close()

	if len(records) == 0 {
		return 0, nil
	}

	w.logger.Debug("Processing outbox events batch", watermill.LogFields{
		"count": len(records),
	})

	for _, r := range records {
		msg := message.NewMessage(r.id, r.payload)

		var meta map[string]string
		if err := json.Unmarshal([]byte(r.metadata), &meta); err == nil {
			for k, v := range meta {
				msg.Metadata.Set(k, v)
			}
		}

		// Publish to the real Watermill publisher (e.g. GoChannel, RabbitMQ)
		if err := w.publisher.Publish(r.topic, msg); err != nil {
			return 0, fmt.Errorf("failed to publish outbox event %s to topic %s: %w", r.id, r.topic, err)
		}

		// Delete upon successful publication
		deleteQuery := fmt.Sprintf("DELETE FROM %s WHERE id = ?", w.config.TableName)
		_, err = w.db.ExecContext(ctx, deleteQuery, r.id)
		if err != nil {
			return 0, fmt.Errorf("failed to delete outbox record %s: %w", r.id, err)
		}
	}

	return len(records), nil
}

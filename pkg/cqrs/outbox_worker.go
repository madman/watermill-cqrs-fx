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

type SQLOutboxWorker struct {
	db           *sql.DB
	tableName    string
	publisher    message.Publisher
	logger       watermill.LoggerAdapter
	pollInterval time.Duration
	stopChan     chan struct{}
	wg           sync.WaitGroup
}

func NewSQLOutboxWorker(
	db *sql.DB,
	tableName string,
	publisher message.Publisher,
	logger watermill.LoggerAdapter,
) *SQLOutboxWorker {
	return &SQLOutboxWorker{
		db:           db,
		tableName:    tableName,
		publisher:    publisher,
		logger:       logger,
		pollInterval: 100 * time.Millisecond,
		stopChan:     make(chan struct{}),
	}
}

func (w *SQLOutboxWorker) Start(ctx context.Context) error {
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		w.logger.Info("Starting SQL Outbox Worker", watermill.LogFields{
			"table": w.tableName,
		})

		ticker := time.NewTicker(w.pollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-w.stopChan:
				w.logger.Info("Stopping SQL Outbox Worker", nil)
				return
			case <-ctx.Done():
				w.logger.Info("SQL Outbox Worker context cancelled, stopping", nil)
				return
			case <-ticker.C:
				if err := w.processOutbox(ctx); err != nil {
					w.logger.Error("Failed to process outbox records", err, nil)
				}
			}
		}
	}()
	return nil
}

func (w *SQLOutboxWorker) Stop() error {
	close(w.stopChan)
	w.wg.Wait()
	return nil
}

func (w *SQLOutboxWorker) processOutbox(ctx context.Context) error {
	// Query next batch of unpublished outbox records
	query := fmt.Sprintf(`
		SELECT id, topic, payload, metadata FROM %s ORDER BY occurred_at ASC LIMIT 50
	`, w.tableName)

	rows, err := w.db.QueryContext(ctx, query)
	if err != nil {
		return err
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
			return err
		}
		records = append(records, r)
	}
	_ = rows.Close()

	if len(records) == 0 {
		return nil
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
			return fmt.Errorf("failed to publish outbox event %s to topic %s: %w", r.id, r.topic, err)
		}

		// Delete upon successful publication
		deleteQuery := fmt.Sprintf("DELETE FROM %s WHERE id = ?", w.tableName)
		_, err = w.db.ExecContext(ctx, deleteQuery, r.id)
		if err != nil {
			return fmt.Errorf("failed to delete outbox record %s: %w", r.id, err)
		}
	}

	return nil
}

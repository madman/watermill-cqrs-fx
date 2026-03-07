package cqrs

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

type SQLOutbox struct {
	db    *sql.DB
	table string
}

func NewSQLOutbox(db *sql.DB, table string) *SQLOutbox {
	return &SQLOutbox{
		db:    db,
		table: table,
	}
}

func (o *SQLOutbox) Save(ctx context.Context, tx Tx, records ...OutboxRecord) error {
	query := fmt.Sprintf(`
		INSERT INTO %s (id, topic, payload, metadata, occurred_at)
		VALUES (?, ?, ?, ?, ?)
	`, o.table)

	for _, r := range records {
		metadataJSON, err := json.Marshal(r.Metadata)
		if err != nil {
			return fmt.Errorf("failed to marshal metadata: %w", err)
		}

		_, err = tx.ExecContext(ctx, query, r.ID, r.Topic, r.Payload, string(metadataJSON), r.OccurredAt)
		if err != nil {
			return fmt.Errorf("failed to insert outbox record: %w", err)
		}
	}

	return nil
}

type SQLTransactionManager struct {
	db *sql.DB
}

func NewSQLTransactionManager(db *sql.DB) *SQLTransactionManager {
	return &SQLTransactionManager{db: db}
}

func (m *SQLTransactionManager) WithinTransaction(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()

	if err := fn(ctx, tx); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			return fmt.Errorf("error: %v, rollback error: %v", err, rollbackErr)
		}
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

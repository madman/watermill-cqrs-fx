package cqrs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type Dialect string

const (
	DialectSQLite Dialect = "sqlite"
	DialectMySQL  Dialect = "mysql"
)

type sqlCommandExecutionStore struct {
	db        *sql.DB
	tableName string
	dialect   Dialect
}

func NewSQLCommandExecutionStore(db *sql.DB, tableName string, dialect Dialect) CommandExecutionStore {
	return &sqlCommandExecutionStore{
		db:        db,
		tableName: tableName,
		dialect:   dialect,
	}
}

func (s *sqlCommandExecutionStore) RecordStarted(ctx context.Context, tx Tx, commandID string, handlerName string) error {
	var query string
	if s.dialect == DialectMySQL {
		query = fmt.Sprintf(`
			INSERT INTO %s (command_id, handler_name, status, started_at)
			VALUES (?, ?, ?, CURRENT_TIMESTAMP)
			ON DUPLICATE KEY UPDATE
				status = VALUES(status),
				started_at = VALUES(started_at),
				finished_at = NULL,
				error_data = NULL
		`, s.tableName)
	} else {
		// Default to SQLite/PostgreSQL syntax
		query = fmt.Sprintf(`
			INSERT INTO %s (command_id, handler_name, status, started_at)
			VALUES (?, ?, ?, CURRENT_TIMESTAMP)
			ON CONFLICT(command_id) DO UPDATE SET
				status = excluded.status,
				started_at = excluded.started_at,
				finished_at = NULL,
				error_data = NULL
		`, s.tableName)
	}

	_, err := tx.ExecContext(ctx, query, commandID, handlerName, CommandExecutionStatusPending)
	return err
}

func (s *sqlCommandExecutionStore) RecordSuccess(ctx context.Context, tx Tx, commandID string) error {
	query := fmt.Sprintf(`
		UPDATE %s SET status = ?, finished_at = CURRENT_TIMESTAMP WHERE command_id = ?
	`, s.tableName)

	_, err := tx.ExecContext(ctx, query, CommandExecutionStatusSuccess, commandID)
	return err
}

func (s *sqlCommandExecutionStore) RecordFailure(ctx context.Context, tx Tx, commandID string, errorData []byte) error {
	query := fmt.Sprintf(`
		UPDATE %s SET status = ?, error_data = ?, finished_at = CURRENT_TIMESTAMP WHERE command_id = ?
	`, s.tableName)

	_, err := tx.ExecContext(ctx, query, CommandExecutionStatusFailed, errorData, commandID)
	return err
}

func (s *sqlCommandExecutionStore) GetExecution(ctx context.Context, tx Tx, commandID string) (*CommandExecution, error) {
	query := fmt.Sprintf(`
		SELECT command_id, handler_name, status, error_data, started_at, finished_at
		FROM %s WHERE command_id = ?
	`, s.tableName)

	var exec CommandExecution
	var rows *sql.Rows
	var err error

	if tx != nil {
		rows, err = tx.QueryContext(ctx, query, commandID)
	} else {
		rows, err = s.db.QueryContext(ctx, query, commandID)
	}

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	if !rows.Next() {
		return nil, nil
	}

	err = rows.Scan(
		&exec.CommandID,
		&exec.HandlerName,
		&exec.Status,
		&exec.ErrorData,
		&exec.StartedAt,
		&exec.FinishedAt,
	)
	if err != nil {
		return nil, err
	}

	return &exec, nil
}

func (s *sqlCommandExecutionStore) GetStatus(ctx context.Context, tx Tx, commandID string) (CommandExecutionStatus, error) {
	query := fmt.Sprintf(`SELECT status FROM %s WHERE command_id = ?`, s.tableName)

	var status CommandExecutionStatus
	var err error
	if tx != nil {
		err = tx.QueryRowContext(ctx, query, commandID).Scan(&status)
	} else {
		err = s.db.QueryRowContext(ctx, query, commandID).Scan(&status)
	}

	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return status, err
}

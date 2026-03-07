package cqrs

import (
	"context"
	"database/sql"
)

// Tx defines the interface for database transactions.
// It is designed so that *sql.Tx satisfies it.
type Tx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	PrepareContext(ctx context.Context, query string) (*sql.Stmt, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Check that *sql.Tx implements Tx at compile time.
var _ Tx = (*sql.Tx)(nil)

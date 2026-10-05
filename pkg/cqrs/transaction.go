package cqrs

import (
	"context"
	"database/sql"
	"sync"
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

type txHooksKey struct{}

type txHooks struct {
	mu          sync.Mutex
	afterCommit []func()
}

// RegisterAfterCommit registers a callback to be called after the current transaction commits successfully.
// Returns true if registered within an active transaction context, false otherwise.
func RegisterAfterCommit(ctx context.Context, fn func()) bool {
	if fn == nil {
		return false
	}
	if h, ok := ctx.Value(txHooksKey{}).(*txHooks); ok && h != nil {
		h.mu.Lock()
		h.afterCommit = append(h.afterCommit, fn)
		h.mu.Unlock()
		return true
	}
	return false
}

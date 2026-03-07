package cqrs

import (
	"context"
	"time"
)

type OutboxRecord struct {
	ID         string
	Topic      string
	Payload    []byte
	Metadata   map[string]string
	OccurredAt time.Time
}

// Outbox defines the interface for storing events.
type Outbox interface {
	// Save stores events in the outbox within the current transaction.
	Save(ctx context.Context, tx Tx, records ...OutboxRecord) error
}

// TransactionManager defines the interface for running code within a database transaction.
type TransactionManager interface {
	// WithinTransaction executes the given function within a transaction.
	// If the function returns an error, the transaction is rolled back.
	WithinTransaction(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error
}

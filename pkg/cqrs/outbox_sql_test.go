package cqrs

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

func setupTestDB(t *testing.T) *sql.DB {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)

	_, err = db.Exec(`
		CREATE TABLE outbox (
			id TEXT PRIMARY KEY,
			topic TEXT NOT NULL,
			payload BLOB NOT NULL,
			metadata TEXT NOT NULL,
			occurred_at DATETIME NOT NULL
		)
	`)
	require.NoError(t, err)

	return db
}

func TestSQLOutbox_Save(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	outbox := NewSQLOutbox(db, "outbox")
	tm := NewSQLTransactionManager(db)

	record := OutboxRecord{
		ID:         uuid.NewString(),
		Topic:      "test_topic",
		Payload:    []byte(`{"foo":"bar"}`),
		Metadata:   map[string]string{"key": "value"},
		OccurredAt: time.Now().Round(time.Second),
	}

	err := tm.WithinTransaction(context.Background(), func(ctx context.Context, tx Tx) error {
		return outbox.Save(ctx, tx, record)
	})
	require.NoError(t, err)

	// Verify in DB manually
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM outbox WHERE id = ?", record.ID).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestSQLTransactionManager_Rollback(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	outbox := NewSQLOutbox(db, "outbox")
	tm := NewSQLTransactionManager(db)

	err := tm.WithinTransaction(context.Background(), func(ctx context.Context, tx Tx) error {
		_ = outbox.Save(ctx, tx, OutboxRecord{
			ID:         uuid.NewString(),
			Topic:      "test",
			Payload:    []byte("test"),
			Metadata:   map[string]string{},
			OccurredAt: time.Now(),
		})
		return fmt.Errorf("simulated error")
	})
	assert.Error(t, err)

	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM outbox").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

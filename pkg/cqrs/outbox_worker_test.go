package cqrs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	watermill_cqrs "github.com/ThreeDotsLabs/watermill/components/cqrs"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

type memoryPublisher struct {
	mu       sync.Mutex
	messages map[string][]*message.Message
	notify   chan struct{}
}

func newMemoryPublisher() *memoryPublisher {
	return &memoryPublisher{
		messages: make(map[string][]*message.Message),
		notify:   make(chan struct{}, 100),
	}
}

func (p *memoryPublisher) Publish(topic string, messages ...*message.Message) error {
	p.mu.Lock()
	p.messages[topic] = append(p.messages[topic], messages...)
	p.mu.Unlock()
	select {
	case p.notify <- struct{}{}:
	default:
	}
	return nil
}

func (p *memoryPublisher) Close() error { return nil }

func (p *memoryPublisher) count(topic string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.messages[topic])
}

func setupOutboxTestDB(t *testing.T, tableName string) *sql.DB {
	db, err := sql.Open("sqlite", "file::memory:?cache=shared")
	require.NoError(t, err)

	_, err = db.Exec(fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id VARCHAR(255) PRIMARY KEY,
			topic VARCHAR(255) NOT NULL,
			payload BLOB NOT NULL,
			metadata TEXT NOT NULL,
			occurred_at DATETIME NOT NULL
		)
	`, tableName))
	require.NoError(t, err)

	_, err = db.Exec(fmt.Sprintf("DELETE FROM %s", tableName))
	require.NoError(t, err)

	return db
}

type testEvent struct {
	Greeting string `json:"greeting"`
}

func TestSQLOutboxWorker_PublishedImmediatelyAfterCommandCommit(t *testing.T) {
	db, store, tm := setupCommandQueueTestDB(t)
	defer func() { _ = db.Close() }()

	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS events (
			id VARCHAR(255) PRIMARY KEY,
			topic VARCHAR(255) NOT NULL,
			payload BLOB NOT NULL,
			metadata TEXT NOT NULL,
			occurred_at DATETIME NOT NULL
		)
	`)
	require.NoError(t, err)

	logger := watermill.NopLogger{}
	marshaler := watermill_cqrs.JSONMarshaler{}
	commandNotifier := NewChannelNotifier()
	outboxNotifier := NewChannelNotifier()
	pub := newMemoryPublisher()

	outbox := NewSQLOutbox(db, "events")
	eventBus := NewEventBusWithConfig(nil, marshaler, outbox, EventBusConfig{
		TxManager:      tm,
		OutboxNotifier: outboxNotifier,
	})

	handler := NewCommandHandler("greetHandler", func(ctx context.Context, tx Tx, cmd *testCmd) error {
		return eventBus.Publish(ctx, tx, testEvent{Greeting: "Hello " + cmd.Data})
	})

	// Start command queue worker
	cmdWorkerCfg := SQLQueueConfig{
		PollInterval: 1 * time.Hour,
		Notifier:     commandNotifier,
	}
	cmdWorker, err := NewSQLCommandQueueWorkerWithConfig(
		store,
		tm,
		marshaler,
		logger,
		[]any{handler},
		cmdWorkerCfg,
		outboxNotifier,
	)
	require.NoError(t, err)

	// Start outbox worker with long fallback polling
	outboxWorkerCfg := SQLOutboxWorkerConfig{
		TableName:    "events",
		PollInterval: 1 * time.Hour,
		Notifier:     outboxNotifier,
	}
	outboxWorker := NewSQLOutboxWorkerWithConfig(db, pub, logger, outboxWorkerCfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, cmdWorker.Start(ctx))
	defer func() { _ = cmdWorker.Stop() }()
	require.NoError(t, outboxWorker.Start(ctx))
	defer func() { _ = outboxWorker.Stop() }()

	bus := NewCommandBus(nil, store, marshaler, CommandBusConfig{
		UseSQLQueue: true,
		SQLQueue: SQLQueueConfig{
			Notifier: commandNotifier,
		},
	})

	start := time.Now()
	cmd := &testCmd{BaseCommand: NewBaseCommand(), Data: "World"}
	require.NoError(t, bus.Send(ctx, cmd))

	// Wait for published message in outbox publisher
	select {
	case <-pub.notify:
		elapsed := time.Since(start)
		assert.Less(t, elapsed, 1*time.Second, "outbox publication latency should be sub-second after command commit")
		assert.Equal(t, 1, pub.count(marshaler.Name(testEvent{})))
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for outbox event publication via wake-up")
	}

	// Verify outbox table has deleted the record
	assert.Eventually(t, func() bool {
		var remaining int
		if err := db.QueryRow("SELECT COUNT(*) FROM events").Scan(&remaining); err != nil {
			return false
		}
		return remaining == 0
	}, 1*time.Second, 10*time.Millisecond, "outbox record should be deleted after publication")
}

func TestEventBus_AfterCommitNotificationAndRollback(t *testing.T) {
	db := setupOutboxTestDB(t, "events")
	defer func() { _ = db.Close() }()

	outbox := NewSQLOutbox(db, "events")
	tm := NewSQLTransactionManager(db)
	marshaler := watermill_cqrs.JSONMarshaler{}
	outboxNotifier := NewChannelNotifier()

	eb := NewEventBusWithConfig(nil, marshaler, outbox, EventBusConfig{
		TxManager:      tm,
		OutboxNotifier: outboxNotifier,
	})

	// 1. Transaction succeeds: AfterCommit fires outboxNotifier
	err := tm.WithinTransaction(context.Background(), func(ctx context.Context, tx Tx) error {
		return eb.Publish(ctx, tx, testEvent{Greeting: "Committed"})
	})
	require.NoError(t, err)

	select {
	case <-outboxNotifier.C():
		// Received wake-up signal after commit!
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected outboxNotifier wake-up after commit")
	}

	// 2. Transaction rolls back: AfterCommit MUST NOT fire outboxNotifier
	err = tm.WithinTransaction(context.Background(), func(ctx context.Context, tx Tx) error {
		_ = eb.Publish(ctx, tx, testEvent{Greeting: "Will Rollback"})
		return errors.New("rollback test")
	})
	require.Error(t, err)

	select {
	case <-outboxNotifier.C():
		t.Fatal("outboxNotifier should NOT have fired on rollback")
	default:
		// Clean, no wake-up signal
	}
}

func TestSQLOutboxWorker_DrainBatchLoop(t *testing.T) {
	db := setupOutboxTestDB(t, "events")
	defer func() { _ = db.Close() }()

	pub := newMemoryPublisher()
	logger := watermill.NopLogger{}
	notifier := NewChannelNotifier()

	// Pre-insert 25 records into outbox
	for i := 0; i < 25; i++ {
		_, err := db.Exec(
			"INSERT INTO events (id, topic, payload, metadata, occurred_at) VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)",
			fmt.Sprintf("evt-%d", i), "test_topic", []byte(`{}`), `{}`,
		)
		require.NoError(t, err)
	}

	cfg := SQLOutboxWorkerConfig{
		TableName:    "events",
		BatchSize:    10,            // Batches of 10 -> will require 3 drain iterations (10 + 10 + 5)
		PollInterval: 1 * time.Hour, // Polling disabled
		Notifier:     notifier,
	}

	worker := NewSQLOutboxWorkerWithConfig(db, pub, logger, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, worker.Start(ctx))
	defer func() { _ = worker.Stop() }()

	// Wait briefly for startup recovery drain
	assert.Eventually(t, func() bool {
		return pub.count("test_topic") == 25
	}, 1*time.Second, 20*time.Millisecond, "all 25 items should be drained via batch loop")

	var remaining int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM events").Scan(&remaining))
	assert.Equal(t, 0, remaining)
}

func TestSQLOutboxWorker_CustomTableName(t *testing.T) {
	customTable := "custom_outbox_table"
	db := setupOutboxTestDB(t, customTable)
	defer func() { _ = db.Close() }()

	pub := newMemoryPublisher()
	logger := watermill.NopLogger{}

	_, err := db.Exec(
		fmt.Sprintf("INSERT INTO %s (id, topic, payload, metadata, occurred_at) VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)", customTable),
		"evt-1", "custom_topic", []byte(`{}`), `{}`,
	)
	require.NoError(t, err)

	cfg := SQLOutboxWorkerConfig{
		TableName:    customTable,
		PollInterval: 1 * time.Hour,
	}
	worker := NewSQLOutboxWorkerWithConfig(db, pub, logger, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, worker.Start(ctx))
	defer func() { _ = worker.Stop() }()

	assert.Eventually(t, func() bool {
		return pub.count("custom_topic") == 1
	}, 1*time.Second, 20*time.Millisecond)
}

func TestSQLOutboxWorker_DoubleStop_NoPanic(t *testing.T) {
	db := setupOutboxTestDB(t, "events")
	defer func() { _ = db.Close() }()

	pub := newMemoryPublisher()
	logger := watermill.NopLogger{}
	worker := NewSQLOutboxWorker(db, "events", pub, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, worker.Start(ctx))

	assert.NotPanics(t, func() {
		_ = worker.Stop()
		_ = worker.Stop()
	})
}

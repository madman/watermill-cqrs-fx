package cqrs

import (
	"context"
	"database/sql"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	watermill_cqrs "github.com/ThreeDotsLabs/watermill/components/cqrs"
	_ "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func getMySQLTestDB(t *testing.T) *sql.DB {
	dsn := os.Getenv("MYSQL_DSN")
	if dsn == "" {
		t.Skip("skipping MySQL test: MYSQL_DSN not set")
		return nil
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("failed to open MySQL connection: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("MySQL not reachable at %s: %v", dsn, err)
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS test_command_executions (
			command_id VARCHAR(255) PRIMARY KEY,
			handler_name VARCHAR(255) NOT NULL DEFAULT '',
			command_name VARCHAR(255) NOT NULL DEFAULT '',
			payload BLOB,
			status VARCHAR(32) NOT NULL DEFAULT 'pending',
			error_data BLOB,
			started_at DATETIME NOT NULL,
			finished_at DATETIME,
			INDEX idx_status_started_at (status, started_at)
		)
	`)
	require.NoError(t, err)

	_, err = db.Exec("DELETE FROM test_command_executions")
	require.NoError(t, err)

	return db
}

func TestMySQL_ConcurrentWorkersNoDuplicateProcessing(t *testing.T) {
	db := getMySQLTestDB(t)
	if db == nil {
		return
	}
	defer func() {
		_, _ = db.Exec("DROP TABLE IF EXISTS test_command_executions")
		_ = db.Close()
	}()

	store := NewSQLCommandExecutionStore(db, "test_command_executions", DialectMySQL)
	tm := NewSQLTransactionManager(db)
	logger := watermill.NopLogger{}
	marshaler := watermill_cqrs.JSONMarshaler{}
	notifier := NewChannelNotifier()

	var (
		mu           sync.Mutex
		processCount = make(map[string]int)
		totalCount   atomic.Int32
	)

	handler := NewCommandHandler("concurrentHandler", func(ctx context.Context, tx Tx, cmd *testCmd) error {
		mu.Lock()
		processCount[cmd.CommandID()]++
		mu.Unlock()
		totalCount.Add(1)
		time.Sleep(10 * time.Millisecond) // slight delay to allow multiple workers to compete
		return nil
	})

	workerCfg := SQLQueueConfig{
		Concurrency:  3, // 3 concurrent worker goroutines using FOR UPDATE SKIP LOCKED
		PollInterval: 1 * time.Hour,
		Notifier:     notifier,
		Dialect:      DialectMySQL,
	}

	worker, err := NewSQLCommandQueueWorkerWithConfig(
		store,
		tm,
		marshaler,
		logger,
		[]any{handler},
		workerCfg,
		nil,
	)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, worker.Start(ctx))
	defer func() { _ = worker.Stop() }()

	bus := NewCommandBus(nil, store, marshaler, CommandBusConfig{
		UseSQLQueue: true,
		SQLQueue: SQLQueueConfig{
			Notifier: notifier,
		},
	})

	const numCommands = 15
	for i := 0; i < numCommands; i++ {
		cmd := &testCmd{BaseCommand: NewBaseCommand(), Data: "concurrent"}
		require.NoError(t, bus.Send(ctx, cmd))
	}

	// Wait for all 15 commands to be processed
	assert.Eventually(t, func() bool {
		return totalCount.Load() == int32(numCommands)
	}, 5*time.Second, 50*time.Millisecond, "all commands should be processed by concurrent workers")

	// Verify no double processing occurred (each command processed exactly once)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, numCommands, len(processCount))
	for cmdID, count := range processCount {
		assert.Equal(t, 1, count, "command %s was processed %d times (expected exactly 1)", cmdID, count)
	}
}

package cqrs

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	watermill_cqrs "github.com/ThreeDotsLabs/watermill/components/cqrs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func setupCommandQueueTestDB(t *testing.T) (*sql.DB, CommandExecutionStore, TransactionManager) {
	db, err := sql.Open("sqlite", "file::memory:?cache=shared")
	require.NoError(t, err)

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS command_executions (
			command_id VARCHAR(255) PRIMARY KEY,
			handler_name VARCHAR(255) NOT NULL DEFAULT '',
			command_name VARCHAR(255) NOT NULL DEFAULT '',
			payload BLOB,
			status VARCHAR(32) NOT NULL DEFAULT 'pending',
			error_data BLOB,
			started_at DATETIME NOT NULL,
			finished_at DATETIME
		)
	`)
	require.NoError(t, err)

	_, err = db.Exec("DELETE FROM command_executions")
	require.NoError(t, err)

	store := NewSQLCommandExecutionStore(db, "command_executions", DialectSQLite)
	tm := NewSQLTransactionManager(db)
	return db, store, tm
}

type testCmd struct {
	BaseCommand
	Data string `json:"data"`
}

func TestSQLCommandQueueWorker_ImmediateProcessingAfterSend(t *testing.T) {
	db, store, tm := setupCommandQueueTestDB(t)
	defer func() { _ = db.Close() }()

	logger := watermill.NopLogger{}
	marshaler := watermill_cqrs.JSONMarshaler{}
	notifier := NewChannelNotifier()

	processed := make(chan string, 1)
	handler := NewCommandHandler("testHandler", func(ctx context.Context, tx Tx, cmd *testCmd) error {
		processed <- cmd.Data
		return nil
	})

	workerCfg := SQLQueueConfig{
		PollInterval: 1 * time.Hour, // Very long fallback polling
		Notifier:     notifier,
	}

	worker, err := NewSQLCommandQueueWorkerWithConfig(store, tm, marshaler, logger, []any{handler}, workerCfg, nil)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = worker.Start(ctx)
	require.NoError(t, err)
	defer func() { _ = worker.Stop() }()

	busCfg := CommandBusConfig{
		UseSQLQueue: true,
		SQLQueue: SQLQueueConfig{
			Notifier: notifier,
		},
	}
	bus := NewCommandBus(nil, store, marshaler, busCfg)

	start := time.Now()
	cmd := &testCmd{BaseCommand: NewBaseCommand(), Data: "wake-up-test"}
	err = bus.Send(ctx, cmd)
	require.NoError(t, err)

	select {
	case data := <-processed:
		assert.Equal(t, "wake-up-test", data)
		elapsed := time.Since(start)
		assert.Less(t, elapsed, 1*time.Second, "processing latency should be sub-second (far less than PollInterval 1h)")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for command execution via wake-up")
	}

	exec, err := store.GetExecution(ctx, nil, cmd.CommandID())
	require.NoError(t, err)
	require.NotNil(t, exec)
	assert.Equal(t, CommandExecutionStatusSuccess, exec.Status)
}

func TestSQLCommandQueueWorker_RecoveryOnStartup(t *testing.T) {
	db, store, tm := setupCommandQueueTestDB(t)
	defer func() { _ = db.Close() }()

	logger := watermill.NopLogger{}
	marshaler := watermill_cqrs.JSONMarshaler{}

	// Pre-insert a pending command before the worker starts
	cmd := &testCmd{BaseCommand: NewBaseCommand(), Data: "recovery-test"}
	msg, err := marshaler.Marshal(cmd)
	require.NoError(t, err)

	err = store.RecordPending(context.Background(), nil, cmd.CommandID(), marshaler.Name(cmd), msg.Payload)
	require.NoError(t, err)

	processed := make(chan string, 1)
	handler := NewCommandHandler("testHandler", func(ctx context.Context, tx Tx, cmd *testCmd) error {
		processed <- cmd.Data
		return nil
	})

	workerCfg := SQLQueueConfig{
		PollInterval:  1 * time.Hour, // Polling disabled for test
		DisableWakeup: true,          // Wake-up disabled
	}

	worker, err := NewSQLCommandQueueWorkerWithConfig(store, tm, marshaler, logger, []any{handler}, workerCfg, nil)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Starting the worker should immediately drain existing pending commands via startup recovery
	err = worker.Start(ctx)
	require.NoError(t, err)
	defer func() { _ = worker.Stop() }()

	select {
	case data := <-processed:
		assert.Equal(t, "recovery-test", data)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for recovery drain on startup")
	}
}

func TestCommandBus_MaxPendingAndErrQueueFull(t *testing.T) {
	db, store, _ := setupCommandQueueTestDB(t)
	defer func() { _ = db.Close() }()

	marshaler := watermill_cqrs.JSONMarshaler{}
	busCfg := CommandBusConfig{
		UseSQLQueue: true,
		SQLQueue: SQLQueueConfig{
			MaxPending:           2,
			PendingCountCacheTTL: -1, // Disable cache for immediate DB reflection
		},
	}
	bus := NewCommandBus(nil, store, marshaler, busCfg)
	ctx := context.Background()

	cmd1 := &testCmd{BaseCommand: NewBaseCommand(), Data: "1"}
	cmd2 := &testCmd{BaseCommand: NewBaseCommand(), Data: "2"}
	cmd3 := &testCmd{BaseCommand: NewBaseCommand(), Data: "3"}

	require.NoError(t, bus.Send(ctx, cmd1))
	require.NoError(t, bus.Send(ctx, cmd2))

	// Third command should exceed MaxPending and return ErrQueueFull
	err := bus.Send(ctx, cmd3)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrQueueFull), "expected errors.Is(err, ErrQueueFull) to be true")

	// Test negative MaxPending allows unlimited
	unlimitedCfg := CommandBusConfig{
		UseSQLQueue: true,
		SQLQueue: SQLQueueConfig{
			MaxPending: -1,
		},
	}
	unlimitedBus := NewCommandBus(nil, store, marshaler, unlimitedCfg)
	require.NoError(t, unlimitedBus.Send(ctx, cmd3))
}

func TestCommandBus_PendingCountCache(t *testing.T) {
	db, store, _ := setupCommandQueueTestDB(t)
	defer func() { _ = db.Close() }()

	marshaler := watermill_cqrs.JSONMarshaler{}
	busCfg := CommandBusConfig{
		UseSQLQueue: true,
		SQLQueue: SQLQueueConfig{
			MaxPending:           1,
			PendingCountCacheTTL: 500 * time.Millisecond,
		},
	}
	bus := NewCommandBus(nil, store, marshaler, busCfg)
	ctx := context.Background()

	cmd1 := &testCmd{BaseCommand: NewBaseCommand(), Data: "1"}
	require.NoError(t, bus.Send(ctx, cmd1))

	// Queue is full now
	cmd2 := &testCmd{BaseCommand: NewBaseCommand(), Data: "2"}
	err := bus.Send(ctx, cmd2)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrQueueFull))

	// Clear DB directly
	_, err = db.Exec("DELETE FROM command_executions")
	require.NoError(t, err)

	// Still full due to TTL cache!
	err = bus.Send(ctx, cmd2)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrQueueFull))

	// Wait for cache TTL to expire
	time.Sleep(550 * time.Millisecond)

	// Now cache misses, DB is checked, Send succeeds
	require.NoError(t, bus.Send(ctx, cmd2))
}

func TestSQLCommandQueueWorker_SQLiteConcurrencyEnforcedToOne(t *testing.T) {
	db, store, tm := setupCommandQueueTestDB(t)
	defer func() { _ = db.Close() }()

	logger := watermill.NopLogger{}
	marshaler := watermill_cqrs.JSONMarshaler{}

	cfg := SQLQueueConfig{
		Concurrency: 5, // Requesting 5 workers
	}

	worker, err := NewSQLCommandQueueWorkerWithConfig(store, tm, marshaler, logger, nil, cfg, nil)
	require.NoError(t, err)

	// Because store is DialectSQLite, concurrency must be enforced to 1
	assert.Equal(t, 1, worker.config.Concurrency)
}

func TestSQLCommandQueueWorker_ErrorBackoff(t *testing.T) {
	// Custom store that returns error on GetNextPending
	errStore := &failingExecStore{err: errors.New("db connection failure")}
	tm := &mockTxManager{}
	logger := watermill.NopLogger{}
	marshaler := watermill_cqrs.JSONMarshaler{}

	cfg := SQLQueueConfig{
		PollInterval: 10 * time.Millisecond,
		ErrorBackoff: 50 * time.Millisecond,
	}

	worker, err := NewSQLCommandQueueWorkerWithConfig(errStore, tm, marshaler, logger, nil, cfg, nil)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, worker.Start(ctx))
	time.Sleep(120 * time.Millisecond)
	require.NoError(t, worker.Stop())

	// If there were a hot loop without backoff, call count would be huge (>1000)
	// With 50ms backoff, call count in 120ms should be less than 10
	assert.Less(t, errStore.callCount.Load(), int64(10), "ErrorBackoff should prevent hot-looping when DB fails")
}

func TestSQLCommandQueueWorker_GracefulShutdown(t *testing.T) {
	db, store, tm := setupCommandQueueTestDB(t)
	defer func() { _ = db.Close() }()

	logger := watermill.NopLogger{}
	marshaler := watermill_cqrs.JSONMarshaler{}
	notifier := NewChannelNotifier()

	inProgress := make(chan struct{})
	finished := make(chan struct{})

	handler := NewCommandHandler("slowHandler", func(ctx context.Context, tx Tx, cmd *testCmd) error {
		close(inProgress)
		time.Sleep(150 * time.Millisecond) // Simulate slow work
		close(finished)
		return nil
	})

	workerCfg := SQLQueueConfig{
		PollInterval: 1 * time.Hour,
		Notifier:     notifier,
	}
	worker, err := NewSQLCommandQueueWorkerWithConfig(store, tm, marshaler, logger, []any{handler}, workerCfg, nil)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, worker.Start(ctx))

	busCfg := CommandBusConfig{
		UseSQLQueue: true,
		SQLQueue: SQLQueueConfig{
			Notifier: notifier,
		},
	}
	bus := NewCommandBus(nil, store, marshaler, busCfg)
	cmd := &testCmd{BaseCommand: NewBaseCommand(), Data: "slow"}
	require.NoError(t, bus.Send(ctx, cmd))

	// Wait until command processing has started
	<-inProgress

	// Now initiate worker.Stop() while command is actively running
	stopDone := make(chan struct{})
	go func() {
		_ = worker.Stop()
		close(stopDone)
	}()

	select {
	case <-finished:
		// Command finished successfully
	case <-time.After(500 * time.Millisecond):
		t.Fatal("handler did not finish in time")
	}

	select {
	case <-stopDone:
		// Stop completed only after handler finished!
	case <-time.After(500 * time.Millisecond):
		t.Fatal("worker Stop() failed to return")
	}

	exec, err := store.GetExecution(ctx, nil, cmd.CommandID())
	require.NoError(t, err)
	require.NotNil(t, exec)
	assert.Equal(t, CommandExecutionStatusSuccess, exec.Status)
}

type failingExecStore struct {
	err       error
	callCount atomic.Int64
}

func (s *failingExecStore) RecordPending(context.Context, Tx, string, string, []byte) error {
	return s.err
}
func (s *failingExecStore) RecordStarted(context.Context, Tx, string, string) error {
	return s.err
}
func (s *failingExecStore) RecordSuccess(context.Context, Tx, string) error { return s.err }
func (s *failingExecStore) RecordFailure(context.Context, Tx, string, []byte) error {
	return s.err
}
func (s *failingExecStore) GetStatus(context.Context, Tx, string) (CommandExecutionStatus, error) {
	return "", s.err
}
func (s *failingExecStore) GetExecution(context.Context, Tx, string) (*CommandExecution, error) {
	return nil, s.err
}
func (s *failingExecStore) GetNextPending(context.Context, Tx) (*CommandExecution, error) {
	s.callCount.Add(1)
	return nil, s.err
}
func (s *failingExecStore) CountPending(context.Context, Tx) (int, error) {
	return 0, s.err
}

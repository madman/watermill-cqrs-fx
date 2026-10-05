package cqrs

import (
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
)

func TestChannelNotifier_CoalescingAndNonBlocking(t *testing.T) {
	n := NewChannelNotifier()

	// Calling Notify multiple times should not block and should coalesce into 1 signal
	for i := 0; i < 10; i++ {
		n.Notify()
	}

	// First receive should succeed immediately
	select {
	case <-n.C():
		// Received signal as expected
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected wake-up signal on notifier channel")
	}

	// Second receive should not have any pending signals (coalesced)
	select {
	case <-n.C():
		t.Fatal("unexpected second signal, calls should have coalesced")
	default:
		// Channel is empty as expected
	}

	// Calling Notify again works after channel is drained
	n.Notify()
	select {
	case <-n.C():
		// Signal received
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected signal after draining channel")
	}
}

func TestSQLQueueConfig_Normalize(t *testing.T) {
	// Zero values should get defaults
	cfg := SQLQueueConfig{}
	cfg.Normalize()

	assert.Equal(t, 30*time.Second, cfg.PollInterval)
	assert.Nil(t, cfg.Notifier)
	assert.False(t, cfg.DisableWakeup)
	assert.Equal(t, 1, cfg.Concurrency)
	assert.Equal(t, 1000, cfg.MaxPending)
	assert.Equal(t, 1*time.Second, cfg.PendingCountCacheTTL)
	assert.Equal(t, 1*time.Second, cfg.ErrorBackoff)

	// Negative values should be preserved (meaning disabled)
	negCfg := SQLQueueConfig{
		PollInterval:         -1,
		Concurrency:          0, // Concurrency 0 becomes 1
		MaxPending:           -1,
		PendingCountCacheTTL: -1,
		ErrorBackoff:         -1,
	}
	negCfg.Normalize()

	assert.Equal(t, time.Duration(-1), negCfg.PollInterval)
	assert.Equal(t, 1, negCfg.Concurrency)
	assert.Equal(t, -1, negCfg.MaxPending)
	assert.Equal(t, time.Duration(-1), negCfg.PendingCountCacheTTL)
	assert.Equal(t, time.Duration(-1), negCfg.ErrorBackoff)
}

func TestSQLOutboxWorkerConfig_Normalize(t *testing.T) {
	// Zero values should get defaults
	cfg := SQLOutboxWorkerConfig{}
	cfg.Normalize()

	assert.Equal(t, "events", cfg.TableName)
	assert.Equal(t, 30*time.Second, cfg.PollInterval)
	assert.Equal(t, 50, cfg.BatchSize)
	assert.Nil(t, cfg.Notifier)
	assert.Equal(t, 1*time.Second, cfg.ErrorBackoff)

	// Negative values should be preserved
	negCfg := SQLOutboxWorkerConfig{
		TableName:    "custom_events",
		PollInterval: -1,
		BatchSize:    -1, // BatchSize <= 0 becomes 50
		ErrorBackoff: -1,
	}
	negCfg.Normalize()

	assert.Equal(t, "custom_events", negCfg.TableName)
	assert.Equal(t, time.Duration(-1), negCfg.PollInterval)
	assert.Equal(t, 50, negCfg.BatchSize)
	assert.Equal(t, time.Duration(-1), negCfg.ErrorBackoff)
}

func TestFx_EventBusUsesOutboxConfigNotifier(t *testing.T) {
	customNotifier := NewChannelNotifier()

	var eb EventBus
	app := fxtest.New(
		t,
		fx.Provide(
			func() message.Publisher { return newMemoryPublisher() },
			func() watermill.LoggerAdapter { return watermill.NopLogger{} },
			func() SQLOutboxWorkerConfig {
				return SQLOutboxWorkerConfig{
					Notifier: customNotifier,
				}
			},
		),
		Module,
		fx.Populate(&eb),
	)
	app.RequireStart()
	defer app.RequireStop()

	require.NotNil(t, eb)
	busImpl, ok := eb.(*eventBus)
	require.True(t, ok)
	assert.Equal(t, customNotifier, busImpl.outboxNotifier)
}

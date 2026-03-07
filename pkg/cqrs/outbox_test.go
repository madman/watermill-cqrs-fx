package cqrs

import (
	"context"
	"testing"

	"github.com/ThreeDotsLabs/watermill/components/cqrs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

type mockOutbox struct {
	mock.Mock
}

func (m *mockOutbox) Save(ctx context.Context, tx Tx, records ...OutboxRecord) error {
	args := m.Called(ctx, tx, records)
	return args.Error(0)
}

type mockTxManager struct {
	mock.Mock
}

func (m *mockTxManager) WithinTransaction(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error {
	return fn(ctx, nil)
}

type mockTx struct {
	Tx
}

type mockHandler struct {
	mock.Mock
}

func (m *mockHandler) HandlerName() string { return "mockHandler" }
func (m *mockHandler) NewCommand() any     { return &mockCommand{} }

type mockCommand struct {
	BaseCommand
}

func (m *mockHandler) Handle(ctx context.Context, tx Tx, cmd any) error {
	args := m.Called(ctx, tx, cmd)
	return args.Error(0)
}

func TestTransactionalCommandHandler(t *testing.T) {
	tm := &mockTxManager{}
	handler := &mockHandler{}

	wrapper := &transactionalCommandHandler{
		next:      handler,
		txManager: tm,
	}

	handler.On("Handle", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	err := wrapper.Handle(context.Background(), &mockCommand{BaseCommand: NewBaseCommand()})
	assert.NoError(t, err)
	handler.AssertExpectations(t)
}

func TestTransactionalEventBus(t *testing.T) {
	outbox := &mockOutbox{}
	marshaler := cqrs.JSONMarshaler{}

	// We need a real cqrs.EventBus to satisfy the struct field,
	// even if we don't use it in the transactional path.
	// However, creating a real one is complex due to dependencies.
	// For this test, we'll focus on the logic that branches to outbox.

	bus := &eventBus{
		bus:       nil, // Will panic if called, which is what we want to verify (it should NOT be called)
		marshaler: marshaler,
		outbox:    outbox,
	}

	tx := &mockTx{}
	event := struct{ Name string }{"TestEvent"}

	outbox.On("Save", context.Background(), tx, mock.MatchedBy(func(records []OutboxRecord) bool {
		return len(records) == 1 && records[0].Topic == "struct { Name string }"
	})).Return(nil)

	err := bus.Publish(context.Background(), tx, event)
	assert.NoError(t, err)
	outbox.AssertExpectations(t)
}

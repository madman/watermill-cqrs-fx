package cqrs

import (
	"context"
	"errors"
	"testing"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

type retryMockCommandHandler struct {
	mock.Mock
}

func (m *retryMockCommandHandler) HandlerName() string { return "RetryMockHandler" }
func (m *retryMockCommandHandler) NewCommand() any     { return &retryMockCommand{} }
func (m *retryMockCommandHandler) Handle(ctx context.Context, tx Tx, cmd any) error {
	args := m.Called(ctx, tx, cmd)
	return args.Error(0)
}

type retryMockCommand struct {
	BaseCommand
}

type retryMockTxManager struct{}
func (m *retryMockTxManager) WithinTransaction(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error {
	return fn(ctx, &retryMockTx{})
}

type retryMockTx struct {
	Tx
}

type retryMockExecStore struct {
	mock.Mock
}

func (m *retryMockExecStore) RecordStarted(ctx context.Context, tx Tx, commandID string, handlerName string) error {
	return m.Called(ctx, tx, commandID, handlerName).Error(0)
}
func (m *retryMockExecStore) RecordSuccess(ctx context.Context, tx Tx, commandID string) error {
	return m.Called(ctx, tx, commandID).Error(0)
}
func (m *retryMockExecStore) RecordFailure(ctx context.Context, tx Tx, commandID string, errorData []byte) error {
	return m.Called(ctx, tx, commandID, errorData).Error(0)
}
func (m *retryMockExecStore) GetStatus(ctx context.Context, tx Tx, commandID string) (CommandExecutionStatus, error) {
	args := m.Called(ctx, tx, commandID)
	return args.Get(0).(CommandExecutionStatus), args.Error(1)
}
func (m *retryMockExecStore) GetExecution(ctx context.Context, tx Tx, commandID string) (*CommandExecution, error) {
	args := m.Called(ctx, tx, commandID)
	return args.Get(0).(*CommandExecution), args.Error(1)
}

func TestTransactionalCommandHandler_RetryPrevention(t *testing.T) {
	logger := watermill.NopLogger{}
	txManager := &retryMockTxManager{}
	
	t.Run("returns nil and stops retry when handler fails and failure is recorded", func(t *testing.T) {
		execStore := &retryMockExecStore{}
		nextHandler := &retryMockCommandHandler{}
		
		h := &transactionalCommandHandler{
			next:      nextHandler,
			txManager: txManager,
			execStore: execStore,
			logger:    logger,
		}
		
		cmd := &retryMockCommand{BaseCommand: BaseCommand{ID: "cmd-1"}}
		domainErr := errors.New("domain error")
		
		execStore.On("GetStatus", mock.Anything, mock.Anything, "cmd-1").Return(CommandExecutionStatus(""), nil)
		execStore.On("RecordStarted", mock.Anything, mock.Anything, "cmd-1", "RetryMockHandler").Return(nil)
		nextHandler.On("Handle", mock.Anything, mock.Anything, cmd).Return(domainErr)
		execStore.On("RecordFailure", mock.Anything, mock.Anything, "cmd-1", mock.Anything).Return(nil)
		
		err := h.Handle(context.Background(), cmd)
		
		assert.NoError(t, err, "Expected nil error to stop retries")
		execStore.AssertExpectations(t)
		nextHandler.AssertExpectations(t)
	})

	t.Run("returns nil immediately if command already failed", func(t *testing.T) {
		execStore := &retryMockExecStore{}
		nextHandler := &retryMockCommandHandler{}
		
		h := &transactionalCommandHandler{
			next:      nextHandler,
			txManager: txManager,
			execStore: execStore,
			logger:    logger,
		}
		
		cmd := &retryMockCommand{BaseCommand: BaseCommand{ID: "cmd-2"}}
		
		execStore.On("GetStatus", mock.Anything, mock.Anything, "cmd-2").Return(CommandExecutionStatusFailed, nil)
		
		err := h.Handle(context.Background(), cmd)
		
		assert.NoError(t, err)
		nextHandler.AssertNotCalled(t, "Handle", mock.Anything, mock.Anything, mock.Anything)
	})
}

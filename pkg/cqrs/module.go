package cqrs

import (
	"context"
	"errors"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/components/cqrs"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/madman/watermill-cqrs-fx/pkg/cqrs/cmderr"
	"go.uber.org/fx"
)

var Module = fx.Module("cqrs",
	fx.Provide(
		// Provide the default marshaler
		func() cqrs.CommandEventMarshaler {
			return cqrs.JSONMarshaler{}
		},
		// Provide watermill's CommandBus
		func(params struct {
			fx.In
			Publisher message.Publisher
			Marshaler cqrs.CommandEventMarshaler
			Logger    watermill.LoggerAdapter
		}) (*cqrs.CommandBus, error) {
			return cqrs.NewCommandBusWithConfig(params.Publisher, cqrs.CommandBusConfig{
				GeneratePublishTopic: func(params cqrs.CommandBusGeneratePublishTopicParams) (string, error) {
					return params.CommandName, nil
				},
				Marshaler: params.Marshaler,
				Logger:    params.Logger,
			})
		},
		// Provide watermill's EventBus
		func(params struct {
			fx.In
			Publisher message.Publisher
			Marshaler cqrs.CommandEventMarshaler
			Logger    watermill.LoggerAdapter
		}) (*cqrs.EventBus, error) {
			return cqrs.NewEventBusWithConfig(params.Publisher, cqrs.EventBusConfig{
				GeneratePublishTopic: func(params cqrs.GenerateEventPublishTopicParams) (string, error) {
					return params.EventName, nil
				},
				Marshaler: params.Marshaler,
				Logger:    params.Logger,
			})
		},
		// Provide our wrapper buses
		func(params struct {
			fx.In
			Bus       *cqrs.CommandBus
			ExecStore CommandExecutionStore `optional:"true"`
			Config    CommandBusConfig      `optional:"true"`
		}) CommandBus {
			return NewCommandBus(params.Bus, params.ExecStore, params.Config)
		},
		NewEventBus,
		// Provide QueryBus
		func(params struct {
			fx.In
			Handlers []any `group:"query_handlers"`
		}) (QueryBus, error) {
			return NewQueryBus(params.Handlers)
		},
		// Provide Processors
		NewCommandProcessor,
		NewEventProcessor,
	),
)

type ProcessorParams struct {
	fx.In

	Logger     watermill.LoggerAdapter
	Subscriber message.Subscriber
	Marshaler  cqrs.CommandEventMarshaler
	Router     *message.Router

	CommandHandlers []any `group:"command_handlers"`
	EventHandlers   []any `group:"event_handlers"`

	CommandBus *cqrs.CommandBus
	EventBus   *cqrs.EventBus

	TxManager TransactionManager    `optional:"true"`
	Outbox    Outbox                `optional:"true"`
	ExecStore CommandExecutionStore `optional:"true"`
}

func NewCommandProcessor(params ProcessorParams) (*cqrs.CommandProcessor, error) {
	handlers := make([]cqrs.CommandHandler, 0, len(params.CommandHandlers))
	for _, h := range params.CommandHandlers {
		if ch, ok := h.(CommandHandler); ok {
			if params.TxManager != nil {
				handlers = append(handlers, &transactionalCommandHandler{
					next:      ch,
					txManager: params.TxManager,
					execStore: params.ExecStore,
				})
			} else {
				// If no TxManager, we still need to satisfy Watermill's interface.
				// We can either fail or pass nil Tx.
				handlers = append(handlers, &handlerWrapper{next: ch})
			}
		} else if ch, ok := h.(cqrs.CommandHandler); ok {
			// Legacy support for Watermill's interface (if any)
			handlers = append(handlers, ch)
		}
	}

	config := cqrs.CommandProcessorConfig{
		GenerateSubscribeTopic: func(p cqrs.CommandProcessorGenerateSubscribeTopicParams) (string, error) {
			return p.CommandName, nil
		},
		SubscriberConstructor: func(p cqrs.CommandProcessorSubscriberConstructorParams) (message.Subscriber, error) {
			return params.Subscriber, nil
		},
		Marshaler: params.Marshaler,
		Logger:    params.Logger,
	}
	cp, err := cqrs.NewCommandProcessorWithConfig(params.Router, config)
	if err != nil {
		return nil, err
	}

	if err := cp.AddHandlers(handlers...); err != nil {
		return nil, err
	}

	return cp, nil
}

type transactionalCommandHandler struct {
	next      CommandHandler
	txManager TransactionManager
	execStore CommandExecutionStore
}

func (h *transactionalCommandHandler) HandlerName() string {
	return h.next.HandlerName()
}

func (h *transactionalCommandHandler) NewCommand() interface{} {
	return h.next.NewCommand()
}

func (h *transactionalCommandHandler) Handle(ctx context.Context, cmd interface{}) error {
	cc, ok := cmd.(Command)
	if !ok {
		// Fallback for non-Command types (legacy or unexpected)
		return h.txManager.WithinTransaction(ctx, func(ctx context.Context, tx Tx) error {
			return h.next.Handle(ctx, tx, cmd)
		})
	}

	return h.txManager.WithinTransaction(ctx, func(ctx context.Context, tx Tx) error {
		if h.execStore != nil {
			status, err := h.execStore.GetStatus(ctx, tx, cc.CommandID())
			if err != nil {
				return err
			}

			if status == CommandExecutionStatusSuccess {
				// Command already processed successfully, skip
				return nil
			}

			if err := h.execStore.RecordStarted(ctx, tx, cc.CommandID(), h.next.HandlerName()); err != nil {
				return err
			}
		}

		err := h.next.Handle(ctx, tx, cmd)
		if err != nil {
			if h.execStore != nil {
				var ce *cmderr.CommandError
				if !errors.As(err, &ce) {
					ce = cmderr.Wrap("COMMAND_FAILED", err, "execution error")
				}
				data, _ := ce.Encode()
				if recErr := h.execStore.RecordFailure(ctx, tx, cc.CommandID(), data); recErr != nil {
					return errors.Join(err, recErr)
				}
			}
			return err
		}

		if h.execStore != nil {
			if err := h.execStore.RecordSuccess(ctx, tx, cc.CommandID()); err != nil {
				return err
			}
		}

		return nil
	})
}

type handlerWrapper struct {
	next CommandHandler
}

func (h *handlerWrapper) HandlerName() string {
	return h.next.HandlerName()
}

func (h *handlerWrapper) NewCommand() interface{} {
	return h.next.NewCommand()
}

func (h *handlerWrapper) Handle(ctx context.Context, cmd interface{}) error {
	return h.next.Handle(ctx, nil, cmd)
}

func NewEventProcessor(params ProcessorParams) (*cqrs.EventProcessor, error) {
	handlers := make([]cqrs.EventHandler, 0, len(params.EventHandlers))
	for _, h := range params.EventHandlers {
		if eh, ok := h.(cqrs.EventHandler); ok {
			handlers = append(handlers, eh)
		}
	}

	ep, err := cqrs.NewEventProcessorWithConfig(params.Router, cqrs.EventProcessorConfig{
		GenerateSubscribeTopic: func(p cqrs.EventProcessorGenerateSubscribeTopicParams) (string, error) {
			return p.EventName, nil
		},
		SubscriberConstructor: func(p cqrs.EventProcessorSubscriberConstructorParams) (message.Subscriber, error) {
			return params.Subscriber, nil
		},
		Marshaler: params.Marshaler,
		Logger:    params.Logger,
	})
	if err != nil {
		return nil, err
	}

	if err := ep.AddHandlers(handlers...); err != nil {
		return nil, err
	}

	return ep, nil
}

// Utility functions for registering handlers in Fx
func AsCommandHandler(handler any) any {
	return fx.Annotate(
		handler,
		fx.As(new(any)),
		fx.ResultTags(`group:"command_handlers"`),
	)
}

func AsEventHandler(handler any) any {
	return fx.Annotate(
		handler,
		fx.As(new(any)),
		fx.ResultTags(`group:"event_handlers"`),
	)
}

func AsQueryHandler(handler any) any {
	return fx.Annotate(
		handler,
		fx.As(new(any)),
		fx.ResultTags(`group:"query_handlers"`),
	)
}

// HandlerOut structures for fx.Out registration
type CommandHandlerOut struct {
	fx.Out
	Handler any `group:"command_handlers"`
}

type EventHandlerOut struct {
	fx.Out
	Handler any `group:"event_handlers"`
}

type QueryHandlerOut struct {
	fx.Out
	Handler any `group:"query_handlers"`
}

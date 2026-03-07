package cqrs

import (
	"context"
	"time"

	"github.com/ThreeDotsLabs/watermill/components/cqrs"
	"github.com/google/uuid"
)

type eventBus struct {
	bus       *cqrs.EventBus
	marshaler cqrs.CommandEventMarshaler
	outbox    Outbox
}

func NewEventBus(bus *cqrs.EventBus, marshaler cqrs.CommandEventMarshaler, outbox Outbox) EventBus {
	return &eventBus{
		bus:       bus,
		marshaler: marshaler,
		outbox:    outbox,
	}
}

func (b *eventBus) Publish(ctx context.Context, tx Tx, events ...Event) error {
	if tx != nil && b.outbox != nil {
		records := make([]OutboxRecord, 0, len(events))
		for _, event := range events {
			eventName := b.marshaler.Name(event)
			msg, err := b.marshaler.Marshal(event)
			if err != nil {
				return err
			}

			records = append(records, OutboxRecord{
				ID:         uuid.NewString(),
				Topic:      eventName, // Using event name as topic by default
				Payload:    msg.Payload,
				Metadata:   map[string]string{"name": eventName},
				OccurredAt: time.Now(),
			})
		}
		return b.outbox.Save(ctx, tx, records...)
	}

	for _, event := range events {
		if err := b.bus.Publish(ctx, event); err != nil {
			return err
		}
	}
	return nil
}

// NewEventHandler creates a new Watermill EventHandler from a function with a specific event type.
func NewEventHandler[E any](name string, handler func(ctx context.Context, event *E) error) cqrs.EventHandler {
	return &genericEventHandler[E]{
		name:    name,
		handler: handler,
	}
}

type genericEventHandler[E any] struct {
	name    string
	handler func(ctx context.Context, event *E) error
}

func (h *genericEventHandler[E]) HandlerName() string {
	return h.name
}

func (h *genericEventHandler[E]) NewEvent() any {
	return new(E)
}

func (h *genericEventHandler[E]) Handle(ctx context.Context, event any) error {
	return h.handler(ctx, event.(*E))
}

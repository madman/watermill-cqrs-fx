package cqrs

import (
	"context"
	"fmt"
	"reflect"
)

type QueryHandler[Q Query, R any] interface {
	Handle(ctx context.Context, query Q) (R, error)
}

type queryBus struct {
	handlers map[reflect.Type]any
}

func NewQueryBus(handlers []any) (QueryBus, error) {
	b := &queryBus{
		handlers: make(map[reflect.Type]any),
	}

	for _, handler := range handlers {
		if err := b.registerHandler(handler); err != nil {
			return nil, err
		}
	}

	return b, nil
}

func (b *queryBus) registerHandler(handler any) error {
	handlerType := reflect.TypeOf(handler)

	// We are looking for a method Handle(ctx, Query) (Result, error)
	method, ok := handlerType.MethodByName("Handle")
	if !ok {
		return fmt.Errorf("handler does not have Handle method")
	}

	if method.Type.NumIn() != 3 { // receiver, ctx, query
		return fmt.Errorf("handler Handle method must have 2 arguments (ctx, query)")
	}

	queryType := method.Type.In(2)
	b.handlers[queryType] = handler

	return nil
}

func (b *queryBus) Execute(ctx context.Context, query Query, result any) error {
	queryType := reflect.TypeOf(query)
	handler, ok := b.handlers[queryType]
	if !ok {
		return fmt.Errorf("no handler for query %s", queryType)
	}

	handlerVal := reflect.ValueOf(handler)
	method := handlerVal.MethodByName("Handle")

	args := []reflect.Value{
		reflect.ValueOf(ctx),
		reflect.ValueOf(query),
	}

	results := method.Call(args)

	if !results[1].IsNil() {
		return results[1].Interface().(error)
	}

	// copy result[0] to result pointer
	resVal := reflect.ValueOf(result)
	if resVal.Kind() != reflect.Ptr {
		return fmt.Errorf("result must be a pointer")
	}

	resVal.Elem().Set(results[0])

	return nil
}

// NewQueryHandler creates a new QueryHandler from a function with specific query and result types.
func NewQueryHandler[Q Query, R any](handler func(ctx context.Context, query Q) (R, error)) QueryHandler[Q, R] {
	return &genericQueryHandler[Q, R]{
		handler: handler,
	}
}

type genericQueryHandler[Q Query, R any] struct {
	handler func(ctx context.Context, query Q) (R, error)
}

func (h *genericQueryHandler[Q, R]) Handle(ctx context.Context, query Q) (R, error) {
	return h.handler(ctx, query)
}

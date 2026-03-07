package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	wcqrs "github.com/madman/watermill-cqrs-fx/pkg/cqrs"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/components/cqrs"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ThreeDotsLabs/watermill/pubsub/gochannel"
	"go.uber.org/fx"
)

// --- Domain ---

type RegisterUser struct {
	wcqrs.BaseCommand
	Email string
}

type UserRegistered struct {
	Email string
}

// --- In-Memory Outbox Implementation ---

type memoryOutbox struct {
	mu      sync.Mutex
	records []wcqrs.OutboxRecord
}

func (m *memoryOutbox) Save(ctx context.Context, tx wcqrs.Tx, records ...wcqrs.OutboxRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i := range records {
		fmt.Printf("[Outbox] Saving event to database: %s\n", records[i].Topic)
		m.records = append(m.records, records[i])
	}
	return nil
}

type memoryTxManager struct{}

func (m *memoryTxManager) WithinTransaction(ctx context.Context, fn func(ctx context.Context, tx wcqrs.Tx) error) error {
	fmt.Println("[Tx] Starting database transaction...")
	err := fn(ctx, nil) // passing nil for in-memory example
	if err != nil {
		fmt.Printf("[Tx] Transaction rolled back: %v\n", err)
		return err
	}
	fmt.Println("[Tx] Transaction committed.")
	return nil
}

// --- Handlers ---

type RegistrationHandler struct {
	eb wcqrs.EventBus
}

func (h *RegistrationHandler) Handle(ctx context.Context, tx wcqrs.Tx, cmd *RegisterUser) error {
	fmt.Printf(">>> Processing registration for: %s\n", cmd.Email)
	return h.eb.Publish(ctx, tx, UserRegistered{Email: cmd.Email})
}

func NewRegistrationHandler(eb wcqrs.EventBus) *RegistrationHandler {
	return &RegistrationHandler{eb: eb}
}

type RegistrationWorker struct{}

func (h *RegistrationWorker) Handle(ctx context.Context, event *UserRegistered) error {
	fmt.Printf(">>> [Worker] Finalizing registration for: %s\n", event.Email)
	return nil
}

func NewRegistrationWorker() *RegistrationWorker {
	return &RegistrationWorker{}
}

func main() {
	app := fx.New(
		wcqrs.Module,
		fx.Provide(
			func() watermill.LoggerAdapter {
				return watermill.NewStdLogger(false, false)
			},
			func(logger watermill.LoggerAdapter) (message.Publisher, message.Subscriber) {
				pubSub := gochannel.NewGoChannel(gochannel.Config{}, logger)
				return pubSub, pubSub
			},
			func(logger watermill.LoggerAdapter) (*message.Router, error) {
				return message.NewRouter(message.RouterConfig{}, logger)
			},
			// Outbox & TxManager implementation
			func() wcqrs.Outbox { return &memoryOutbox{} },
			func() wcqrs.TransactionManager { return &memoryTxManager{} },

			// Register handlers
			NewRegistrationHandler,
			func(h *RegistrationHandler) wcqrs.CommandHandlerOut {
				return wcqrs.CommandHandlerOut{
					Handler: wcqrs.NewCommandHandler("RegistrationHandler", h.Handle),
				}
			},
			NewRegistrationWorker,
			func(h *RegistrationWorker) wcqrs.EventHandlerOut {
				return wcqrs.EventHandlerOut{
					Handler: wcqrs.NewEventHandler("RegistrationWorker", h.Handle),
				}
			},
		),
		fx.Invoke(func(lc fx.Lifecycle, router *message.Router, cp *cqrs.CommandProcessor, ep *cqrs.EventProcessor, cb wcqrs.CommandBus) {
			lc.Append(fx.Hook{
				OnStart: func(ctx context.Context) error {
					go func() {
						if err := router.Run(context.Background()); err != nil {
							fmt.Printf("Router error: %v\n", err)
						}
					}()

					go func() {
						<-router.Running()
						time.Sleep(100 * time.Millisecond)

						fmt.Println("--- Sending command ---")
						err := cb.Send(context.Background(), RegisterUser{
							BaseCommand: wcqrs.NewBaseCommand(),
							Email:       "user@example.com",
						})
						if err != nil {
							fmt.Printf("Failed to send command: %v\n", err)
						}
					}()

					return nil
				},
				OnStop: func(ctx context.Context) error {
					return router.Close()
				},
			})
		}),
	)

	app.Run()
}

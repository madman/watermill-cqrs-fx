package main

import (
	"context"
	"fmt"

	wcqrs "github.com/madman/watermill-cqrs-fx/pkg/cqrs"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/components/cqrs"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ThreeDotsLabs/watermill/pubsub/gochannel"
	"go.uber.org/fx"
)

// --- Domain ---

type CreateUser struct {
	wcqrs.BaseCommand
	Name string
}

type UserCreated struct {
	ID   string
	Name string
}

type GetUser struct {
	ID string
}

type User struct {
	ID   string
	Name string
}

// --- Handlers ---

type UserHandler struct {
	eb wcqrs.EventBus
}

func (h *UserHandler) Handle(ctx context.Context, tx wcqrs.Tx, cmd *CreateUser) error {
	fmt.Printf(">>> Received CreateUser command: %s\n", cmd.Name)
	return h.eb.Publish(ctx, tx, UserCreated{ID: "123", Name: cmd.Name})
}

func NewUserHandler(eb wcqrs.EventBus) *UserHandler {
	return &UserHandler{eb: eb}
}

type UserEventHandler struct{}

func (h *UserEventHandler) Handle(ctx context.Context, event *UserCreated) error {
	fmt.Printf(">>> Received UserCreated event: %s (ID: %s)\n", event.Name, event.ID)
	return nil
}

func NewUserEventHandler() *UserEventHandler {
	return &UserEventHandler{}
}

type UserQueryHandler struct{}

func (h *UserQueryHandler) Handle(ctx context.Context, query GetUser) (User, error) {
	fmt.Printf(">>> Received GetUser query: %s\n", query.ID)
	return User{ID: query.ID, Name: "John Doe"}, nil
}

func NewUserQueryHandler() *UserQueryHandler {
	return &UserQueryHandler{}
}

func main() {
	app := fx.New(
		wcqrs.Module,
		fx.Provide(
			func() (watermill.LoggerAdapter, error) {
				return watermill.NewStdLogger(false, false), nil
			},
			func(logger watermill.LoggerAdapter) (message.Publisher, message.Subscriber) {
				pubSub := gochannel.NewGoChannel(gochannel.Config{}, logger)
				return pubSub, pubSub
			},
			func(logger watermill.LoggerAdapter) (*message.Router, error) {
				return message.NewRouter(message.RouterConfig{}, logger)
			},
			// Registration of handlers
			NewUserHandler,
			func(h *UserHandler) wcqrs.CommandHandlerOut {
				return wcqrs.CommandHandlerOut{
					Handler: wcqrs.NewCommandHandler("UserHandler", h.Handle),
				}
			},
			NewUserEventHandler,
			func(h *UserEventHandler) wcqrs.EventHandlerOut {
				return wcqrs.EventHandlerOut{
					Handler: wcqrs.NewEventHandler("UserEventHandler", h.Handle),
				}
			},
			NewUserQueryHandler,
			func(h *UserQueryHandler) wcqrs.QueryHandlerOut {
				return wcqrs.QueryHandlerOut{
					Handler: wcqrs.NewQueryHandler(h.Handle),
				}
			},
		),
		fx.Invoke(func(lc fx.Lifecycle, cp *cqrs.CommandProcessor, ep *cqrs.EventProcessor, router *message.Router, cb wcqrs.CommandBus, qb wcqrs.QueryBus) {
			lc.Append(fx.Hook{
				OnStart: func(ctx context.Context) error {
					fmt.Println("Starting application...")
					go func() {
						if err := router.Run(context.Background()); err != nil {
							fmt.Printf("Router error: %v\n", err)
						}
					}()

					// Give some time for router to start
					go func() {
						<-router.Running()
						fmt.Println("Router is running.")

						// Execute command
						err := cb.Send(context.Background(), CreateUser{
							BaseCommand: wcqrs.NewBaseCommand(),
							Name:        "Alice",
						})
						if err != nil {
							fmt.Printf("Failed to send command: %v\n", err)
						}

						// Execute query
						var user User
						err = qb.Execute(context.Background(), GetUser{ID: "123"}, &user)
						if err != nil {
							fmt.Printf("Failed to execute query: %v\n", err)
						} else {
							fmt.Printf("Query result: %+v\n", user)
						}
					}()

					return nil
				},
				OnStop: func(ctx context.Context) error {
					fmt.Println("Stopping application...")
					return router.Close()
				},
			})
		}),
	)

	app.Run()
}

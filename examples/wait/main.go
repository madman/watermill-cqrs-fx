package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	wcqrs "github.com/madman/watermill-cqrs-fx/pkg/cqrs"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/components/cqrs"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ThreeDotsLabs/watermill/pubsub/gochannel"
	"go.uber.org/fx"
	_ "modernc.org/sqlite"
)

// --- Domain ---

type CreateUserWait struct {
	wcqrs.BaseCommand
	Name string
}

type UserCreatedWait struct {
	ID   string
	Name string
}

// --- Handlers ---

type UserHandlerWait struct {
	eb wcqrs.EventBus
}

func (h *UserHandlerWait) Handle(ctx context.Context, tx wcqrs.Tx, cmd *CreateUserWait) error {
	log.Printf(">>> Handler: Processing CreateUser command: %s (ID: %s)\n", cmd.Name, cmd.CommandID())

	// Simulate some work
	time.Sleep(1 * time.Second)

	if cmd.Name == "fail" {
		return fmt.Errorf("simulated failure")
	}

	return h.eb.Publish(ctx, tx, UserCreatedWait{ID: "123", Name: cmd.Name})
}

func NewUserHandlerWait(eb wcqrs.EventBus) *UserHandlerWait {
	return &UserHandlerWait{eb: eb}
}

func main() {
	dbPath := filepath.Join(os.TempDir(), "wait.db")
	// Cleanup old DB
	os.Remove(dbPath)

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
			// DB and Execution Store
			func() (*sql.DB, error) {
				db, err := sql.Open("sqlite", dbPath)
				if err != nil {
					return nil, err
				}

				_, err = db.Exec(`
					CREATE TABLE IF NOT EXISTS command_executions (
						command_id TEXT PRIMARY KEY,
						handler_name TEXT NOT NULL,
						status TEXT NOT NULL,
						error_data BLOB,
						started_at DATETIME NOT NULL,
						finished_at DATETIME
					)
				`)
				return db, err
			},
			func(db *sql.DB) wcqrs.CommandExecutionStore {
				return wcqrs.NewSQLCommandExecutionStore(db, "command_executions")
			},
			// Registration of handlers
			NewUserHandlerWait,
			func(h *UserHandlerWait) wcqrs.CommandHandlerOut {
				return wcqrs.CommandHandlerOut{
					Handler: wcqrs.NewCommandHandler("UserHandlerWait", h.Handle),
				}
			},
		),
		fx.Invoke(func(lc fx.Lifecycle, cp *cqrs.CommandProcessor, router *message.Router, cb wcqrs.CommandBus) {
			lc.Append(fx.Hook{
				OnStart: func(ctx context.Context) error {
					go func() {
						if err := router.Run(context.Background()); err != nil {
							log.Printf("Router error: %v\n", err)
						}
					}()

					go func() {
						<-router.Running()
						log.Println("Router is running. Sending command...")

						cmd := CreateUserWait{
							BaseCommand: wcqrs.NewBaseCommand(),
							Name:        "Alice",
						}

						if err := cb.Send(context.Background(), cmd); err != nil {
							log.Printf("Failed to send command: %v\n", err)
							return
						}

						log.Printf("Command sent (ID: %s). Waiting for status...\n", cmd.CommandID())

						exec, err := cb.Wait(context.Background(), cmd.CommandID(), 5*time.Second)
						if err != nil {
							log.Printf("Wait error: %v\n", err)
						} else {
							log.Printf("Command finished with status: %s\n", exec.Status)
						}

						// Test failure
						log.Println("\nSending failing command...")
						cmdFail := CreateUserWait{
							BaseCommand: wcqrs.NewBaseCommand(),
							Name:        "fail",
						}
						cb.Send(context.Background(), cmdFail)

						exec, err = cb.Wait(context.Background(), cmdFail.CommandID(), 5*time.Second)
						if err != nil {
							log.Printf("Wait finished with expected error: %v\n", err)
						} else {
							log.Printf("Command finished with status: %s\n", exec.Status)
						}

						// Exit
						time.Sleep(1 * time.Second)
						os.Exit(0)
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

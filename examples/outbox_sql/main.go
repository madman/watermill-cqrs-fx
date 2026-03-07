package main

import (
	"context"
	"database/sql"
	"fmt"
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

type RegisterUser struct {
	wcqrs.BaseCommand
	Email string
}

type UserRegistered struct {
	Email string
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
	dbPath := filepath.Join(os.TempDir(), "outbox.db")
	// Cleanup old DB
	os.Remove(dbPath)

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
			// SQL Implementation
			func() (*sql.DB, error) {
				db, err := sql.Open("sqlite", dbPath)
				if err != nil {
					return nil, err
				}
				_, err = db.Exec(`
					CREATE TABLE IF NOT EXISTS events (
						id TEXT PRIMARY KEY,
						topic TEXT NOT NULL,
						payload BLOB NOT NULL,
						metadata TEXT NOT NULL,
						occurred_at DATETIME NOT NULL
					);
					CREATE TABLE IF NOT EXISTS command_executions (
						command_id TEXT PRIMARY KEY,
						handler_name TEXT NOT NULL,
						status TEXT NOT NULL,
						error TEXT,
						error_code TEXT,
						error_data BLOB,
						started_at DATETIME NOT NULL,
						finished_at DATETIME
					);
				`)
				return db, err
			},
			func(db *sql.DB) wcqrs.Outbox {
				return wcqrs.NewSQLOutbox(db, "events")
			},
			func(db *sql.DB) wcqrs.TransactionManager {
				return wcqrs.NewSQLTransactionManager(db)
			},
			func(db *sql.DB) wcqrs.CommandExecutionStore {
				return wcqrs.NewSQLCommandExecutionStore(db, "command_executions")
			},

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
						time.Sleep(200 * time.Millisecond)

						fmt.Println("--- Sending command (SQL Outbox Example) ---")
						err := cb.Send(context.Background(), RegisterUser{
							BaseCommand: wcqrs.NewBaseCommand(),
							Email:       "sql-user@example.com",
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

package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	wcqrs "github.com/madman/watermill-cqrs-fx/pkg/cqrs"
	"github.com/madman/watermill-cqrs-fx/pkg/cqrs/cmderr"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/components/cqrs"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ThreeDotsLabs/watermill/pubsub/gochannel"
	"go.uber.org/fx"
	_ "modernc.org/sqlite"
)

// --- Domain Errors ---

type InsufficientFundsError struct {
	AccountID string `json:"AccountID"`
	Amount    int    `json:"Amount"`
}

func (e *InsufficientFundsError) Error() string {
	return fmt.Sprintf("insufficient funds for account %s: need %d", e.AccountID, e.Amount)
}

func init() {
	// Register the custom error
	cmderr.RegisterType("INSUFFICIENT_FUNDS", &InsufficientFundsError{})
}

// --- Domain Commands ---

type WithdrawMoney struct {
	wcqrs.BaseCommand
	AccountID string
	Amount    int
}

// --- Handlers ---

type AccountHandler struct{}

func (h *AccountHandler) Handle(ctx context.Context, tx wcqrs.Tx, cmd *WithdrawMoney) error {
	fmt.Printf(">>> Processing withdrawal: %s, amount: %d\n", cmd.AccountID, cmd.Amount)

	domainErr := &InsufficientFundsError{
		AccountID: cmd.AccountID,
		Amount:    cmd.Amount,
	}

	// Create a structured CommandError
	return cmderr.New("INSUFFICIENT_FUNDS", domainErr)
}

func NewAccountHandler() *AccountHandler {
	return &AccountHandler{}
}

func main() {
	dbPath := filepath.Join(os.TempDir(), "errors_demo_refined.db")
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
						error TEXT,
						error_data BLOB,
						started_at DATETIME NOT NULL,
						finished_at DATETIME
					);
				`)
				return db, err
			},
			func(db *sql.DB) wcqrs.TransactionManager {
				return wcqrs.NewSQLTransactionManager(db)
			},
			func(db *sql.DB) wcqrs.CommandExecutionStore {
				return wcqrs.NewSQLCommandExecutionStore(db, "command_executions")
			},

			NewAccountHandler,
			func(h *AccountHandler) wcqrs.CommandHandlerOut {
				return wcqrs.CommandHandlerOut{
					Handler: wcqrs.NewCommandHandler("AccountHandler", h.Handle),
				}
			},
		),
		fx.Invoke(func(lc fx.Lifecycle, router *message.Router, cp *cqrs.CommandProcessor, cb wcqrs.CommandBus) {
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

						fmt.Println("\n--- Sending command that will fail with CommandError ---")
						cmd := WithdrawMoney{
							BaseCommand: wcqrs.NewBaseCommand(),
							AccountID:   "ACC-456",
							Amount:      5000,
						}
						err := cb.Send(context.Background(), cmd)
						if err != nil {
							fmt.Printf("Failed to send command: %v\n", err)
							return
						}

						fmt.Println("Wait for command processing...")
						_, err = cb.Wait(context.Background(), cmd.CommandID(), 5*time.Second)
						if err != nil {
							fmt.Printf("\nCommand failed with error: %v\n", err)

							// Inspect CommandError
							var ce *cmderr.CommandError
							if errors.As(err, &ce) {
								fmt.Printf("Metadata: Code=%s, Msg=%s\n", ce.Code, ce.Msg)
								if ce.Details != nil {
									fmt.Printf("Details: %v\n", ce.Details)
								}
							}

							// Verify errors.As for domain error
							var insErr *InsufficientFundsError
							if errors.As(err, &insErr) {
								fmt.Printf("SUCCESS: Caught domain error with errors.As!\n")
								fmt.Printf("Reconstructed Details: AccountID=%s, Amount=%d\n", insErr.AccountID, insErr.Amount)
							} else {
								fmt.Printf("FAILURE: Error is NOT InsufficientFundsError\n")
							}
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

	go func() {
		time.Sleep(3 * time.Second)
		os.Exit(0)
	}()
	app.Run()
}

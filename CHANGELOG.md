# Changelog

All notable changes to `watermill-cqrs-fx` will be documented in this file.

## [v0.4.1] - 2026-10-05

### Fixed & Improved

- **Backward Compatibility for Core Interfaces**:
  - Reverted `TransactionManager` to its original single-method definition (`WithinTransaction`).
  - Extracted post-commit hook registration to optional interface `AfterCommitter` (`AfterCommit(ctx context.Context, fn func()) bool`).
  - Reverted `CommandExecutionStore` to its original 7 methods.
  - Extracted pending commands counting to optional interface `PendingCounter` (`CountPending(ctx context.Context, tx Tx) (int, error)`). If not implemented by a store, `MaxPending` backpressure is gracefully bypassed.
- **`SQLTransactionManager.AfterCommit` Signature Change**:
  - `SQLTransactionManager.AfterCommit` now returns `bool` (`true` if registered inside an active `WithinTransaction` context, `false` otherwise).
  - *Note*: This is a minor signature refinement from `v0.4.0` where it previously returned `void`.
- **Immediate Notification Fallback for External Transactions**:
  - In `EventBus.Publish`, if events are saved under a transaction outside `WithinTransaction` (or when `AfterCommit` registration returns `false`), the outbox notifier is triggered immediately instead of falling back only to polling.
- **Explicit Notifier Sharing & Standalone Manual Wiring**:
  - `SQLQueueConfig.Normalize()` and `SQLOutboxWorkerConfig.Normalize()` no longer create isolated dummy notifiers.
  - Introduced `NotifierProvider` interface (`Notifier() Notifier`) implemented by `CommandBus`, `SQLCommandQueueWorker`, and `SQLOutboxWorker`.
  - Introduced `SQLQueueConfigProvider` interface (`SQLQueueConfig() SQLQueueConfig`) implemented by `CommandBus`.
  - Updated `NewSQLCommandQueueWorkerWithBus` to accept `outboxNotifier` and reuse the complete `SQLQueueConfig` (including concurrency, poll interval, backoff, and notifier) from the bus.
- **Worker Startup Logging**:
  - Added `"wakeup_enabled"` to `SQLCommandQueueWorker` and `SQLOutboxWorker` startup logs.
  - Added explicit notice logging when workers are configured without a wake-up Notifier (running in fallback polling mode only).
- **Singleflight Resiliency**:
  - In `checkQueueCapacity`, the `CountPending` query executes with `context.WithoutCancel(ctx)` and a 3-second timeout so that canceling the initial caller's context does not fail concurrent callers sharing the deduplicated singleflight call.
- **CI & Integration Tests**:
  - Added MySQL 8.0 service container in GitHub Actions workflow with reliable TCP ping healthcheck (`mysqladmin ping -h 127.0.0.1 -uroot -prootpassword`).
  - Removed project-specific default DSN; integration test skips if `MYSQL_DSN` is empty and fails if unreachable.
- **Dependencies**:
  - Cleaned up `go.mod` and `go.sum`, promoting `golang.org/x/sync` to direct dependency.

## [v0.4.0] - 2026-10-04

### Added
- Hybrid in-process wake-up notifications (`Notifier`) for SQL command queue and outbox workers.
- Worker concurrency parameterization using `SELECT ... FOR UPDATE SKIP LOCKED` on MySQL.
- Backpressure support via `MaxPending` and `ErrQueueFull`.
- Parameterized polling interval and database error backoff.

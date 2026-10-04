# CQRS with Watermill and Uber.fx

This project implements a CQRS (Command Query Responsibility Segregation) approach in Go using the [Watermill](https://watermill.io/) library and [Uber.fx](https://github.com/uber-go/fx) for dependency injection and modularity.

## Goals

- **CQRS Implementation**: Separation of Read (Query) and Write (Command) operations.
- **Watermill Integration**: Use Watermill as the underlying message routing and handling engine for commands, queries, and events.
- **Uber.fx Modularity**: Provide a clean `fx.Module` that can be easily integrated into any application, with automatic discovery and registration of handlers.
- **Event Sourcing Ready**: Design the architecture to be compatible with event sourcing patterns.

## Architecture

The module will provide:

- **Command Bus**: To dispatch commands to their respective handlers.
- **Query Bus**: To execute queries and return results.
- **Event Bus**: To publish events resulting from command execution.
- **Automatic Registration**: Use Fx's provide/invoke patterns (likely using groups or tags) to automatically register handlers that implement specific interfaces.

## Transactional & Fault-Tolerant CQRS (SQL Queue & Outbox)

This module supports a highly resilient, single-transaction CQRS execution cycle with **hybrid in-process wake-up**:

1. **Commands** are queued directly in a database table (`command_executions`).
2. **Immediate In-Process Wake-Up**: Dispatching a command immediately triggers worker processing via an in-process `Notifier` (sub-millisecond latency), eliminating aggressive 100ms database polling.
3. **Background Workers** execute commands inside a single database transaction using row-level locking (`FOR UPDATE SKIP LOCKED`).
4. **Domain changes and Outbox events** are committed atomically in that same transaction.
5. **After-Commit Outbox Wake-Up**: Upon successful transaction commit, the outbox worker is woken immediately to dispatch pending events.
6. **Backpressure**: When pending commands exceed `MaxPending` (default 1000), `CommandBus.Send()` returns `ErrQueueFull` (`errors.Is`-compatible).
7. **Relaxed Fallback Polling**: A 30s fallback polling interval guarantees eventual recovery and multi-instance processing without burning database CPU at idle.

For sequence diagrams, configuration tables, schema definitions, and migration instructions, see:
👉 **[Transactional CQRS Documentation](docs/transactional_cqrs.md)**

## Technology Stack

- **Go**: Primary programming language (1.27+).
- **Watermill**: Message library for Go.
- **Uber.fx**: Dependency injection framework.

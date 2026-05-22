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

This module supports a highly resilient, single-transaction CQRS execution cycle where:

1. Commands are queued directly in a database table.
2. Background workers execute commands inside a single database transaction using row-level locking (`FOR UPDATE SKIP LOCKED`).
3. Domain changes and emitted events (Outbox) are written to the database atomically.
4. An asynchronous Outbox worker guarantees **At-Least-Once** event delivery to local or remote handlers.

For sequence diagrams, setup instructions, and deep technical details of this approach, please refer to the:
👉 **[Transactional CQRS Documentation](docs/transactional_cqrs.md)**

## Technology Stack

- **Go**: Primary programming language.
- **Watermill**: Message library for Go.
- **Uber.fx**: Dependency injection framework.

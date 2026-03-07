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

## Technology Stack

- **Go**: Primary programming language.
- **Watermill**: Message library for Go.
- **Uber.fx**: Dependency injection framework.

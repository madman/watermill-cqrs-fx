GOLANGCI_LINT_VERSION := v2.8.0
GOLANGCI_LINT := github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

.PHONY: lint lint-fix

lint:
	go run $(GOLANGCI_LINT) run ./...

lint-fix:
	go run $(GOLANGCI_LINT) run --fix ./...

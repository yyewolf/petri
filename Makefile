.DEFAULT_GOAL := help

.PHONY: help test lint fmt tidy

help: ## Show this help
	@awk 'BEGIN {FS = ":.*## "} /^[a-z-]+:.*## / {printf "  %-6s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

test: ## Run tests with the race detector
	go test -race ./...

lint: ## Run golangci-lint
	golangci-lint run

fmt: ## Format code with the golangci-lint formatters
	golangci-lint fmt

tidy: ## Tidy go.mod
	go mod tidy

.DEFAULT_GOAL := help

.PHONY: help test lint fmt tidy mirror

help: ## Show this help
	@awk 'BEGIN {FS = ":.*## "} /^[a-z-]+:.*## / {printf "  %-6s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

test: ## Run tests with the race detector
	go test -race ./...

lint: ## Run golangci-lint
	golangci-lint config verify
	golangci-lint run

fmt: ## Format code with the golangci-lint formatters
	golangci-lint fmt

tidy: ## Tidy go.mod
	go mod tidy

mirror: ## Regenerate internal/kubernetes (K8S_VERSION=v1.N.P switches version)
	go run ./hack/mirror sync $(if $(K8S_VERSION),-version $(K8S_VERSION))

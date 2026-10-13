.DEFAULT_GOAL := help

MODULES = . ./controllerruntime ./website/examples

.PHONY: help test lint fmt tidy mirror

help: ## Show this help
	@awk 'BEGIN {FS = ":.*## "} /^[a-z-]+:.*## / {printf "  %-6s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

test: ## Run tests with the race detector (every module)
	@for m in $(MODULES); do (cd $$m && go test -race ./...) || exit 1; done

lint: ## Run golangci-lint (every module)
	golangci-lint config verify
	@for m in $(MODULES); do (cd $$m && golangci-lint run) || exit 1; done

fmt: ## Format code with the golangci-lint formatters (every module)
	@for m in $(MODULES); do (cd $$m && golangci-lint fmt) || exit 1; done

tidy: ## Tidy go.mod (every module)
	@for m in $(MODULES); do (cd $$m && go mod tidy) || exit 1; done

mirror: ## Regenerate internal/kubernetes and internal/kine (K8S_VERSION=v1.N.P, KINE_VERSION=v0.N.P switch versions)
	go run ./hack/mirror sync $(if $(K8S_VERSION),-version $(K8S_VERSION)) $(if $(KINE_VERSION),-kine-version $(KINE_VERSION))

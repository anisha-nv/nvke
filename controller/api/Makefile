# Copyright (c) 2026, NVIDIA CORPORATION & AFFILIATES.  All rights reserved.

# Build/lint/test targets for the controller/api Go module (github.com/NVIDIA/nke/controller/api).
# CI invokes these `make` targets directly (see .github/workflows/qualification.yaml) so any
# failure is reproducible locally with the same command.

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Build

.PHONY: lint
lint: ## Lint source files.
ifdef CI
	@if ! command -v golangci-lint >/dev/null 2>&1; then \
		curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b /usr/local/bin v2.7.2; \
	fi
	golangci-lint run --timeout 20m --max-same-issues 0 --max-issues-per-linter 0 ./...
else
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.7.2 run --timeout 5m --max-same-issues 0 --max-issues-per-linter 0 ./...
endif

.PHONY: test
test: ## Execute unit tests.
	go test ./...

.PHONY: build
build: ## Build all packages.
	go build ./...

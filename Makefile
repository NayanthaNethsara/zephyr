BINARY := zephyr
BIN_DIR := bin
GATEWAY_DIR := apps/gateway
PKG := ./cmd/gateway

ifneq (,$(wildcard .env))
include .env
export
endif

.DEFAULT_GOAL := help

.PHONY: help
help:
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z_-]+:.*## / {printf "  %-14s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

.PHONY: setup
setup: ## Create .env from .env.example and download modules
	@test -f .env || cp .env.example .env
	cd $(GATEWAY_DIR) && go mod download

.PHONY: run
run: ## Run the gateway locally
	cd $(GATEWAY_DIR) && go run $(PKG)

.PHONY: build
build: ## Build the binary into bin/
	cd $(GATEWAY_DIR) && CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w" -o $(BIN_DIR)/$(BINARY) $(PKG)

.PHONY: test
test: ## Run tests
	cd $(GATEWAY_DIR) && go test -race ./...

.PHONY: fmt
fmt: ## Format code
	cd $(GATEWAY_DIR) && gofmt -w .

.PHONY: vet
vet: ## Run go vet
	cd $(GATEWAY_DIR) && go vet ./...

.PHONY: lint
lint: ## Run golangci-lint
	cd $(GATEWAY_DIR) && golangci-lint run

.PHONY: tidy
tidy: ## Tidy go.mod
	cd $(GATEWAY_DIR) && go mod tidy

.PHONY: docker-build
docker-build: ## Build the Docker image
	docker compose build gateway

.PHONY: up
up: ## Start the gateway with Docker Compose
	docker compose up --build --detach gateway

.PHONY: down
down: ## Stop Docker Compose services
	docker compose down

.PHONY: logs
logs: ## Follow gateway logs
	docker compose logs --follow gateway

.PHONY: clean
clean: ## Remove build output
	rm -rf $(GATEWAY_DIR)/$(BIN_DIR)

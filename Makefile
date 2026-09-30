# Park's development commands; `make help` lists them.

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
# The database tests run against the Postgres that `make db` starts.
export PARK_TEST_DATABASE_URL ?= postgres://park:park@localhost:5432/park?sslmode=disable

.PHONY: help gen web build test lint db

help: ## List the commands
	@awk 'BEGIN {FS = ":.*## "} /^[a-z]+:.*## / {printf "  make %-6s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

gen: ## Generate the server code and the client types from api/openapi.yaml
	go generate ./...
	cd web && pnpm run gen

web: ## Build the frontend into web/dist
	cd web && pnpm install --frozen-lockfile && pnpm run build

build: web ## Build bin/park with the frontend embedded
	go build -ldflags "-X main.version=$(VERSION)" -o bin/park ./cmd/park

test: ## Run the Go tests, then the whole sweep without the race detector; the database tests need `make db`
	go test -race ./...
	go test -count=1 -run TestSweep ./internal/verdicts/

lint: ## Lint the Go code and the frontend
	golangci-lint run
	cd web && pnpm run lint && pnpm run typecheck

db: ## Start a Postgres for development and tests
	docker compose up -d --wait db

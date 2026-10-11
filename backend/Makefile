.PHONY: run worker build tidy test test-integration lint fmt

DATABASE_URL ?= postgres://talanta:talanta@localhost:5434/talanta?sslmode=disable

run: ## Run the API against local Postgres (needs `make db-up migrate-up` from the repo root)
	DATABASE_URL="$(DATABASE_URL)" go run ./cmd/api

worker: ## Run the background worker against local Postgres
	DATABASE_URL="$(DATABASE_URL)" go run ./cmd/worker

build: ## Build every binary into bin/
	go build -o bin/ ./cmd/...

tidy: ## Resolve module dependencies
	go mod tidy

test: ## Unit tests (no database needed)
	go test -race ./...

test-integration: ## Unit + integration tests; starts Postgres 16 in Docker via testcontainers
	go test -race -tags=integration -count=1 ./...

lint: ## gofmt and go vet, with and without the integration tag
	@unformatted="$$(gofmt -l .)"; if [ -n "$$unformatted" ]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi
	go vet ./...
	go vet -tags=integration ./...

fmt: ## Format the code
	gofmt -w .

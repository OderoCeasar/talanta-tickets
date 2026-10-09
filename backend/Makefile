.PHONY: run build tidy test

DATABASE_URL ?= postgres://talanta:talanta@localhost:5434/talanta?sslmode=disable

run: ## Run the API against local Postgres (needs `make db-up migrate-up` from the repo root)
	DATABASE_URL="$(DATABASE_URL)" go run ./cmd/api

build: ## Build the binary
	go build -o bin/api ./cmd/api

tidy: ## Resolve module dependencies
	go mod tidy

test: ## Run tests
	go test ./...

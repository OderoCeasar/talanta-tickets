.PHONY: help install dev down logs backend worker admin mobile db-up migrate-up migrate-down \
        db-reset seed sqlc openapi-clients test test-integration lint loadtest

DATABASE_URL ?= postgres://talanta:talanta@localhost:5434/talanta?sslmode=disable
MIGRATIONS := backend/migrations

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

install: ## Install JS deps
	pnpm install

dev: ## Start Postgres, run migrations, start the API (http://localhost:8080), worker and Adminer
	docker compose up --build -d --wait db api worker adminer
	@echo "API: http://localhost:8080/v1/healthz   Adminer: http://localhost:8081"

down: ## Stop the local stack (keeps data)
	docker compose down

logs: ## Follow the API and worker logs
	docker compose logs -f api worker

backend: ## Run the Go API on the host against the local database
	$(MAKE) -C backend run

worker: ## Run the background worker on the host against the local database
	$(MAKE) -C backend worker

admin: ## Run the admin web app
	pnpm --filter @talanta/admin dev

mobile: ## Run the Expo app
	pnpm --filter @talanta/mobile start

db-up: ## Start only Postgres
	docker compose up -d --wait db

migrate-up: ## Apply all migrations (needs the golang-migrate CLI)
	migrate -path $(MIGRATIONS) -database "$(DATABASE_URL)" up

migrate-down: ## Roll back the last migration
	migrate -path $(MIGRATIONS) -database "$(DATABASE_URL)" down 1

db-reset: ## Wipe the local database and rebuild it from migrations
	docker compose down -v
	docker compose up -d --wait db
	docker compose run --rm migrate

seed: ## Build the made-up Talanta venue, two demo events and one user per role
	cd backend && DATABASE_URL="$(DATABASE_URL)" go run ./cmd/seed

sqlc: ## Regenerate the Go query code (needs the sqlc CLI, v1.31.1)
	cd backend && sqlc generate

openapi-clients: ## Regenerate the TypeScript API types from backend/docs/openapi.yaml
	pnpm --filter @talanta/shared generate

test: ## Backend unit tests
	$(MAKE) -C backend test

test-integration: ## Backend unit + integration tests (starts Postgres in Docker)
	$(MAKE) -C backend test-integration

lint: ## Backend gofmt + go vet, then typecheck the JS workspaces
	$(MAKE) -C backend lint
	pnpm -r typecheck

.PHONY: help install dev down backend admin mobile db-up migrate-up migrate-down db-reset test

DATABASE_URL ?= postgres://talanta:talanta@localhost:5434/talanta?sslmode=disable
MIGRATIONS := backend/migrations

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

install: ## Install JS deps
	pnpm install

dev: ## Start Postgres, run migrations, start the API (http://localhost:8080) and Adminer
	docker compose up --build -d
	@echo "API: http://localhost:8080/v1/healthz   Adminer: http://localhost:8081"

down: ## Stop the local stack (keeps data)
	docker compose down

backend: ## Run the Go API on the host against the local database
	$(MAKE) -C backend run

admin: ## Run the admin web app
	pnpm --filter @talanta/admin dev

mobile: ## Run the Expo app
	pnpm --filter @talanta/mobile start

db-up: ## Start only Postgres
	docker compose up -d db

migrate-up: ## Apply all migrations (needs the golang-migrate CLI)
	migrate -path $(MIGRATIONS) -database "$(DATABASE_URL)" up

migrate-down: ## Roll back the last migration
	migrate -path $(MIGRATIONS) -database "$(DATABASE_URL)" down 1

db-reset: ## Wipe the local database and rebuild it from migrations
	docker compose down -v
	docker compose up -d db migrate

test: ## Run backend tests
	$(MAKE) -C backend test

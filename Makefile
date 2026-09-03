.PHONY: help run build test cover lint fmt docker up down migrate

help: ## Show available targets
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-10s %s\n", $$1, $$2}'

run: ## Run the API locally
	go run ./cmd/api

build: ## Compile the binary into bin/
	go build -trimpath -ldflags="-s -w" -o bin/holibrary-api ./cmd/api

test: ## Run the test suite with the race detector
	go test -race ./...

cover: ## Run tests and print coverage
	go test -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

fmt: ## Format all Go source
	gofmt -w .

vet: ## Run go vet
	go vet ./...

docker: ## Build the production image
	docker build -t holibrary-backend:local .

up: ## Start the local stack (Postgres, Redis, API)
	docker compose up --build

down: ## Stop the local stack
	docker compose down

migrate: ## Apply migrations to $$DATABASE_URL
	@for f in migrations/*.sql; do \
		echo "applying $$f"; \
		psql "$$DATABASE_URL" -v ON_ERROR_STOP=1 -f "$$f" || exit 1; \
	done

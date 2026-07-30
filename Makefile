# Weather Chain - Docker Management Makefile

.PHONY: help build up down logs restart clean setup test shell mongo-shell status

# Default target
help:
	@echo "Weather Chain Docker Commands:"
	@echo ""
	@echo "  make build        - Build Docker images"
	@echo "  make up           - Start all services"
	@echo "  make down         - Stop all services"
	@echo "  make logs         - View application logs"
	@echo "  make restart      - Restart application"
	@echo "  make clean        - Remove containers and volumes"
	@echo "  make setup        - Run funding basket setup"
	@echo "  make test         - Run tests in Docker"
	@echo "  make shell        - Open shell in app container"
	@echo "  make mongo-shell  - Open MongoDB shell"
	@echo "  make status       - Show service status"
	@echo ""

# Build Docker images
build:
	docker-compose build

# Start all services
up:
	@echo "Starting MongoDB..."
	docker-compose up -d mongodb
	@echo "Waiting for MongoDB to be ready..."
	@sleep 10
	@echo "Starting Weather Chain application..."
	docker-compose up -d app
	@echo "Services started. Use 'make logs' to view logs."

# Stop all services
down:
	docker-compose down

# View logs
logs:
	docker-compose logs -f app

# Restart application
restart:
	docker-compose restart app

# Clean up everything (including volumes)
clean:
	@echo "WARNING: This will remove all containers and data volumes!"
	@read -p "Are you sure? [y/N] " -n 1 -r; \
	echo; \
	if [[ $$REPLY =~ ^[Yy]$$ ]]; then \
		docker-compose down -v; \
		echo "Cleaned up successfully."; \
	else \
		echo "Cancelled."; \
	fi

# Run setup to create funding basket
setup:
	@echo "Running funding basket setup..."
	docker-compose --profile setup run --rm setup

# Run tests
test:
	docker-compose run --rm app npm test

# Open shell in app container
shell:
	docker-compose exec app sh

# Open MongoDB shell
mongo-shell:
	docker-compose exec mongodb mongosh -u admin -p password weather-chain

# Show service status
status:
	@echo "Service Status:"
	@docker-compose ps
	@echo ""
	@echo "Resource Usage:"
	@docker stats --no-stream weather-chain-app weather-chain-mongodb 2>/dev/null || echo "Containers not running"

# Quick start (build + up + setup)
quickstart: build
	@echo "Starting services..."
	@make up
	@echo ""
	@echo "Waiting for services to stabilize..."
	@sleep 5
	@echo ""
	@echo "Ready for setup. Run 'make setup' to create funding basket."

# Development mode (start only MongoDB, run app locally)
dev:
	@echo "Starting MongoDB only..."
	docker-compose up -d mongodb
	@echo "MongoDB started. You can now run 'npm run dev' locally."

.PHONY: go-build go-test go-lint go-golden check

# Named go-* because this Makefile already has Docker-oriented `build` and
# `test` targets that must keep working. Reconciling them is a later plan.
go-build:
	go build ./...

go-test:
	go test ./... -count=1

go-lint:
	golangci-lint run

# Regenerate the self-generated golden file. Review the diff before committing:
# this target is how a deliberate format change is recorded, and a surprising
# diff here means the encoder changed by accident.
go-golden:
	go test ./internal/weather -run TestGolden -update -count=1

check: go-build go-test go-lint

# ---------------------------------------------------------------------------
# Postgres for the Go store tests.
#
# Named pg-*/go-* because this Makefile's `build`, `up`, `down` and `test`
# belong to the Docker/TypeScript workflow and must keep working. New targets
# use `docker compose` (the v2+ plugin spelling) rather than the legacy
# hyphenated `docker-compose` used above; reconciling the two is a later plan.
# There is no host psql client in this project's environment, so pg-psql execs
# into the container.
# ---------------------------------------------------------------------------
.PHONY: pg-up pg-down pg-psql go-test-pg

WEATHER_TEST_POSTGRES_DSN ?= postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable

pg-up:
	docker compose up -d postgres
	@echo "Waiting for Postgres..."
	@until docker compose exec -T postgres pg_isready -U postgres -d weatherproof_test >/dev/null 2>&1; do sleep 1; done
	@echo "Postgres ready on 127.0.0.1:5432 (database weatherproof_test)"

pg-down:
	docker compose stop postgres

pg-psql:
	docker compose exec postgres psql -U postgres -d weatherproof_test

go-test-pg:
	WEATHER_TEST_POSTGRES_DSN='$(WEATHER_TEST_POSTGRES_DSN)' \
	WEATHER_TEST_REQUIRE_POSTGRES=1 \
	go test -race -count=1 -timeout 10m ./internal/store/...

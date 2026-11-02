# Loads .env when present, so DATABASE_URL reaches every command.
-include .env
export

MIGRATE_VERSION       := v4.20.1
SQLC_VERSION          := v1.31.1
# Keep in sync with .github/workflows/ci.yml.
GOLANGCI_LINT_VERSION := v2.15.0

BIN := $(CURDIR)/bin

# sqlc and golangci-lint need a newer Go than the project. GOTOOLCHAIN=auto lets
# go install download it for the build only; CI sets GOTOOLCHAIN=local otherwise.
GO_INSTALL := GOTOOLCHAIN=auto GOBIN=$(BIN) go install

.PHONY: tools up down migrate-up migrate-down sqlc test lint check-database-url

tools: bin/migrate bin/sqlc bin/golangci-lint

# The postgres tag compiles the driver into migrate; without it, migrate
# cannot open postgres:// URLs.
bin/migrate:
	$(GO_INSTALL) -tags postgres github.com/golang-migrate/migrate/v4/cmd/migrate@$(MIGRATE_VERSION)

bin/sqlc:
	$(GO_INSTALL) github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)

bin/golangci-lint:
	$(GO_INSTALL) github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

up:
	docker compose up -d --wait

down:
	docker compose down

migrate-up: check-database-url bin/migrate
	bin/migrate -path migrations -database "$(DATABASE_URL)" up

# Undoes one migration only. A bare "down" would wipe the whole database.
migrate-down: check-database-url bin/migrate
	bin/migrate -path migrations -database "$(DATABASE_URL)" down 1

sqlc: bin/sqlc
	bin/sqlc generate

# Needs Docker: integration tests run against a real PostgreSQL.
test:
	go test -race ./...

lint: bin/golangci-lint
	bin/golangci-lint run

check-database-url:
	@test -n "$(DATABASE_URL)" || { echo "DATABASE_URL is not set: copy .env.example to .env"; exit 1; }

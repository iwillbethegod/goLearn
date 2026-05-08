# Day-6 ops-flavoured targets. Run `make help` for a summary.

SHELL := /bin/bash

# Pinned versions of CLI tools that ship as `package main` (so they
# can't live in tools/tools.go). Bump here, contributors re-install.
SQLC_VERSION    ?= v1.31.1
MIGRATE_VERSION ?= v4.19.1

# Default DSN for local Docker-Compose dev. Override via env.
DATABASE_URL ?= postgres://app:app@localhost:5432/app?sslmode=disable
NATS_URL     ?= nats://localhost:4222

# Migration tags pull in the postgres driver; without these the migrate
# binary refuses pg:// URLs.
MIGRATE_TAGS := postgres

.PHONY: help install-tools sqlc-gen migrate-up migrate-down \
        compose-up compose-down compose-logs psql nats-cli \
        api-run consumer-run \
        test test-race build vet

help:
	@echo "make install-tools   install sqlc + golang-migrate at pinned versions"
	@echo "make sqlc-gen        regenerate internal/storage/postgres/pgdb/"
	@echo "make migrate-up      apply all pending migrations against DATABASE_URL"
	@echo "make migrate-down    revert one migration"
	@echo "make compose-up      docker compose up -d  (db + nats + jaeger)"
	@echo "make compose-down    docker compose down (volumes persist)"
	@echo "make compose-logs    tail compose logs (all services)"
	@echo "make psql            open a psql shell against DATABASE_URL"
	@echo "make nats-cli        open a NATS CLI shell on the golearn-net"
	@echo "make api-run         run cmd/api with -storage=postgres -migrate"
	@echo "make consumer-run    run cmd/consumer against NATS_URL + DATABASE_URL"
	@echo "make test            go test ./..."
	@echo "make test-race       go test -race ./..."
	@echo "make build / vet     go build / vet against ./..."

install-tools:
	go install github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)
	go install -tags '$(MIGRATE_TAGS)' \
	    github.com/golang-migrate/migrate/v4/cmd/migrate@$(MIGRATE_VERSION)

sqlc-gen:
	sqlc generate

migrate-up:
	migrate -path db/migrations -database '$(DATABASE_URL)' up

migrate-down:
	migrate -path db/migrations -database '$(DATABASE_URL)' down 1

compose-up:
	docker compose up -d

compose-down:
	docker compose down

compose-logs:
	docker compose logs -f

psql:
	psql '$(DATABASE_URL)'

# Open the NATS CLI inside the compose network so it can reach `nats`
# by service name. Use it to inspect streams/consumers, e.g.:
#   make nats-cli   # then inside: nats stream ls / consumer info USERS user-welcome
nats-cli:
	docker run --rm -it --network golearn-net natsio/nats-box \
	    nats -s nats://nats:4222

api-run:
	go run ./cmd/api -storage=postgres -migrate -nats-url='$(NATS_URL)'

consumer-run:
	go run ./cmd/consumer -nats-url='$(NATS_URL)' -db-dsn='$(DATABASE_URL)'

test:
	go test ./...

test-race:
	go test -race ./...

build:
	go build ./...

vet:
	go vet ./...

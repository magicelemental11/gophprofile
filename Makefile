ifneq (,$(wildcard .env))
include .env
export
endif

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
export VERSION
LDFLAGS := -X main.version=$(VERSION)

.PHONY: env build run-server run-worker docker-build up up-deps down logs test test-integration test-alerts cover lint load clean

env:
	@test ! -e .env || { echo ".env already exists" >&2; exit 1; }
	@awk '/^[A-Z_]+_PASSWORD=$$/ { cmd = "openssl rand -hex 24"; cmd | getline p; close(cmd); $$0 = $$0 p } { print }' .env.example > .env
	@echo "created .env with random passwords"

build:
	go build -ldflags "$(LDFLAGS)" -o bin/server ./cmd/server
	go build -ldflags "$(LDFLAGS)" -o bin/worker ./cmd/worker

run-server:
	go run ./cmd/server

run-worker:
	go run ./cmd/worker

docker-build:
	docker compose build

up:
	docker compose up -d --build

up-deps:
	docker compose up -d postgres rabbitmq minio

down:
	docker compose down

logs:
	docker compose logs -f server worker

test:
	go test -race ./...

test-integration:
	go test -race -tags integration ./...

PROMETHEUS_IMAGE ?= prom/prometheus:v3.15.1

ALERTMANAGER_IMAGE ?= prom/alertmanager:v0.34.2

test-alerts:
	docker run --rm -v "$(CURDIR)/deploy/prometheus:/rules:ro" --entrypoint promtool $(PROMETHEUS_IMAGE) check config /rules/prometheus.yml
	docker run --rm -v "$(CURDIR)/deploy/alertmanager:/config:ro" --entrypoint amtool $(ALERTMANAGER_IMAGE) check-config /config/alertmanager.yml
	docker run --rm -v "$(CURDIR)/deploy/prometheus:/rules:ro" --entrypoint promtool $(PROMETHEUS_IMAGE) test rules /rules/alerts_test.yml

cover:
	go test -race -coverprofile=cover.out $$(go list ./... | grep -v /cmd/)
	go tool cover -func=cover.out | tail -1

lint:
	golangci-lint run

load:
	scripts/load.sh

clean:
	rm -rf bin cover.out

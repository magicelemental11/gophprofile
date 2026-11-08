ifneq (,$(wildcard .env))
include .env
export
endif

.PHONY: build run-server run-worker docker-build up up-deps down logs test test-integration cover lint clean

build:
	go build -o bin/server ./cmd/server
	go build -o bin/worker ./cmd/worker

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

cover:
	go test -race -coverprofile=cover.out $$(go list ./... | grep -v /cmd/)
	go tool cover -func=cover.out | tail -1

lint:
	golangci-lint run

clean:
	rm -rf bin cover.out

ifneq (,$(wildcard .env))
include .env
export
endif

.PHONY: build run-server run-worker up down test test-integration cover lint clean

build:
	go build -o bin/server ./cmd/server
	go build -o bin/worker ./cmd/worker

run-server:
	go run ./cmd/server

run-worker:
	go run ./cmd/worker

up:
	docker compose up -d

down:
	docker compose down

test:
	go test -race ./...

test-integration:
	go test -race -tags integration ./...

cover:
	go test -race -coverprofile=cover.out ./...
	go tool cover -func=cover.out | tail -1

lint:
	golangci-lint run

clean:
	rm -rf bin cover.out

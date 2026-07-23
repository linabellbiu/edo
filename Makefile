.PHONY: dev-up dev-down deps-up deps-down migrate server web test build

dev-up:
	docker compose -f deploy/compose.dev.yml up --build

dev-down:
	docker compose -f deploy/compose.dev.yml down

deps-up:
	docker compose -f deploy/compose.dev.yml up -d redis nats

deps-down:
	docker compose -f deploy/compose.dev.yml down

migrate:
	go run ./cmd/zrt migrate

server:
	go run ./cmd/zrt server

web:
	npm --prefix web run dev

test:
	go test ./...
	go vet ./...
	npm --prefix web run build

build:
	mkdir -p bin
	go build -trimpath -o bin/zrt ./cmd/zrt
	npm --prefix web run build

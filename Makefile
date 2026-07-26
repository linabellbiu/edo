.PHONY: dev-up dev-down deps-up deps-down migrate server web test build

dev-up:
	docker compose -f deploy/compose.dev.yml up --build


migrate:
	go run ./cmd/zrt migrate

server:
	go run ./cmd/zrt server

web:
	npm --prefix web run dev

build:
	mkdir -p bin
	go build -trimpath -o bin/zrt ./cmd/zrt
	npm --prefix web run build

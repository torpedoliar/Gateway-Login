.PHONY: tidy build test test-unit test-integration docker-up docker-down setup clean

tidy:
	go mod tidy

build:
	go build -o bin/api ./cmd/api
	go build -o bin/sync ./cmd/sync
	go build -o bin/setup ./cmd/setup

test:
	go test ./... -short

test-unit:
	go test ./internal/... -short

test-integration:
	go test ./tests/integration/... -v

docker-up:
	cd deploy && docker compose up -d --build

docker-down:
	cd deploy && docker compose down

setup:
	cd deploy && docker compose run --rm setup

clean:
	rm -rf bin/ coverage.txt

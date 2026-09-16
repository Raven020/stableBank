.PHONY: run build test vet db-up db-down

build:
	go build -o bin/stablebank ./cmd/stablebank

run:
	go run ./cmd/stablebank

test:
	go vet ./... && go test ./...

db-up:
	docker compose up -d postgres

db-down:
	docker compose down

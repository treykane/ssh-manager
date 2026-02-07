BINARY=ssh-manager

.PHONY: build test run lint

build:
	go build ./cmd/ssh-manager

test:
	go test ./...

run:
	go run ./cmd/ssh-manager

lint:
	go vet ./...

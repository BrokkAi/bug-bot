.PHONY: build test check
build:
	go build -o bin/bbb ./cmd/bbb
test:
	go test -race ./...
check: test
	go vet ./...

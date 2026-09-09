.PHONY: build test check licenses
build:
	go build -o bin/bbb ./cmd/bbb
test:
	go test -race ./...
licenses:
	python3 scripts/licenses.py
check: test licenses
	go vet ./...

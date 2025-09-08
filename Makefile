    BIN=edge
    VERSION?=0.1.0
    LDFLAGS=-s -w -X main.version=$(VERSION)

    .PHONY: build run test

    build:
	go build -trimpath -ldflags="$(LDFLAGS)" -o bin/edge ./cmd/edge

    run: build
	./bin/edge

    test:
	go test ./...

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/jackellisw/nzbfirstframe/internal/version.Version=$(VERSION)

.PHONY: build test vet fmt clean

build:
	go build -ldflags "$(LDFLAGS)" -o bin/nzbff ./cmd/nzbff

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

clean:
	rm -rf bin

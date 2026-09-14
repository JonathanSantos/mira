# mira: build, test and local release. The tree-sitter runtime is cgo,
# so builds need a C compiler (clang on macOS, gcc on Linux, mingw on Windows).
MODULE  := github.com/JonathanSantos/mira
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X $(MODULE)/internal/cli.Version=$(VERSION)
BIN     := mira

.PHONY: build install test check lint race release-local clean

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/mira

install:
	go install -trimpath -ldflags "$(LDFLAGS)" ./cmd/mira

test:
	go test ./...

race:
	go test -race ./...

lint:
	gofmt -l . && go vet ./... && golangci-lint run ./...

check: lint race

# Binário para a máquina atual, com a versão embutida, em dist/.
release-local:
	mkdir -p dist
	go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(BIN)-$(VERSION)-$(shell go env GOOS)-$(shell go env GOARCH) ./cmd/mira
	ls -la dist

clean:
	rm -rf $(BIN) dist

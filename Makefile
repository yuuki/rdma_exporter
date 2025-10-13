GO ?= go
BIN_DIR ?= bin
PKG := ./...
BINARY := $(BIN_DIR)/rdma_exporter

.PHONY: all build test lint fmt clean

all: build

build: $(BINARY)

$(BINARY):
	mkdir -p $(BIN_DIR)
	$(GO) build -o $@ .

test:
	$(GO) test $(PKG)

lint:
	$(GO) vet $(PKG)

fmt:
	gofmt -w $(shell find . -type f -name '*.go')

clean:
	rm -rf $(BIN_DIR)

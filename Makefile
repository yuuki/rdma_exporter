GO ?= go
PKG := ./...
BINARY := rdma_exporter

.PHONY: all build test lint fmt clean

all: build

build: $(BINARY)

$(BINARY):
	$(GO) build -o $@ .

test:
	$(GO) test $(PKG)

lint:
	$(GO) vet $(PKG)

fmt:
	gofmt -w $(shell find . -type f -name '*.go')

clean:
	rm -f $(BINARY)

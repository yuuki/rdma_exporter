GO ?= go
PKG := ./...
BINARY := rdma_exporter
MLXLINK_BINARY := mlxlink_exporter

.PHONY: all build test lint fmt clean

all: build

build: $(BINARY) $(MLXLINK_BINARY)

$(BINARY):
	$(GO) build -o $@ .

$(MLXLINK_BINARY):
	$(GO) build -o $@ ./cmd/mlxlink_exporter

test:
	$(GO) test $(PKG)

lint:
	$(GO) vet $(PKG)

fmt:
	gofmt -w $(shell find . -type f -name '*.go')

clean:
	rm -f $(BINARY) $(MLXLINK_BINARY)

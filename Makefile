.PHONY: all build test clean run fmt vet lint install help

# Binary name
BINARY_NAME=prometheus-rdma-exporter

# Version information
VERSION?=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME=$(shell date -u '+%Y-%m-%d_%H:%M:%S')
GIT_COMMIT=$(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")

# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
GOMOD=$(GOCMD) mod
GOFMT=$(GOCMD) fmt
GOVET=$(GOCMD) vet

# Build flags
LDFLAGS=-ldflags "-X main.version=$(VERSION) -X main.buildTime=$(BUILD_TIME) -X main.gitCommit=$(GIT_COMMIT)"

all: fmt vet test build ## Run fmt, vet, test and build

build: ## Build the binary
	$(GOBUILD) $(LDFLAGS) -o $(BINARY_NAME) -v

test: ## Run tests
	$(GOTEST) -v -race -coverprofile=coverage.out ./...

coverage: test ## Generate coverage report
	$(GOCMD) tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated: coverage.html"

clean: ## Remove build artifacts
	$(GOCLEAN)
	rm -f $(BINARY_NAME)
	rm -f coverage.out coverage.html

run: build ## Build and run the exporter
	./$(BINARY_NAME)

fmt: ## Format source code
	$(GOFMT) ./...

vet: ## Run go vet
	$(GOVET) ./...

lint: ## Run golangci-lint (requires golangci-lint installed)
	@which golangci-lint > /dev/null || (echo "golangci-lint not found, install it from https://golangci-lint.run/usage/install/" && exit 1)
	golangci-lint run

mod-download: ## Download dependencies
	$(GOMOD) download

mod-tidy: ## Tidy dependencies
	$(GOMOD) tidy

mod-verify: ## Verify dependencies
	$(GOMOD) verify

install: build ## Install the binary to GOPATH/bin
	install -m 755 $(BINARY_NAME) $(GOPATH)/bin/

help: ## Display this help message
	@echo "Available targets:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-20s %s\n", $$1, $$2}'

GO ?= go
PKG := ./...
BINARY := rdma_exporter

.PHONY: all build test lint fmt complexity clean grafana-com-export

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

# Rank the most complex functions with cccc (https://github.com/moznion/cccc);
# what is analyzed is configured in cccc.toml.
complexity:
	cccc --table --top-cognitive 20 .

grafana-com-export:
	$(GO) run ./dashboards/cmd/grafana-com-export .

clean:
	rm -f $(BINARY)

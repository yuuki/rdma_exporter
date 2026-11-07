GO ?= go
PKG := ./...
BINARY := rdma_exporter

# Tool versions. CI pins the same versions (.github/workflows); bump together.
GOLANGCI_LINT_VERSION ?= v2.15.0
GOVULNCHECK_VERSION   ?= v1.9.0
ACTIONLINT_VERSION    ?= v1.7.12
ZIZMOR_VERSION        ?= 1.32.0
FUZZTIME              ?= 30s
BENCH_COUNT           ?= 6

# go run tool@version picks its toolchain from the tool's go.mod, which can be
# older than this module's and then cannot analyze it: use this module's.
GO_TOOLCHAIN := $(shell $(GO) env GOVERSION)
GO_RUN := GOTOOLCHAIN=$(GO_TOOLCHAIN) $(GO) run
# The exporter targets Linux; other platforms build stubs only. golangci-lint
# and govulncheck therefore analyze the Linux build on every host: -exec hands
# GOOS to the tool alone, after go run has built the tool for the host.
GOLANGCI_LINT ?= $(GO_RUN) -exec 'env GOOS=linux' github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
GOVULNCHECK   ?= $(GO_RUN) -exec 'env GOOS=linux' golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
ACTIONLINT    ?= $(GO_RUN) github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)
# uvx or pipx (preinstalled on GitHub-hosted runners) runs the pinned zizmor.
ZIZMOR ?= $(if $(shell command -v uvx 2>/dev/null),uvx zizmor@$(ZIZMOR_VERSION),pipx run zizmor==$(ZIZMOR_VERSION))

# The injected-netlink-reply tests need CAP_NET_ADMIN; use sudo unless root.
SUDO_EXEC ?= $(if $(filter 0,$(shell id -u)),,-exec 'sudo -E')

.PHONY: all build check lint vet fmt lint-actions test test-race test-integration test-fuzz bench vuln complexity clean grafana-com-export

all: build

# go build is incremental, so always run it rather than trust the file's age.
build:
	$(GO) build -o $(BINARY) .

# Local pre-PR gate: what the required CI jobs check, without root or hardware.
check: lint lint-actions test-race

# golangci-lint (linters and gofmt, see .golangci.yml; govet included) and
# go.mod tidiness.
lint:
	$(GOLANGCI_LINT) run
	$(GO) mod tidy -diff

vet:
	$(GO) vet $(PKG)

# Skip the module and build caches that AGENTS.md places under the repository.
fmt:
	gofmt -w $(shell find . -type f -name '*.go' -not -path './.gocache/*' -not -path './.gomodcache/*')

# Lint (actionlint) and audit (zizmor) the GitHub Actions workflows. zizmor's
# online audits need GH_TOKEN; without it they are skipped.
lint-actions:
	$(ACTIONLINT)
	$(ZIZMOR) $(if $(GH_TOKEN),,--offline) .

test:
	$(GO) test $(PKG)

test-race:
	$(GO) test -race $(PKG)

# The Execute and Reconnect cases that inject crafted netlink replies, as
# root; RDMANL_REQUIRE_INJECT=1 makes them fail instead of skip without
# CAP_NET_ADMIN.
test-integration:
	RDMANL_REQUIRE_INJECT=1 $(GO) test -race $(SUDO_EXEC) -run 'TestExecute|TestReconnect' ./internal/rdmanl/

# Run every fuzz target for FUZZTIME; FUZZ_PARALLEL caps the fuzzing workers.
test-fuzz:
	@set -e; for pkg in $$($(GO) list $(PKG)); do \
		targets=$$($(GO) test -list '^Fuzz' $$pkg); \
		for f in $$(printf '%s\n' "$$targets" | grep '^Fuzz'); do \
			echo "== $$pkg $$f"; \
			$(GO) test -run='^$$' -fuzz="^$$f\$$" -fuzztime=$(FUZZTIME) $(if $(FUZZ_PARALLEL),-parallel=$(FUZZ_PARALLEL)) $$pkg; \
		done; \
	done

# Benchmarks into bench.txt; compare two runs with benchstat. BENCHTIME
# (e.g. 100ms) shortens a smoke run.
bench:
	$(GO) test -run='^$$' -bench=. -benchmem -count=$(BENCH_COUNT) $(if $(BENCHTIME),-benchtime=$(BENCHTIME)) $(PKG) > bench.txt; \
		status=$$?; cat bench.txt; exit $$status

# Known vulnerabilities reachable from the code.
vuln:
	$(GOVULNCHECK) $(PKG)

# Rank the most complex functions with cccc (https://github.com/moznion/cccc);
# what is analyzed is configured in cccc.toml.
complexity:
	cccc --table --top-cognitive 20 .

grafana-com-export:
	$(GO) run ./dashboards/cmd/grafana-com-export .

clean:
	rm -f $(BINARY) bench.txt

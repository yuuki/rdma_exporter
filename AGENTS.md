# AGENTS.md

Guide for coding agents working on `rdma_exporter`. User-facing docs are in
`README.md`; architecture and rationale in `docs/design.md`.

Prefix Go commands with `GOCACHE=$(pwd)/.gocache GOMODCACHE=$(pwd)/.gomodcache`
in shared or sandboxed environments (README "Testing"); both directories are in
`.gitignore` and `.dockerignore`.

## 1. Overview

A Prometheus exporter for RDMA NICs (InfiniBand, RoCE, EFA) on Linux. It is
pull-only: every `/metrics` scrape reads the kernel afresh, nothing runs in the
background and nothing is cached across scrapes (except "unsupported" verdicts).

`main.go` (flags, slog, wiring) → `internal/server` (HTTP, scrape timeout) →
`internal/collector` (`prometheus.Collector`) → providers:

- `internal/rdma`: sysfs `class/infiniband/<dev>/ports/<n>/{counters,hw_counters}`, port attributes, SR-IOV PF/VF detection.
- `internal/rdmanl`: NETLINK_RDMA GET/DUMP for optional counters (`cc_*`, traffic) and QP counter sets, on two separate sockets.
- `internal/netdev`: ethtool stats (PFC, buffers, PCIe, PHY) of the port's netdev.

Collectors are flag-gated (`--collector.ethtool`, `--collector.optional-counters`, `--collector.qp-counters`).

## 2. Layout

- `main.go`, `internal/{config,server,collector,rdma,rdmanl,netdev}` (tests alongside).
- `internal/rdma/testdata/sysfs/<scenario>/`: sysfs fixture trees; extend them when modeling new hardware or kernel behaviour.
- `**/testdata/fuzz/<FuzzTarget>/`: fuzz regression inputs (commit new crashers here).
- `dashboards/`: bundled Grafana dashboard, its grafana.com export and the export tool.
- `deploy/`: systemd units (asserted by `deploy/systemd/*_test.go`), counter-enablement script, udev rule.
- `docs/`: `design.md`, `deployment.md`, `metrics-runbook.md`, `releases/`.

## 3. Build

- `make build` → `./rdma_exporter` (`CGO_ENABLED` not needed). Linux only; other platforms build stubs (`*_unsupported.go`).
- Images: `Dockerfile` (multi-stage) and `Dockerfile.goreleaser`; releases are built by GoReleaser (`.goreleaser.yaml`, `.github/workflows/release.yml`).

## 4. Test

| Command | What | Needs |
|---|---|---|
| `make test` | unit tests | nothing (fixtures, stubs) |
| `make test-race` | unit tests with `-race` | nothing |
| `make test-integration` | `TestExecute*`/`TestReconnect*` in `internal/rdmanl` with injected netlink replies, `RDMANL_REQUIRE_INJECT=1` | root (uses `sudo -E` unless root); `TestReconnect…` skips without NETLINK_RDMA (`ib_core`) |
| `make test-fuzz [FUZZTIME=30s] [FUZZ_PARALLEL=n]` | every `Fuzz*` target in turn | nothing |
| `make bench [BENCH_COUNT=6]` | benchmarks → `bench.txt` (compare with `benchstat`) | nothing |

- No test needs RDMA hardware. Tests are table-driven; assert metric text with `prometheus/testutil`; reference fixtures by relative path.
- `internal/server` uses `goleak.VerifyTestMain`: tests must not leave goroutines behind.
- Fuzz targets state properties (round trip, bounds, valid UTF-8 labels, reference model), not just "no panic".

## 5. Static analysis

- `make lint`: golangci-lint (`.golangci.yml`, gofmt included) on the Linux build, plus `go mod tidy -diff`.
- `make lint-actions`: actionlint and zizmor on `.github/` (offline unless `GH_TOKEN` is set).
- `make vuln`: govulncheck. `make complexity`: cccc ranking (informational; CI tracks it with octocov).
- CI also runs CodeQL (`security.yml`). Tool versions are pinned in the Makefile and the workflows; bump them together.

## 6. Conventions

- Logging: `log/slog` with keys `device`, `port`, `duration` (plus `netdev`, `err`). Tool or kernel strings go to logs, never into new labels.
- Errors: wrap with `%w`; compare with `errors.Is` (the cached `ErrQPUnsupported` is intentionally the bare sentinel).
- Emit series through `constMetric` in `internal/collector`, which makes sysfs/netlink label values valid UTF-8; never call `prometheus.MustNewConstMetric` with external strings directly.
- `//nolint` only as `//nolint:<linter> // <reason>`.
- Comments and docs in English; comments say why.
- Commits: headline `type(scope): subject` (imperative); the body must state the motivation (why) in complete sentences. Types: `feat`, `fix`, `docs`, `style`, `refactor`, `test`, `chore`, `ci`, `init`, `rearrange`, `update`.
- New metrics: include a sample `/metrics` snippet in the PR.

## 7. Compatibility contracts

Changing any of these is a breaking change: call it out in the PR and in `docs/releases/`.

- Metric names, types and label sets: `internal/collector/collector.go` (`metricSpecs`, descriptors in `New`) and `netdev.go`; documented in README "Metrics" and `docs/design.md`. Adding a label changes series identity (as `netdev` on `rdma_port_info` did). The dashboards query them (`dashboards/dashboard_test.go`).
- CLI flags and `RDMA_EXPORTER_*` env vars: `internal/config`; README "Configuration". Removed flags must keep failing with their rename message.
- HTTP endpoints: `/metrics`, `/healthz` (defaults), content negotiation via `expfmt`.
- NETLINK_RDMA command and attribute numbers (`internal/rdmanl/attr.go`) are kernel ABI: never renumber.
- Sysfs paths read (`internal/rdma/provider.go`), shipped units (`deploy/systemd`), image name `ghcr.io/yuuki/rdma_exporter`, archive layout.
- `dashboards/rdma_exporter_dashboard.grafana.com.json` is generated: `make grafana-com-export` (a test checks it matches the bundle).

## 8. Concurrency and resources

- `RdmaCollector.Collect` is serialized by `collectMu`; `Describe` reads dynamic descriptors under it.
- The server stores the per-scrape context with `SetContext`/`ResetContext` (atomic pointer) and gathers on a goroutine; at the timeout it answers 504 and that goroutine finishes when the providers return. Providers must honour `ctx`.
- `rdmanl.nlConn.Execute` holds a mutex per request; netlink receives are bounded by `SO_RCVTIMEO` (5 s or the context deadline); an unfinished dump replaces the socket so stale replies cannot be read.
- Providers opened in `main.go` are closed on shutdown; the HTTP server shuts down gracefully on SIGINT/SIGTERM.
- Read-only by design: never write sysfs, never issue `STAT_SET` / `rdma statistic set`, never bind QPs or enable auto mode.

## 9. Performance

- Scrape-time hot paths: sysfs walk (one read per counter file), metric construction, one ethtool `Stats()` per netdev per scrape (cached within the scrape), optional counters only for ports with an enabled counter.
- QP dumps are budgeted (1 MiB or the scrape deadline, 256 messages); the QP collector is off by default.
- Cardinality: labels are bounded by devices, ports, priorities, lanes and QP types. Never export LQPN, PID, `counter_id`, GIDs or IPs as labels.
- Measure with `make bench` (`BenchmarkGather*`, `BenchmarkSysfsProviderDevices`, `BenchmarkParseQPDump`) before and after hot-path changes.

## 10. Before opening a PR

- `make check` (lint, lint-actions, test-race) must pass.
- Touching `internal/rdmanl` client code: `make test-integration`.
- Touching a parser (sysfs, netlink, ethtool names, label building): `make test-fuzz FUZZTIME=1m`.
- Touching metrics: README "Metrics", `docs/design.md`, dashboards (`make grafana-com-export`), sample `/metrics` in the PR.

## 11. Rules for AI agents

Never:
- disable, skip, weaken or delete tests, fuzz properties or lint rules to get green (no blanket excludes, `|| true`, `continue-on-error`);
- edit generated files by hand (`dashboards/*.grafana.com.json`);
- add dependencies without a stated justification (test-only ones included);
- change a public contract (§7) without calling it out;
- loosen workflow permissions, unpin actions or add build caches to the release workflow;
- commit secrets, rewrite history, force-push, tag or publish releases.

## 12. Needs human review even when CI is green

Workflows and release files (`.github/`, `.goreleaser.yaml`, Dockerfiles), permissions and secrets, netlink and sysfs parsing (untrusted input), anything near privileged operations or the read-only rule, public contracts (§7), new dependencies, large refactors.

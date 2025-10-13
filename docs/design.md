# rdma_exporter Design

## 1. Background and Goals
High-performance computing clusters and low-latency trading platforms increasingly rely on RDMA-capable network adapters to reduce CPU overhead and latency. Operators need continuous visibility into link health, error counters, and configuration drift. `rdma_exporter` collects RDMA NIC statistics from Linux hosts and exposes them as Prometheus metrics, providing the following goals:

- Observe InfiniBand and RoCE ports with low operational overhead.
- Follow Prometheus exporter best practices for metric naming, cardinality, and instrumentation.
- Offer predictable performance under concurrent scrape load and degraded hardware states.

## 2. Requirements
- **Platform**: Linux only, running Go 1.22+ binaries.
- **Dependencies**:
  - [`github.com/Mellanox/rdmamap`](https://pkg.go.dev/github.com/Mellanox/rdmamap) for sysfs discovery and statistics.
  - [`github.com/prometheus/client_golang`](https://pkg.go.dev/github.com/prometheus/client_golang) for instrumentation and HTTP handlers.
- **Metrics**:
  - Port-level and hardware counters (`rdma_port_<stat>_total`) aligned with NVIDIA documentation (e.g. `rdma_port_xmit_data_total`, `rdma_port_duplicate_request_total`).
  - Port metadata (`rdma_port_info`) with value `1` and descriptive labels.
  - Exporter health metrics (Go/process collectors, HTTP instrumentation).
- **Service Interface**:
  - HTTP server with configurable listen address and metrics path.
  - Health endpoint returning `200 OK` when scrapes are possible.
  - Structured logging via `log/slog`.
- **Configurability**:
  - Flags for listen address, metrics path, log level, scrape timeout, and optional sysfs root override (for testing or chroot usage).
  - Environment variable overrides mirror CLI flags.
- **Non-Goals**:
  - Windows or FreeBSD support.
  - Active device control or configuration.
  - Aggregation across multiple hosts.

## 3. High-Level Architecture

```
┌────────────────┐
│   main (cmd)   │
│ - parse flags  │
│ - init slog    │
│ - wire deps    │
└──────┬─────────┘
       │
┌──────▼─────────┐      ┌─────────────────────┐
│ HTTP Server    │─────►│ /metrics (promhttp) │
│ - health check │      └─────────────────────┘
│ - graceful stop│
└──────┬─────────┘
       │
┌──────▼─────────┐
│ Collector      │
│ - Describe     │
│ - Collect      │
└──────┬─────────┘
       │
┌──────▼─────────┐
│ RDMA Provider  │
│ - rdmamap API  │
│ - sysfs root   │
└────────────────┘
```

The `cmd/rdma_exporter` package wires configuration, logging, and the HTTP server. The server exposes `/metrics`, `/healthz`, and optional `/readyz` endpoints. The `internal/collector` package implements `prometheus.Collector`, delegating data retrieval to `internal/rdma`, which wraps `rdmamap` for easier mocking and testing.

## 4. Data Flow
1. A scrape hits `/metrics`.
2. The Prometheus handler invokes the registered `RdmaCollector`.
3. `RdmaCollector.Collect` queries `internal/rdma.Provider` for:
   - Host Channel Adapter (HCA) inventory.
   - Per-port standard stats.
   - Per-port hardware stats (if available).
4. The collector transforms each counter into const metrics. All counters become stat-specific metric names prefixed with `rdma_port_` (e.g. `rdma_port_xmit_data_total`, `rdma_port_symbol_errors_total`) with labels `device` and `port`. Metadata metrics add labels like `link_layer`, `state`, `phys_state`, `link_width`, and `link_speed`.
5. Prometheus receives the serialized metrics response.

## 5. Error Handling and Resilience
- **Partial Failures**: When an HCA or port fails to respond, the collector logs a warning and continues with remaining ports. Metrics for failed ports are omitted in that scrape to avoid publishing stale data.
- **Timeouts**: Scrape requests reuse the HTTP request context but `prometheus.Collector` does not accept cancellation. The exporter wraps collection in a goroutine, aggregates results via channels, and respects the context deadline before writing the response. When the deadline is exceeded, it aborts the response and increments an error counter.
- **Initialization**: Startup fails fast on configuration errors (e.g., invalid flags). When no RDMA devices exist, the exporter serves zero port metrics but keeps running.
- **Graceful Shutdown**: The HTTP server listens for SIGINT/SIGTERM, stops accepting new connections, and waits for in-flight scrapes to finish.

## 6. Performance Considerations
- `rdmamap` reads sysfs files; the collector avoids additional caching to keep results up to date. For environments with very frequent scrapes, an optional short-lived cache (e.g., 1–2 seconds) can be enabled behind a flag once needed.
- Concurrency is limited by serializing `Collect` calls using a mutex, preventing overlapping sysfs traversals and avoiding double counting.
- Profiling hooks (`pprof`) are intentionally excluded by default to reduce attack surface but may be added under a gated build tag if required.

## 7. Configuration Interface
- **Flags**:
  - `--listen-address=":9879"`
  - `--metrics-path="/metrics"`
  - `--health-path="/healthz"`
  - `--log-level="info"` (`debug`, `warn`, `error` supported)
  - `--sysfs-root="/sys"`
  - `--scrape-timeout="5s"` (upper bound applied to scrape processing via context and goroutine)
- **Environment Variables**: `RDMA_EXPORTER_LISTEN_ADDRESS`, etc., map one-to-one with flags and provide defaults when flags are unset. CLI flags override environment values to match typical Go flag semantics.
- **Future Config**: A YAML file can be introduced under `config/` for static deployments (e.g., selecting devices).

## 8. Security Considerations
- The exporter runs with minimal privileges but requires read access to `/sys/class/infiniband`. It should run as an unprivileged user with CAP_DAC_READ_SEARCH if necessary.
- TLS termination and authentication are expected to be handled by an external sidecar or ingress controller when exposing metrics beyond localhost.
- No dynamic configuration endpoints are exposed; health endpoints return a simple status payload.

## 9. Observability
- The exporter emits structured logs with `time`, `level`, and `msg`, plus keys like `device`, `port`, and `duration`.
- Scrape durations and HTTP status codes are captured via `promhttp.InstrumentMetricHandler`.
- A custom counter `rdma_scrape_errors_total` tracks failures fetching sysfs data.

## 10. Testing Strategy
- **Unit Tests**:
  - `internal/rdma`: Mock sysfs directory using fixtures; verify mapping of `rdmamap` structures.
  - `internal/collector`: Use `prometheus/testutil` to assert metric contents, including label values.
- **Integration Tests**:
  - Run exporter against recorded sysfs trees under `testdata/sysfs/<scenario>`.
  - Validate HTTP responses and content type using the embedded test server.
- **CI Tasks**:
  - `go test ./...`
  - `go vet ./...`
  - `golangci-lint run` (optional but recommended).

## 11. Deployment Guidance
- Ship a systemd unit and container image reference in the `build/` directory.
- Default scrape interval: 15s; adjust based on infrastructure policy.
- For containerized deployments, mount `/sys/class/infiniband` read-only into the container.

## 12. Future Work
- Aggregate counters at the HCA level (`rdma_device_stat_total`).
- Support event-driven metrics (e.g., link-up changes) using optional polling loops.
- Add `/readyz` endpoint integrating pending configuration validation (e.g., device allow lists).
- Implement on-demand profiling through a dedicated debug build.

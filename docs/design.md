# rdma_exporter Design

This document covers the sysfs-based `rdma_exporter`.

## 1. Background and Goals
High-performance computing clusters and low-latency trading platforms increasingly rely on RDMA-capable network adapters to reduce CPU overhead and latency. Operators need continuous visibility into link health, error counters, and configuration drift. `rdma_exporter` collects RDMA NIC statistics from Linux hosts and exposes them as Prometheus metrics, providing the following goals:

- Observe InfiniBand and RoCE ports with low operational overhead.
- Follow Prometheus exporter best practices for metric naming, cardinality, and instrumentation.
- Offer predictable performance under concurrent scrape load and degraded hardware states.

## 2. Requirements
- **Platform**: Linux only, running Go 1.26.6+ binaries.
- **Dependencies**:
  - [`github.com/prometheus/client_golang`](https://pkg.go.dev/github.com/prometheus/client_golang) for instrumentation and HTTP handlers.
- **Metrics**:
  - Port-level and hardware counters (`rdma_<counter>_total`) aligned with NVIDIA documentation (e.g. `rdma_port_xmit_data_total`, `rdma_symbol_error_total`, `rdma_duplicate_request_total`).
  - Sysfs `hw_counters/lifespan` as `rdma_lifespan_milliseconds` (Gauge, milliseconds). It is a configuration knob, not a cumulative counter.
  - Port metadata (`rdma_port_info`) with value `1` and descriptive labels, including `netdev` from sysfs `gid_attrs/ndevs`.
  - RoCEv2 PFC counters from ethtool (`rdma_roce_pfc_*`), enabled by `--enable-roce-pfc-metrics`.
  - Opt-in ethtool hardware counters (`rdma_netdev_*`, `rdma_pcie_*`, `rdma_phy_*`, including IEEE 802.3x global pause, pause storm, and vport RDMA) behind `--enable-netdev-hw-metrics`.
  - Opt-in optional hardware counters from RDMA netlink (`rdma_optional_counter_enabled`, `rdma_cc_*_total`, `rdma_optional_{rx,tx}_{bytes,packets}_total`) behind `--enable-rdma-optional-counters`. The exporter never enables counters.
  - Opt-in live auto-type QP counters (`rdma_qp_*`) behind `--enable-rdma-qp-counters`. The exporter never binds QPs or enables auto mode. These are not a partition of sysfs port totals.
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
┌──────▼─────────┐      ┌─────────────────────┐
│ Collector      │─────►│ RDMA Provider       │
│ - Describe     │      │ - sysfs root        │
│ - Collect      │      └─────────────────────┘
└──────┬─────────┘
       │
       ├────────────────► NETLINK_RDMA socket A (optional cc_* / traffic)
       ├────────────────► NETLINK_RDMA socket B (QP dump)
       └────────────────► ethtool (optional PFC / netdev HW)
```

The `cmd/rdma_exporter` package wires configuration, logging, and the HTTP server. The server exposes `/metrics`, `/healthz`, and optional `/readyz` endpoints. The `internal/collector` package implements `prometheus.Collector`, delegating sysfs retrieval to `internal/rdma`, optional hardware counters and QP dumps to separate `internal/rdmanl` sockets (NETLINK_RDMA), and PFC/netdev hardware stats to `internal/netdev`.

## 4. Data Flow
1. A scrape hits `/metrics`.
2. The Prometheus handler invokes the registered `RdmaCollector`.
3. `RdmaCollector.Collect` queries `internal/rdma.Provider` for:
   - Host Channel Adapter (HCA) inventory.
   - Per-port standard stats.
   - Per-port hardware stats (if available).
4. The collector transforms each counter into const metrics. Every sysfs counter is published as `rdma_<counter>_total` using the exact counter names from the NVIDIA guide (e.g. `rdma_port_xmit_data_total`, `rdma_symbol_error_total`, `rdma_duplicate_request_total`) with labels `device` and `port`. The exception is `hw_counters/lifespan`, emitted as `rdma_lifespan_milliseconds` (Gauge). Metadata metrics add labels like `netdev`, `link_layer`, `state`, `phys_state`, `link_width`, and `link_speed`.
5. For Ethernet non-VF ports, the collector may also read ethtool stats: PFC families when `--enable-roce-pfc-metrics` is on, and curated buffer/PCIe/PHY/global-pause/pause-storm/vport-RDMA families when `--enable-netdev-hw-metrics` is on. Hardware families are emitted once per netdev. Global pause is IEEE 802.3x and is not mixed into `rdma_roce_pfc_*`. vPort RDMA (`rdma_netdev_vport_rdma_{bytes,packets}_total`) is the function vport of the scraped netdev, not `*_phy` and not a sum of other vports. It is omitted when the device PCI address is not a BDF, when `/sys/bus/pci/devices/<bdf>/sriov_totalvfs` is absent (value ignored), when more than one Ethernet `(device,port)` shares the netdev, or when `phys_port_name` is a VF/SF/host-PF representor (`pf0vf1`, `c1pf0vf0`, `pf0hpf`). A missing `phys_port_name` does not omit (fail-open). `IsVF` stays fail-open and is not used to hide PFC or other HW families on a BDF without `physfn`. Do not add these series to sysfs `port_rcv_data` or `rdma_qp_*` traffic.
6. When `--enable-rdma-optional-counters` is on, the collector dumps RDMA devices over netlink, reads optional-counter status (`STAT_GET_STATUS`, Linux 5.16+), and fetches values (`STAT_GET`) only for ports with at least one enabled optional counter. `rdma_optional_counter_enabled` covers every optional name. `_total` series are emitted for `cc_rx_ce_pkts`, `cc_rx_cnp_pkts`, and `cc_tx_cnp_pkts` via `buildMetricName`, and for netlink `rdma_{rx,tx}_{bytes,packets}` as hardcoded `rdma_optional_{rx,tx}_{bytes,packets}_total` (not `buildMetricName`). Traffic names are link-wide mlx5 optional flow counters (added in Linux 6.15); same-direction packet and byte counters share one hardware flow counter. Skip an optional `_total` when sysfs `hw_counters` already has the raw netlink name. Do not add `rdma_optional_*` to sysfs `port_rcv_data`, `rdma_qp_*`, or `rdma_netdev_vport_rdma_*`. Static hw counters already published from sysfs are not re-exported. The exporter never issues `STAT_SET`. A missing `STAT_GET_STATUS` is treated as unsupported and not retried every scrape.
7. After optional counters finish, `--enable-rdma-qp-counters` uses a second NETLINK_RDMA socket. The collector reads port-level mode (`STAT_GET` DOIT with a `STAT_MODE` sentinel) and dumps live bound sets (`STAT_GET` DUMP). Totals are emitted only for auto + type sets, labeled `{device,port,qp_type}`. LQPN lists, `counter_id`, `auto pid`, and manual sets are not exported as values. Dump receive size is budgeted (1 MiB or remaining scrape deadline, and a message cap); overflow omits that port's totals, sets `rdma_qp_scrape_status{result="overflow"}`, and continues other ports. Sysfs `hw_counters` already include default + running sets + history; `rdma_qp_*` must not be added to `rdma_<name>_total`. Do not add `rdma_optional_*` to `rdma_qp_*` or vice versa. `STAT_GET` is unprivileged; enabling auto type (`rdma statistic qp set … auto type on`) requires `CAP_NET_ADMIN` and must happen before user QPs go INIT. Optional traffic dump keys `rdma_{rx,tx}_{bytes,packets}` are exported as `rdma_qp_{rx,tx}_{bytes,packets}_total` when present; the exporter never enables `optional-counters on`.
8. Prometheus receives the serialized metrics response.

## 5. Error Handling and Resilience
- **Partial Failures**: When an HCA or port fails to respond, the collector logs a warning and continues with remaining ports. Metrics for failed ports are omitted in that scrape to avoid publishing stale data.
- **Timeouts**: Scrape requests reuse the HTTP request context but `prometheus.Collector` does not accept cancellation. The exporter wraps collection in a goroutine, aggregates results via channels, and respects the context deadline before writing the response. When the deadline is exceeded, it aborts the response and increments an error counter.
- **Initialization**: Startup fails fast on configuration errors (e.g., invalid flags). When no RDMA devices exist, the exporter serves zero port metrics but keeps running.
- **Graceful Shutdown**: The HTTP server listens for SIGINT/SIGTERM, stops accepting new connections, and waits for in-flight scrapes to finish.

## 6. Performance Considerations
- The provider reads sysfs files directly; the collector avoids additional caching to keep results up to date. For environments with very frequent scrapes, an optional short-lived cache (e.g., 1–2 seconds) can be enabled behind a flag once needed.
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
  - `--enable-roce-pfc-metrics=true` (RoCEv2 PFC ethtool metrics only)
  - `--enable-netdev-hw-metrics=false` (opt-in buffer/PCIe/PHY/global-pause/pause-storm/vport-RDMA ethtool metrics)
  - `--enable-rdma-optional-counters=false` (NETLINK_RDMA; mlx5 `cc_*` and `rdma_{rx,tx}_{bytes,packets}`; does not enable counters in the kernel)
  - `--enable-rdma-qp-counters=false` (separate NETLINK_RDMA socket; does not bind QPs or enable auto mode)
- **Environment Variables**: `RDMA_EXPORTER_LISTEN_ADDRESS`, etc., map one-to-one with flags and provide defaults when flags are unset. CLI flags override environment values to match typical Go flag semantics.
- **Future Config**: A YAML file can be introduced under `config/` for static deployments (e.g., selecting devices).

## 8. Security Considerations
- The exporter runs with minimal privileges but requires read access to `/sys/class/infiniband`. Optional counters and QP dumps use NETLINK_RDMA get/dump only; enabling optional counters or QP auto mode remains an operator action (`rdma statistic set` / `rdma statistic qp set`, `CAP_NET_ADMIN`). It should run as an unprivileged user with CAP_DAC_READ_SEARCH if necessary.
- TLS termination and authentication are expected to be handled by an external sidecar or ingress controller when exposing metrics beyond localhost.
- No dynamic configuration endpoints are exposed; health endpoints return a simple status payload.

## 9. Observability
- The exporter emits structured logs with `time`, `level`, and `msg`, plus keys like `device`, `port`, and `duration`.
- Scrape durations and HTTP status codes are captured via `promhttp.InstrumentMetricHandler`.
- A custom counter `rdma_scrape_errors_total` tracks failures fetching sysfs data. `rdma_roce_pfc_scrape_errors_total` and `rdma_netdev_scrape_errors_total` track ethtool failures for their respective families. `rdma_optional_counter_scrape_errors_total` tracks NETLINK_RDMA optional-counter failures. `rdma_qp_scrape_errors_total` tracks QP dump/mode failures, including receive-budget overflow.

## 10. Testing Strategy
- **Unit Tests**:
  - `internal/rdma`: Mock sysfs directory using fixtures; verify sysfs parsing.
  - `internal/rdmanl`: Parse crafted netlink attributes; stub the socket for Prepare/Counters and QP mode/dump, including dump receive budgets.
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

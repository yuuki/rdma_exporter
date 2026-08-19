# Prometheus RDMA Exporter

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![CI](https://github.com/yuuki/rdma_exporter/actions/workflows/ci.yml/badge.svg)](https://github.com/yuuki/rdma_exporter/actions/workflows/ci.yml)
[![GitHub release](https://img.shields.io/github/v/release/yuuki/rdma_exporter)](https://github.com/yuuki/rdma_exporter/releases)
[![Go Reference](https://pkg.go.dev/badge/github.com/yuuki/rdma_exporter.svg)](https://pkg.go.dev/github.com/yuuki/rdma_exporter)

For details on GitHub Actions status badges, see the [official documentation](https://docs.github.com/ja/actions/how-tos/monitor-workflows/add-a-status-badge).

`rdma_exporter` collects RDMA (InfiniBand/RoCE) NIC statistics from Linux hosts and exposes them as Prometheus metrics. The exporter walks the kernel's sysfs tree directly and publishes metrics with [`github.com/prometheus/client_golang`](https://pkg.go.dev/github.com/prometheus/client_golang).

## Overview

![rdma_exporter architecture overview](docs/images/architecture-overview.png)

## Features
- Publishes counters from `/sys/class/infiniband/<dev>/<port>/counters` and `/hw_counters` as `rdma_<counter>_total` metrics that match NVIDIA's *Understanding mlx5 Linux Counters and Status Parameters* guide (e.g. `rdma_port_rcv_data_total`, `rdma_symbol_error_total`, `rdma_duplicate_request_total`).
- Exposes port metadata (link layer, state, width, speed, PCI address, VF/PF relationship, etc.) through `rdma_port_info`.
- Tracks scrape failures with `rdma_scrape_errors_total`.
- **Supports device exclusion** (`--exclude-devices`) to prevent kernel log flooding on firmware-restricted devices (NVIDIA DGX, Umbriel, GB200 systems).
- Ships with an HTTP server that serves `/metrics` and `/healthz` and gracefully shuts down on `SIGINT`/`SIGTERM`.
- Supports an alternative sysfs root (`--sysfs-root`) for testing or chroot environments.
- Honors a configurable scrape timeout (`--scrape-timeout`) to protect long-running sysfs reads.
- Optionally enriches RoCEv2 visibility with PFC counters from netdev ethtool stats (Linux only, best effort).
- Optionally exports mlx5 ethtool hardware counters for NIC buffer drops, PCIe stalls, and PHY/FEC (opt-in, `--enable-netdev-hw-metrics`).

## Requirements
- Go 1.26.6 or newer.
- Linux with read access to `/sys/class/infiniband` (for production use).

## Build
```bash
go build -o rdma_exporter .
```

Alternatively, use the provided `Makefile` helpers:

```bash
make build   # compiles ./rdma_exporter
make test    # runs go test ./...
make lint    # runs go vet ./...
```

## Run
```bash
./rdma_exporter \
  --listen-address=":9879" \
  --metrics-path="/metrics" \
  --health-path="/healthz"
```

To exclude specific devices that trigger firmware errors (e.g., on NVIDIA DGX/GB200 systems):
```bash
./rdma_exporter --exclude-devices=mlx5_0,mlx5_1
```

To print build information without starting the server, add `--version`.

## Configuration
Every CLI flag has an equivalent environment variable. Environment values provide defaults; explicit CLI flags take precedence.

| Flag | Environment | Default | Description |
| ---- | ----------- | ------- | ----------- |
| `--listen-address` | `RDMA_EXPORTER_LISTEN_ADDRESS` | `:9879` | HTTP listen address |
| `--metrics-path` | `RDMA_EXPORTER_METRICS_PATH` | `/metrics` | Metrics endpoint path |
| `--health-path` | `RDMA_EXPORTER_HEALTH_PATH` | `/healthz` | Health check endpoint path |
| `--log-level` | `RDMA_EXPORTER_LOG_LEVEL` | `info` | Log verbosity (`debug`, `info`, `warn`, `error`) |
| `--sysfs-root` | `RDMA_EXPORTER_SYSFS_ROOT` | `/sys` | Root directory used to read RDMA sysfs data |
| `--scrape-timeout` | `RDMA_EXPORTER_SCRAPE_TIMEOUT` | `5s` | Upper bound for metric gathering per scrape |
| `--enable-roce-pfc-metrics` | `RDMA_EXPORTER_ENABLE_ROCE_PFC_METRICS` | `true` | Enable RoCEv2 PFC metric collection from netdev ethtool stats (Linux only). Does not enable buffer/PCIe/PHY counters. |
| `--enable-netdev-hw-metrics` | `RDMA_EXPORTER_ENABLE_NETDEV_HW_METRICS` | `false` | Enable ethtool hardware counters for NIC buffer drops, PCIe stalls, and PHY/FEC (Linux only, opt-in). |
| `--exclude-devices` | `RDMA_EXPORTER_EXCLUDE_DEVICES` | `` | Comma-separated list of RDMA devices to exclude (e.g., `mlx5_0,mlx5_1`) |

## Metrics
- `rdma_<counter>_total{device,port}` – Port and hardware counters aligned with NVIDIA documentation (e.g. `rdma_port_rcv_data_total`, `rdma_symbol_error_total`, `rdma_duplicate_request_total`).
- `rdma_port_info{device,port,link_layer,state,phys_state,link_width,link_speed,pci_addr,is_vf,pf_device}` – Gauge set to `1` with descriptive labels. `pci_addr` carries the device's PCI address (e.g. `0000:1a:00.0`); `is_vf` is `"true"` for SR-IOV virtual functions; `pf_device` names the parent PF IB device when `is_vf="true"` (empty otherwise). These enable joins with external sources keyed by PCI address (e.g. `sriov_kubepoddevice`) for per-VF/per-pod RDMA bandwidth attribution.
- `rdma_scrape_errors_total{}` – Counter incremented when sysfs collection fails.
- `rdma_roce_pfc_pause_frames_total{device,port,netdev,direction,priority}` – RoCEv2 PFC pause frames from ethtool stats.
- `rdma_roce_pfc_pause_duration_total{device,port,netdev,direction,priority}` – Cumulative PFC pause duration in **microseconds**. Occupancy is `rate(...[$interval]) / 1e6`.
- `rdma_roce_pfc_pause_transitions_total{device,port,netdev,direction,priority}` – PFC XOFF-to-XON transitions (mlx5 exposes receive/`rx` only).
- `rdma_roce_pfc_scrape_errors_total{}` – Counter incremented when PFC ethtool collection fails.
- `rdma_netdev_*` / `rdma_pcie_*` / `rdma_phy_*` – Opt-in ethtool hardware counters (`--enable-netdev-hw-metrics`). See below.
- `rdma_netdev_scrape_errors_total{}` – Counter incremented when netdev hardware ethtool collection fails.

The Go and process collectors from `client_golang` are registered automatically.

PFC and ethtool hardware metrics are collected only for Ethernet physical functions. Physical-port counters on a PF include VF traffic. Series are emitted only for counters the driver actually returns; missing priorities do not mean “zero pauses”.

### PFC direction

These are observations, not root-cause labels:

- `direction="rx"`: the peer XOFFed this NIC, so this NIC cannot transmit on that priority.
- `direction="tx"`: this NIC XOFFed the peer because it is not absorbing that priority.

Treat a sustained `rx` pause as a reason to inspect the network path, and a sustained `tx` pause as a reason to inspect NIC receive buffers, PCIe, and the host. Do not read `tx` pause as switch congestion.

Pause occupancy:

```promql
rate(rdma_roce_pfc_pause_duration_total[$interval]) / 1e6
```

### Netdev hardware counters (opt-in)

Enable with `--enable-netdev-hw-metrics`. These are mlx5 netdev/device statistics correlated to an RDMA port via `netdev`; they are not RoCE-only.

- Buffer/drop: `rdma_netdev_prio_buf_discard_total`, `rdma_netdev_prio_cong_discard_total`, `rdma_netdev_prio_discards_total`, `rdma_netdev_prio_ecn_marked_total`, `rdma_netdev_dev_out_of_buffer_total`, `rdma_netdev_rx_out_of_buffer_total`, `rdma_netdev_rx_discards_phy_total`. Distinct from the sysfs QP WQE counter `rdma_out_of_buffer_total`.
- PCIe: `rdma_pcie_outbound_stalled_percent` is a **gauge** of the last 1 second (kernel 0–100) and can miss stalls shorter than the scrape interval. Alert on `rate(rdma_pcie_outbound_stalled_seconds_total[$interval])`, the fraction of time stall exceeded 30%. Also `rdma_pcie_outbound_buffer_overflow_total` and `rdma_pcie_signal_integrity_total`.
- PHY/FEC: `rdma_phy_rx_corrected_bits_total`, `rdma_phy_rx_pcs_symbol_err_total`, `rdma_phy_rx_bits_total`, `rdma_phy_rx_err_lane_total`, `rdma_phy_rx_crc_errors_total`, `rdma_phy_link_down_events_total`.

Interval FEC/BER ratio (not a lifetime or instantaneous instrument):

```promql
increase(rdma_phy_rx_corrected_bits_total[$interval])
  /
clamp_min(increase(rdma_phy_rx_bits_total[$interval]), 1)
```

Use the same form with `rdma_phy_rx_pcs_symbol_err_total` for uncorrected symbol errors.

Enabling these counters adds one series per present allowlisted ethtool key (not a full `ethtool -S` dump). Disable with `--enable-netdev-hw-metrics=false`.

### RoCEv2 congestion control

Default sysfs `hw_counters` already export Notification Point / Reaction Point counters (`rdma_np_cnp_sent_total`, `rdma_np_ecn_marked_roce_packets_total`, `rdma_rp_cnp_handled_total`, `rdma_rp_cnp_ignored_total`). mlx5 optional counters (`cc_rx_ce_pkts`, `cc_rx_cnp_pkts`, `cc_tx_cnp_pkts`) are **not** exposed in sysfs even when enabled with `rdma statistic set`; this exporter does not read them yet.

## Dashboards
- Bundled JSON: [`dashboards/rdma_exporter_dashboard.json`](dashboards/rdma_exporter_dashboard.json) includes PFC occupancy, PCIe stall, and PHY/FEC panels. PCIe/PHY panels need `--enable-netdev-hw-metrics`.
- Grafana.com: [RDMA/RoCE NIC Telemetry](https://grafana.com/grafana/dashboards/24241-rdma-roce-nic-telemetry/) – community copy; updating that listing is a separate publish step.

## Testing
```bash
go test ./...
```

For deterministic builds in shared environments, you can pin Go's caches locally:

```bash
GOCACHE=$(pwd)/.gocache GOMODCACHE=$(pwd)/.gomodcache go test ./...
```

`internal/rdma/testdata/sysfs` contains fixture trees used in unit tests to emulate sysfs layouts.

## Deployment
- A systemd unit file is available under `deploy/systemd/rdma_exporter.service`.
- A multi-stage Dockerfile lives at the repository root; see `docs/deployment.md` for build and run instructions.

## Development Notes
- Architectural decisions and future work are documented in `docs/design.md`.
- Logging uses the Go standard library `log/slog`. Set `--log-level=debug` for detailed scrape traces.
- Deployment guidance (systemd and container) lives in `docs/deployment.md`.

## License
This project is licensed under the MIT License. See `LICENSE` for full text.

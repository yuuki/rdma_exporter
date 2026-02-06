# Prometheus RDMA Exporter

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![CI](https://github.com/yuuki/rdma_exporter/actions/workflows/ci.yml/badge.svg)](https://github.com/yuuki/rdma_exporter/actions/workflows/ci.yml)
[![GitHub release](https://img.shields.io/github/v/release/yuuki/rdma_exporter)](https://github.com/yuuki/rdma_exporter/releases)
[![Go Reference](https://pkg.go.dev/badge/github.com/yuuki/rdma_exporter.svg)](https://pkg.go.dev/github.com/yuuki/rdma_exporter)

For details on GitHub Actions status badges, see the [official documentation](https://docs.github.com/ja/actions/how-tos/monitor-workflows/add-a-status-badge).

`rdma_exporter` collects RDMA (InfiniBand/RoCE) NIC statistics from Linux hosts and exposes them as Prometheus metrics. The exporter walks the kernel's sysfs tree directly and publishes metrics with [`github.com/prometheus/client_golang`](https://pkg.go.dev/github.com/prometheus/client_golang).

## Features
- Publishes counters from `/sys/class/infiniband/<dev>/<port>/counters` and `/hw_counters` as `rdma_<counter>_total` metrics that match NVIDIA's *Understanding mlx5 Linux Counters and Status Parameters* guide (e.g. `rdma_port_rcv_data_total`, `rdma_symbol_error_total`, `rdma_duplicate_request_total`).
- Exposes port metadata (link layer, state, width, speed, etc.) through `rdma_port_info`.
- Tracks scrape failures with `rdma_scrape_errors_total`.
- **Supports device exclusion** (`--exclude-devices`) to prevent kernel log flooding on firmware-restricted devices (NVIDIA DGX, Umbriel, GB200 systems).
- Ships with an HTTP server that serves `/metrics` and `/healthz` and gracefully shuts down on `SIGINT`/`SIGTERM`.
- Supports an alternative sysfs root (`--sysfs-root`) for testing or chroot environments.
- Honors a configurable scrape timeout (`--scrape-timeout`) to protect long-running sysfs reads.
- Optionally enriches RoCEv2 visibility with PFC counters from netdev ethtool stats (Linux only, best effort).

## Requirements
- Go 1.25 or newer.
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
| `--enable-roce-pfc-metrics` | `RDMA_EXPORTER_ENABLE_ROCE_PFC_METRICS` | `true` | Enable RoCEv2 PFC metric collection from netdev ethtool stats (Linux only) |
| `--exclude-devices` | `RDMA_EXPORTER_EXCLUDE_DEVICES` | `` | Comma-separated list of RDMA devices to exclude (e.g., `mlx5_0,mlx5_1`) |

## Metrics
- `rdma_<counter>_total{device,port}` – Port and hardware counters aligned with NVIDIA documentation (e.g. `rdma_port_rcv_data_total`, `rdma_symbol_error_total`, `rdma_duplicate_request_total`).
- `rdma_port_info{device,port,link_layer,state,phys_state,link_width,link_speed}` – Gauge set to `1` with descriptive labels.
- `rdma_scrape_errors_total{}` – Counter incremented when sysfs collection fails.
- `rdma_roce_pfc_pause_frames_total{device,port,netdev,direction,priority}` – RoCEv2 PFC pause frame counters from ethtool stats.
- `rdma_roce_pfc_pause_duration_total{device,port,netdev,direction,priority}` – RoCEv2 PFC pause duration counters from ethtool stats.
- `rdma_roce_pfc_pause_transitions_total{device,port,netdev,direction,priority}` – RoCEv2 PFC pause transition counters from ethtool stats.
- `rdma_roce_pfc_scrape_errors_total{}` – Counter incremented when PFC metric collection fails.

The Go and process collectors from `client_golang` are registered automatically.

## Dashboards
- Grafana dashboard: [RDMA/RoCE NIC Telemetry](https://grafana.com/grafana/dashboards/24241-rdma-roce-nic-telemetry/) – Prebuilt panels for visualizing the exporter metrics, helpful for quick validation and long-term monitoring.

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
- A multi-stage Dockerfile lives in `deploy/docker/Dockerfile`; see `docs/deployment.md` for build and run instructions.

## Development Notes
- Architectural decisions and future work are documented in `docs/design.md`.
- Logging uses the Go standard library `log/slog`. Set `--log-level=debug` for detailed scrape traces.
- Deployment guidance (systemd and container) lives in `docs/deployment.md`.

## License
This project is licensed under the MIT License. See `LICENSE` for full text.

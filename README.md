# Prometheus RDMA Exporter

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

`rdma_exporter` collects RDMA (InfiniBand/RoCE) NIC statistics from Linux hosts and exposes them as Prometheus metrics. It uses [`github.com/Mellanox/rdmamap`](https://pkg.go.dev/github.com/Mellanox/rdmamap) to traverse the sysfs tree and [`github.com/prometheus/client_golang`](https://pkg.go.dev/github.com/prometheus/client_golang) to publish metrics.

## Features
- Publishes port-level counters from `/sys/class/infiniband/<dev>/<port>/counters` via `rdma_port_stat_total`.
- Publishes hardware counters from `/sys/class/infiniband/<dev>/<port>/hw_counters` via `rdma_port_hw_stat_total`.
- Exposes port metadata (link layer, state, width, speed, etc.) through `rdma_port_info`.
- Tracks scrape failures with `rdma_scrape_errors_total`.
- Ships with an HTTP server that serves `/metrics` and `/healthz`.
- Supports an alternative sysfs root (`--sysfs-root`) for testing or chroot environments.

## Requirements
- Go 1.25 or newer.
- Linux with read access to `/sys/class/infiniband` (for production use).

## Build
```bash
go build -o bin/rdma_exporter ./cmd/rdma_exporter
```

## Run
```bash
./bin/rdma_exporter \
  --listen-address=":9879" \
  --metrics-path="/metrics" \
  --health-path="/healthz"
```

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

## Metrics
- `rdma_port_stat_total{device,port,stat}` – Standard counters (e.g. `port_xmit_data`, `port_rcv_data`).
- `rdma_port_hw_stat_total{device,port,stat}` – Hardware counters from `hw_counters`.
- `rdma_port_info{device,port,link_layer,state,phys_state,link_width,link_speed}` – Gauge set to `1` with descriptive labels.
- `rdma_scrape_errors_total` – Counter incremented when sysfs collection fails.

The Go and process collectors from `client_golang` are registered automatically.

## Testing
```bash
go test ./...
```

`internal/rdma/testdata/sysfs` contains fixture trees used in unit tests to emulate sysfs layouts.

## Development Notes
- Architectural decisions and future work are documented in `docs/design.md`.
- Logging uses the Go standard library `log/slog`. Set `--log-level=debug` for detailed scrape traces.
- Packaging artifacts such as container images or systemd units can be added under a future `build/` directory.

## License
This project is licensed under the MIT License. See `LICENSE` for full text.

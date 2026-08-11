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

## Requirements
- Go 1.26.2 or newer.
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
- `rdma_port_info{device,port,link_layer,state,phys_state,link_width,link_speed,pci_addr,is_vf,pf_device}` – Gauge set to `1` with descriptive labels. `pci_addr` carries the device's PCI address (e.g. `0000:1a:00.0`); `is_vf` is `"true"` for SR-IOV virtual functions; `pf_device` names the parent PF IB device when `is_vf="true"` (empty otherwise). These enable joins with external sources keyed by PCI address (e.g. `sriov_kubepoddevice`) for per-VF/per-pod RDMA bandwidth attribution.
- `rdma_scrape_errors_total{}` – Counter incremented when sysfs collection fails.
- `rdma_roce_pfc_pause_frames_total{device,port,netdev,direction,priority}` – RoCEv2 PFC pause frame counters from ethtool stats.
- `rdma_roce_pfc_pause_duration_total{device,port,netdev,direction,priority}` – RoCEv2 PFC pause duration counters from ethtool stats.
- `rdma_roce_pfc_pause_transitions_total{device,port,netdev,direction,priority}` – RoCEv2 PFC pause transition counters from ethtool stats.
- `rdma_roce_pfc_scrape_errors_total{}` – Counter incremented when PFC metric collection fails.

The Go and process collectors from `client_golang` are registered automatically.

## mlxlink_exporter

This repository also ships a second exporter, `mlxlink_exporter`, which publishes physical link and optical module telemetry that NVIDIA's `mlxlink` reports: bit error ratios, per-lane raw errors, FEC error histograms, SerDes transmitter tuning, transceiver diagnostics (temperature, voltage, bias current, optical power) and module inventory.

It is a separate binary because `mlxlink` is expensive: on the verified MFT 4.34.1 system, the baseline query took 0.77–0.78 s of wall time and most of that cost was fixed firmware-access overhead. The production query, `mlxlink -d <device> -m -c --rx_fec_histogram --show_histogram --show_serdes_tx --json`, took 0.83 s, adding only about 0.05–0.06 s. This is far too slow to run inside a Prometheus scrape, so a background poller runs the production query once per device per `--poll-interval` and publishes the decoded result into an immutable in-memory snapshot; `/metrics` only reads that snapshot. Scrape frequency therefore has no effect on how often `mlxlink` runs, and the two exporters stay in separate processes so that a firmware hang or an extra privilege never affects `rdma_exporter`.

The default listen address is `:9880`.

### Requirements
- Linux only.
- [NVIDIA MFT](https://network.nvidia.com/products/adapter-software/firmware-tools/) installed, providing the `mlxlink` binary (`--mlxlink-path`, default `/usr/bin/mlxlink`). The exporter exits with status 1 at start-up if that path does not exist.
- Devices are addressed by their IB device name (`mlxlink -d mlx5_0`), so `mst start` and `/dev/mst/*` device nodes are **not** required.
- Read access to `/sys/class/infiniband` for device discovery.
- Verified against MFT 4.34.1 output. Decoder tests use the captured baseline response `internal/mlxlink/testdata/mlxlink/mft-4.34.1-400g-dr4.json` and the captured combined-query response `internal/mlxlink/testdata/mlxlink/mft-4.34.1-400g-fec-serdes.json`.

### Build and run
```bash
make build   # compiles ./rdma_exporter and ./mlxlink_exporter
```

```bash
./mlxlink_exporter \
  --listen-address=":9880" \
  --mlxlink-path="/usr/bin/mlxlink" \
  --poll-interval=30s
```

Recommended settings: keep `--poll-interval=30s` and scrape every 15–30 s. Scraping more often is free, because a scrape never executes `mlxlink`; only `--poll-interval` controls how often the tool runs. A sweep visits every device sequentially, so with N devices one normal sweep costs about N × 0.83 s on the verified hardware. A baseline fallback after a combined query exits non-zero adds another invocation for the affected device.

To print build information without starting the server, add `--version`.

### Configuration
Every flag except `--version` has an equivalent environment variable, ten in total. Environment values provide defaults; explicit CLI flags take precedence.

| Flag | Environment | Default | Description |
| ---- | ----------- | ------- | ----------- |
| `--listen-address` | `MLXLINK_EXPORTER_LISTEN_ADDRESS` | `:9880` | HTTP listen address |
| `--metrics-path` | `MLXLINK_EXPORTER_METRICS_PATH` | `/metrics` | Metrics endpoint path |
| `--health-path` | `MLXLINK_EXPORTER_HEALTH_PATH` | `/healthz` | Liveness endpoint path, always `200 OK` |
| `--ready-path` | `MLXLINK_EXPORTER_READY_PATH` | `/readyz` | Readiness endpoint path, `503` until one device has been collected |
| `--log-level` | `MLXLINK_EXPORTER_LOG_LEVEL` | `info` | Log verbosity (`debug`, `info`, `warn`, `error`) |
| `--mlxlink-path` | `MLXLINK_EXPORTER_MLXLINK_PATH` | `/usr/bin/mlxlink` | Path to the `mlxlink` binary |
| `--sysfs-root` | `MLXLINK_EXPORTER_SYSFS_ROOT` | `/sys` | Root directory used to discover RDMA devices |
| `--poll-interval` | `MLXLINK_EXPORTER_POLL_INTERVAL` | `30s` | Interval between background sweeps over all devices |
| `--command-timeout` | `MLXLINK_EXPORTER_COMMAND_TIMEOUT` | `3s` | Maximum duration of a single `mlxlink` invocation |
| `--exclude-devices` | `MLXLINK_EXPORTER_EXCLUDE_DEVICES` | `` | Comma-separated list of RDMA devices to skip (e.g., `mlx5_0,mlx5_1`) |
| `--version` | – | `false` | Print build information and exit |

### Metrics
All families carry the labels `device`, `port` and `pci_addr`; per-lane families add `lane`. **Lane numbers start at 0**: a lane number is the index of the value within the list `mlxlink` reports.

Link and inventory:
- `mlxlink_link_info{device,port,pci_addr,state,physical_state,speed,width,fec,auto_negotiation}` – Gauge set to `1` with the port's operational attributes as labels. Not published when every attribute is empty.
- `mlxlink_module_info{device,port,pci_addr,identifier,vendor,part_number,serial_number,revision,firmware_version,active_host_compliance,active_media_compliance,cable_type}` – Gauge set to `1` with the transceiver inventory as labels. `firmware_version` is the module firmware, not the adapter firmware. Not published when every attribute is empty.

Physical layer counters:
- `mlxlink_effective_physical_errors_total{device,port,pci_addr}` – Effective physical errors.
- `mlxlink_raw_physical_errors_total{device,port,pci_addr,lane}` – Raw physical errors per lane.
- `mlxlink_link_down_total{device,port,pci_addr}` – Link down events.
- `mlxlink_link_error_recovery_total{device,port,pci_addr}` – Link error recovery events.

FEC histogram counters:
- `mlxlink_rx_fec_codewords_total{device,port,pci_addr,bin,error_count_min,error_count_max}` – Counter of received FEC codewords in each corrected-error range reported by `mlxlink`. A vendor range `[N]` is exported with equal `error_count_min` and `error_count_max`; `[low:high]` preserves both inclusive bounds. The vendor bins are disjoint, not cumulative Prometheus histogram buckets, so queries must sum the desired ranges explicitly. The histogram can be cleared with `mlxlink --clear_histogram`, which this exporter never invokes, and may also reset with the adapter, hardware or firmware.

SerDes transmitter tuning (gauges with vendor-defined tuning codes; the verified output provides no physical units):
- `mlxlink_serdes_tx_fir_coefficient{device,port,pci_addr,lane,tap}` – Transmitter FIR coefficient. `tap` is allowlisted to `pre3`, `pre2`, `pre1`, `main` or `post1`; unknown vendor parameters are not exported.
- `mlxlink_serdes_tx_drive_amplitude{device,port,pci_addr,lane}` – Transmitter drive-amplitude tuning code.

Bit error ratios (gauges, dimensionless):
- `mlxlink_effective_physical_ber{device,port,pci_addr}` – Effective physical BER.
- `mlxlink_raw_physical_ber{device,port,pci_addr}` – Raw physical BER.
- `mlxlink_raw_physical_ber_lane{device,port,pci_addr,lane}` – Raw physical BER per lane.

Digital diagnostic monitoring (gauges). Only the two units `mlxlink` reports with a milli prefix are converted; temperature and optical power are exported exactly as reported:
- `mlxlink_module_temperature_celsius{device,port,pci_addr}` – Module temperature in degrees Celsius.
- `mlxlink_module_voltage_volts{device,port,pci_addr}` – Module supply voltage in volts, converted from the millivolts `mlxlink` reports.
- `mlxlink_module_bias_current_amperes{device,port,pci_addr,lane}` – Laser bias current in amperes, converted from milliamperes.
- `mlxlink_module_rx_power_dbm{device,port,pci_addr,lane}` – Received optical power in dBm.
- `mlxlink_module_tx_power_dbm{device,port,pci_addr,lane}` – Transmitted optical power in dBm.

Fault and state flags (gauges, `0` or `1`):
- `mlxlink_module_fw_fault{device,port,pci_addr}` – Module firmware fault.
- `mlxlink_datapath_fw_fault{device,port,pci_addr}` – Datapath firmware fault.
- `mlxlink_tx_fault{device,port,pci_addr,lane}` – Transmitter fault.
- `mlxlink_tx_los{device,port,pci_addr,lane}` – Transmitter loss of signal.
- `mlxlink_rx_los{device,port,pci_addr,lane}` – Receiver loss of signal.
- `mlxlink_tx_cdr_loss_of_lock{device,port,pci_addr,lane}` – Transmitter CDR loss of lock.
- `mlxlink_rx_cdr_loss_of_lock{device,port,pci_addr,lane}` – Receiver CDR loss of lock.
- `mlxlink_datapath_active{device,port,pci_addr,lane}` – `1` only when the lane reports `DPActivated`.

Exporter self-monitoring:
- `mlxlink_collector_up{device,port,pci_addr}` – `1` when the most recent poll of that device succeeded, `0` otherwise.
- `mlxlink_collection_duration_seconds{device,port,pci_addr}` – Duration of the latest collection attempt, including both invocations when a baseline fallback runs.
- `mlxlink_collection_last_success_timestamp_seconds{device,port,pci_addr}` – Unix timestamp of the last successful collection. Not published for a device that has never succeeded, so the series never reports 1970.
- `mlxlink_collection_errors_total{device,port,pci_addr,reason}` – Collection failure events, including a combined-query error recovered by baseline fallback and an approximate overlap event. `reason` is one of `timeout`, `command_not_found`, `permission_denied`, `exit_error`, `invalid_json`, `output_too_large`, `overlapping`, `unknown`.

A value that `mlxlink` reports as `N/A` produces no sample at all rather than a zero. For the measurement families (BER, temperature, voltage, bias current, optical power, raw errors) only the affected lane is dropped. The flag families above are all-or-nothing: if any lane of `mlxlink_tx_fault` and friends is unreadable, or reports anything other than `0`/`1`, the whole family is omitted for that port rather than published with renumbered lanes.

### Operational notes
- **Physical counters can be cleared.** The physical counters live in the adapter firmware and can be reset by other tooling with `mlxlink --clear_counters`, as well as by firmware resets or link training. `mlxlink` reports how long ago that happened as `Time Since Last Clear [Min]`, which this exporter does not export.
- **The FEC histogram has separate reset semantics.** `mlxlink --clear_histogram` clears the histogram occurrences independently of `--clear_counters`; adapter, hardware or firmware resets may also return them to zero. The exporter invokes neither explicit reset operation, and operators should not run them merely to monitor the link because doing so destroys counter history. `rate()` and `increase()` detect a reset, but a reset inside an evaluation window still hides the errors that preceded it, so treat a sudden return to zero as "the histogram was reset", not "errors stopped".
- **A non-zero combined query falls back to baseline telemetry.** When the combined query exits non-zero, the poller retries `mlxlink -d <device> -m -c --json`. A successful fallback refreshes the base metrics, omits FEC histogram and SerDes metrics, and reports `mlxlink_collector_up=1`; once the fallback completes without shutdown cancellation, the rejected combined query also increments `mlxlink_collection_errors_total{reason="exit_error"}`. If the fallback fails, the previous snapshot is retained and the normal staleness rules apply. Other combined-query failures, including timeouts, do not trigger the fallback, and shutdown cancellation is not counted.
- **Stale data is suppressed.** If a device has not been collected successfully for longer than `--poll-interval` × 5 (150 s by default), its measurement series stop being exported while the self-monitoring series continue. This is what distinguishes "the link is fine" from "we stopped being able to ask".
- **Overlap accounting is approximate.** If a sweep takes longer than `--poll-interval`, the skipped tick is recorded as `mlxlink_collection_errors_total{reason="overlapping"}`. Go tickers coalesce missed ticks, so a sweep that overruns several intervals is still counted once: use the metric to detect that the interval is too short, not to count exactly how many sweeps were lost.
- **Containers are not supported.** `mlxlink` is part of MFT and talks to the adapter firmware, so it is deliberately absent from the published container images (`dockers` in `.goreleaser.yaml` builds only `rdma_exporter`). Run `mlxlink_exporter` on the host.
- **Multi-port adapters.** `mlxlink` is invoked once per device without `-p`, so only the lowest port number of a device is collected.
- **A host with no RDMA devices** stays at `503` on `/readyz` forever, by design: there is nothing to collect. `/healthz` remains `200`.
- **Eye telemetry is not implemented.** One MFT 4.34.1 observation completed `--show_eye` in 0.33 s and returned FOM and grade fields rather than the previously expected height and phase fields, so a dedicated slow poller is not yet justified. Qualification across additional hardware and output formats is tracked in [Issue #24](https://github.com/yuuki/rdma_exporter/issues/24).
- Running unprivileged, and how to grant only the privileges your host actually needs, is covered in [docs/deployment.md](docs/deployment.md).

### Joining with rdma_exporter metrics
The two exporters are separate scrape targets (`:9879` and `:9880`), so their `instance` labels never match and the default vector matching cannot be used. Join on the identity labels both sides carry, `device`, `port` and `pci_addr`, with `on(...)` so that `instance` and `job` are ignored:

```promql
# Raw BER annotated with link layer and PF/VF identity from rdma_port_info.
mlxlink_raw_physical_ber
  * on(device, port, pci_addr) group_left(link_layer, link_speed, is_vf, pf_device)
  rdma_port_info
```

Across more than one host this form is wrong: the same `device`/`port`/`pci_addr` exists on every machine, so it would match series from other hosts. Either add a host label to both jobs with `relabel_configs`, or derive one in the query:

```promql
label_replace(mlxlink_module_temperature_celsius, "host", "$1", "instance", "([^:]+):.*")
  * on(host, device, port, pci_addr) group_left(link_layer, is_vf, pf_device)
label_replace(rdma_port_info, "host", "$1", "instance", "([^:]+):.*")
```

Be careful about which labels `group_left` carries over. `rdma_port_info` exposes `link_layer`, `state`, `phys_state`, `link_width`, `link_speed`, `is_vf` and `pf_device`; of these, `state` also exists on `mlxlink_link_info`. Carrying it does not raise an error — the value from `rdma_port_info` silently replaces the mlxlink one, so the result claims a sysfs port state under a metric named for the mlxlink state. Leave `state` out, or rename it first:

```promql
mlxlink_link_info
  * on(device, port, pci_addr) group_left(sysfs_state)
  label_replace(rdma_port_info, "sysfs_state", "$1", "state", "(.*)")
```

The value families (`mlxlink_raw_physical_ber`, `mlxlink_module_*`, …) only carry `device`, `port`, `pci_addr` and `lane`, so any of the labels above are safe there.

The `instance` regex above assumes a `host:port` target. Targets written as bracketed IPv6 (`[2001:db8::1]:9880`) need a different pattern, for example `(\[.*\]|[^:]+):.*`.

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
- A multi-stage Dockerfile lives at the repository root; see `docs/deployment.md` for build and run instructions.

## Development Notes
- Architectural decisions and future work are documented in `docs/design.md`.
- Logging uses the Go standard library `log/slog`. Set `--log-level=debug` for detailed scrape traces.
- Deployment guidance (systemd and container) lives in `docs/deployment.md`.

## License
This project is licensed under the MIT License. See `LICENSE` for full text.

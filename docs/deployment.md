# Deployment

This guide explains two supported deployment options for `rdma_exporter`: running it as a systemd service on a Linux host, and running it from a container image built with the provided Dockerfile.

## Prerequisites

- A Linux host with RDMA devices accessible through `/sys/class/infiniband`.
- A dedicated, unprivileged user account (the examples use `rdma_exporter`).
- Prometheus or another monitoring system configured to scrape the exporter.

## systemd service

1. **Install the binary**
   ```bash
   sudo install -Dm0755 rdma_exporter /usr/local/bin/rdma_exporter
   ```
   Replace `rdma_exporter` with the path to your compiled binary if different.

2. **Create the service user**
   ```bash
   sudo useradd --system --home /var/lib/rdma_exporter --shell /usr/sbin/nologin rdma_exporter
   ```

3. **Optional: set environment defaults**
   ```bash
   sudo install -Dm0644 /dev/null /etc/rdma_exporter.env
   echo 'RDMA_EXPORTER_LISTEN_ADDRESS=:9879' | sudo tee -a /etc/rdma_exporter.env
   echo 'RDMA_EXPORTER_LOG_LEVEL=info' | sudo tee -a /etc/rdma_exporter.env
   ```
   Adjust the environment variables to match your deployment.

4. **Install the unit file**
   ```bash
   sudo install -Dm0644 deploy/systemd/rdma_exporter.service \
     /etc/systemd/system/rdma_exporter.service
   ```

5. **Reload systemd and start the service**
   ```bash
   sudo systemctl daemon-reload
   sudo systemctl enable --now rdma_exporter.service
   ```

6. **Verify the service**
   ```bash
   systemctl status rdma_exporter.service
   curl -f http://localhost:9879/metrics
   ```

The unit ships with conservative hardening defaults (`ProtectSystem=strict`, `NoNewPrivileges=true`, etc.) and records state under `/var/lib/rdma_exporter`, which systemd creates automatically through `StateDirectory=`.

## Docker image

The repository includes a multi-stage Dockerfile at the repository root.

1. **Build the image**
   ```bash
   docker build -t rdma-exporter:latest .
   ```

2. **Run the exporter**
   ```bash
   docker run --rm \
     --name rdma-exporter \
     --network host \
     --read-only \
     -v /sys/class/infiniband:/sys/class/infiniband:ro \
     -e RDMA_EXPORTER_LOG_LEVEL=info \
     rdma-exporter:latest
   ```

   `--network host` keeps the default listen address. Alternatively, expose port `9879/tcp` explicitly with `-p 9879:9879` and set `--listen-address=:9879` or another value fitting your environment.

3. **Persist configuration** (optional)

   For custom environments, mount an environment file:
   ```bash
   docker run --rm \
     --env-file ./rdma_exporter.env \
     --network host \
     -v /sys/class/infiniband:/sys/class/infiniband:ro \
     rdma-exporter:latest
   ```

The image runs as the unprivileged `rdma_exporter` user by default and contains only the exporter binary plus CA certificates.

## mlxlink_exporter (systemd service)

`mlxlink_exporter` is a second binary that polls NVIDIA's `mlxlink` in the background and serves the result from a cache. It is deployed on the host only: `mlxlink` is part of MFT and talks to the adapter firmware, so it is deliberately absent from the container images and **there is no supported container deployment**. See [mlxlink_exporter_design.md](mlxlink_exporter_design.md) for the reasoning.

### Prerequisites

- NVIDIA MFT installed, providing `mlxlink` (default path `/usr/bin/mlxlink`). The exporter refuses to start if that path does not exist. Devices are addressed by IB device name, so `mst start` is not required.
- A dedicated, unprivileged user account (the examples use `mlxlink_exporter`). Do not start from `root`; see "Verify the privileges" below.

### Install

1. **Install the binary**
   ```bash
   sudo install -Dm0755 mlxlink_exporter /usr/local/bin/mlxlink_exporter
   ```

2. **Create the service user**
   ```bash
   sudo useradd --system --home /var/lib/mlxlink_exporter --shell /usr/sbin/nologin mlxlink_exporter
   ```

3. **Optional: set configuration**
   ```bash
   sudo install -Dm0644 /dev/null /etc/mlxlink_exporter.env
   echo 'MLXLINK_EXPORTER_LISTEN_ADDRESS=:9880' | sudo tee -a /etc/mlxlink_exporter.env
   echo 'MLXLINK_EXPORTER_POLL_INTERVAL=30s'    | sudo tee -a /etc/mlxlink_exporter.env
   echo 'MLXLINK_EXPORTER_LOG_LEVEL=info'       | sudo tee -a /etc/mlxlink_exporter.env
   echo 'MLXLINK_EXPORTER_SHOW_EYE=false'       | sudo tee -a /etc/mlxlink_exporter.env
   echo 'MLXLINK_EXPORTER_SHOW_PCIE_EYE=false'  | sudo tee -a /etc/mlxlink_exporter.env
   ```
   Every flag except `--version` has a `MLXLINK_EXPORTER_*` counterpart and a built-in default, so an empty file is a valid configuration.

   Both Eye collectors are disabled by default because their latency and output have only been verified on MFT 4.34.1 with one ConnectX-7 system. Set either value to `true` only after running the corresponding command as the service user in "Verify the privileges" below.

   All configuration goes in this file rather than in `ExecStart`. systemd does not run `ExecStart` through a shell and expands only `$VAR` and `${VAR}`; shell-style defaults such as `${MLXLINK_EXPORTER_LISTEN_ADDRESS:-:9880}` are not interpreted and would leave the service failing to start. The shipped unit therefore calls the binary with no flags at all.

4. **Install the unit file**
   ```bash
   sudo install -Dm0644 deploy/systemd/mlxlink_exporter.service \
     /etc/systemd/system/mlxlink_exporter.service
   ```

5. **Reload systemd and start the service**
   ```bash
   sudo systemctl daemon-reload
   sudo systemctl enable --now mlxlink_exporter.service
   ```

6. **Verify the service**
   ```bash
   systemctl status mlxlink_exporter.service
   curl -f http://localhost:9880/healthz
   curl -s http://localhost:9880/metrics | grep '^mlxlink_'
   ```

### Verify the privileges

What `mlxlink` needs differs between hosts: MFT packaging, the ownership of the device nodes and the kernel lockdown state all matter. The unit therefore starts unprivileged with an empty capability bounding set, and you grant only what your host turns out to require.

1. **Start unprivileged** with the unit as shipped, then read the error counter:
   ```bash
   curl -s http://localhost:9880/metrics | grep mlxlink_collection_errors_total
   ```

2. **Read the `reason` label**:
   - `reason="permission_denied"` — the process may not execute `/usr/bin/mlxlink` at all. Check the file mode and ownership first.
   - `reason="exit_error"` — `mlxlink` ran but exited non-zero. **A privilege problem usually appears here, not as `permission_denied`**, because `mlxlink` reports insufficient access by failing at run time and explaining itself on stderr. Read that message:
     ```bash
     sudo systemctl stop mlxlink_exporter.service
     sudo -u mlxlink_exporter /usr/bin/mlxlink -d mlx5_0 -m -c --json
     ```
     Running it by hand as the service user is the fastest way to see the exact complaint; the exporter itself logs the truncated stderr only at `--log-level=debug`.
   - `reason="command_not_found"` — the path is wrong; the service would also have refused to start.
   - No error series and `mlxlink_collector_up` at `1` — nothing more is needed. Stop here.

3. **Grant the smallest thing that fixes it**, in this order:
   1. **Device access**: give the service user read/write access to the device nodes `mlxlink` opens, through group ownership or a udev rule, rather than through capabilities.
   2. **Capabilities**: if a capability is genuinely required, add exactly that one to both `CapabilityBoundingSet=` and `AmbientCapabilities=` in the unit. Keep the set explicit; do not clear the bounding set.
   3. **Root override**: if MFT still rejects the service user, install the supplied root-only drop-in. It restores the normal capability bounding set required by the vendor command while preserving the main unit's filesystem hardening:
      ```bash
      sudo install -Dm0644 deploy/systemd/mlxlink_exporter-root.conf \
        /etc/systemd/system/mlxlink_exporter.service.d/root.conf
      sudo systemctl daemon-reload
      sudo systemctl restart mlxlink_exporter.service
      ```
      Use this only after recording why the unprivileged unit cannot collect on the target.

4. **Re-check after every change**: `mlxlink_collection_errors_total` should stop increasing and `mlxlink_collector_up` should report `1` for each device.

5. **Verify optional Eye commands before enabling them**. Run the exact fixed argument vectors as the service user and confirm that each finishes within `MLXLINK_EXPORTER_COMMAND_TIMEOUT` (3 s by default), returns `status.code: 0`, and contains the expected Eye section:
   ```bash
   sudo -u mlxlink_exporter /usr/bin/mlxlink -d mlx5_0 -m -c \
     --rx_fec_histogram --show_histogram --show_serdes_tx --show_eye --json
   sudo -u mlxlink_exporter /usr/bin/mlxlink -d mlx5_0 \
     --port_type PCIE --show_eye --json
   ```
   After setting `MLXLINK_EXPORTER_SHOW_EYE=true` or `MLXLINK_EXPORTER_SHOW_PCIE_EYE=true` and restarting the service, check the network `mlxlink_eye_*` metrics or the PCIe `mlxlink_pcie_eye_*` metrics respectively. PCIe Eye has its own `mlxlink_pcie_eye_collection_errors_total{device,pci_addr,reason}` and `mlxlink_pcie_eye_collector_up{device,pci_addr}`; a PCIe failure does not change `mlxlink_collector_up`.

### Operational notes

- **A host with no RDMA devices never becomes ready.** `/readyz` returns `503` for the lifetime of the process because there is nothing to collect, while `/healthz` stays `200`. Use `/healthz` for liveness and `/readyz` only where "has data" is the question you mean to ask.
- **Scrape interval and poll interval are independent.** A scrape reads a cache, so scraping every 15 s costs nothing extra; `--poll-interval` alone decides how often `mlxlink` runs.
- **Eye collection is explicitly opt-in.** Network Eye adds `--show_eye` to the network combined query. PCIe Eye runs as a separate low-priority query only after every network device has been collected, so enabling it increases the complete sweep time while keeping `mlxlink` concurrency at one.
- **Both exporters can run side by side** on the same host (`:9879` and `:9880`) as separate scrape targets. See the join examples in the README before writing queries that combine them.

## Updating deployment manifests

Whenever new flags or metrics are introduced, update both the systemd unit (if flags are required at start-up) and the Docker instructions accordingly. Tests should continue to pass via `go test ./...` before re-deploying.

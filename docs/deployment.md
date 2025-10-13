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

The repository includes a multi-stage Dockerfile in `deploy/docker/Dockerfile`.

1. **Build the image**
   ```bash
   docker build -t rdma-exporter:latest -f deploy/docker/Dockerfile .
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

## Updating deployment manifests

Whenever new flags or metrics are introduced, update both the systemd unit (if flags are required at start-up) and the Docker instructions accordingly. Tests should continue to pass via `go test ./...` before re-deploying.

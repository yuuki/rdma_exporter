# Prometheus RDMA Exporter

Prometheus exporter for RDMA (Remote Direct Memory Access) NIC statistics on Linux systems.

## Features

- Exports RDMA device and port statistics to Prometheus
- Supports multiple RDMA devices
- Low overhead monitoring using `rdmamap` library
- Graceful shutdown support
- Structured logging with `log/slog`

## Requirements

- Linux operating system
- RDMA-capable network interface cards
- Go 1.21+ (for building from source)

## Installation

### From Source

```bash
git clone https://github.com/yuuki/prometheus-rdma-exporter.git
cd prometheus-rdma-exporter
make build
```

## Usage

### Basic Usage

```bash
./prometheus-rdma-exporter
```

The exporter will start on port 9315 by default and expose metrics at `/metrics`.

### Command-Line Flags

```bash
  -web.listen-address string
        Address on which to expose metrics and web interface. (default ":9315")
  -web.telemetry-path string
        Path under which to expose metrics. (default "/metrics")
  -log.level string
        Log level (debug, info, warn, error). (default "info")
```

### Example

```bash
# Listen on a different port
./prometheus-rdma-exporter -web.listen-address=":8080"

# Enable debug logging
./prometheus-rdma-exporter -log.level=debug
```

## Metrics

The exporter provides the following metrics:

### RDMA Statistics

- `rdma_port_stat_total{device="mlx5_0", port="1", stat="port_rcv_data"}` - RDMA port statistics
  - Labels:
    - `device`: RDMA device name (e.g., mlx5_0)
    - `port`: Port number
    - `stat`: Statistics name (varies by device)

### Exporter Meta Metrics

- `rdma_up` - Whether the last scrape was successful (1 = success, 0 = failure)
- `rdma_scrape_duration_seconds` - Duration of the scrape in seconds

### Standard Go Metrics

The exporter also exposes standard Go runtime and process metrics:
- `go_*` - Go runtime metrics
- `process_*` - Process metrics

## Example Metrics Output

```
# HELP rdma_port_stat_total RDMA port statistics.
# TYPE rdma_port_stat_total counter
rdma_port_stat_total{device="mlx5_0",port="1",stat="port_rcv_data"} 1.234567e+09
rdma_port_stat_total{device="mlx5_0",port="1",stat="port_xmit_data"} 9.876543e+08
rdma_port_stat_total{device="mlx5_0",port="1",stat="port_rcv_packets"} 1.234567e+06
rdma_port_stat_total{device="mlx5_0",port="1",stat="port_xmit_packets"} 9.876543e+05

# HELP rdma_scrape_duration_seconds Duration of the scrape in seconds.
# TYPE rdma_scrape_duration_seconds gauge
rdma_scrape_duration_seconds 0.001234

# HELP rdma_up Was the last scrape of RDMA statistics successful.
# TYPE rdma_up gauge
rdma_up 1
```

## Prometheus Configuration

Add the following to your `prometheus.yml`:

```yaml
scrape_configs:
  - job_name: 'rdma'
    static_configs:
      - targets: ['localhost:9315']
```

## Architecture

The exporter follows Prometheus best practices:

1. **Collector Pattern**: Implements `prometheus.Collector` interface
2. **Dynamic Metrics**: Creates new metrics on each scrape (not updating existing ones)
3. **Standard Naming**: Uses `rdma_` namespace prefix with base units
4. **Minimal Labels**: Uses only essential labels (device, port, stat)
5. **Error Handling**: Continues collecting from other devices even if one fails

## Development

### Building

```bash
make build
```

### Running Tests

```bash
make test
```

### Running Locally

```bash
make run
```

## Design Considerations

### Metric Naming

Following Prometheus naming conventions:
- Namespace prefix: `rdma_`
- Base units: bytes, packets
- Snake case format
- Counters suffixed with `_total`

### Performance

- Lightweight data collection using `rdmamap`
- No background polling - metrics only collected on scrape
- Efficient string to float conversion
- Graceful degradation on errors

### Error Handling

- Device-level error isolation
- Failed devices don't block other devices
- Errors logged with structured logging
- `rdma_up` metric indicates scrape success

## License

[Add your license here]

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

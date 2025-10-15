# RDMA / RoCE Port Telemetry Dashboard

This README describes the bundled Grafana dashboard `rdma_exporter_dashboard.json`, which visualizes telemetry emitted by the `rdma_exporter` Prometheus exporter. The dashboard focuses on InfiniBand and RoCE ports, highlighting availability, throughput, congestion, and error signals that are read from `/sys/class/infiniband` on each host.

## Prerequisites
- Grafana 9.0 or later with a Prometheus data source.
- `rdma_exporter` running on each RDMA-capable node with HTTP access to its `/metrics` endpoint.
- Prometheus scraping the exporter at an interval that matches your operational needs (the dashboard defaults to 1m/5m/15m range selectors).

## Data Pipeline
1. `rdma_exporter` traverses the RDMA sysfs hierarchy and exposes counters such as `rdma_port_rcv_data_total` and `rdma_port_xmit_wait_total`, along with the `rdma_port_info` gauge that carries metadata (device, port, link state, speed, width, etc.).
2. Prometheus scrapes the exporter and stores the metrics with labels `job`, `instance`, `device`, and `port`.
3. Grafana queries Prometheus using the expressions embedded in the dashboard panels and renders time-series, single-stat, and table visualizations for operators.

A minimal Prometheus scrape configuration might look like the following:

```yaml
# prometheus.yml
global:
  scrape_interval: 30s
  scrape_timeout: 5s

scrape_configs:
  - job_name: rdma-exporter
    static_configs:
      - targets:
          - host-a.example.com:9879
          - host-b.example.com:9879
    metrics_path: /metrics
    scheme: http
```

> Note: No CollectD layer is required. If you already use CollectD, you can expose its metrics via the `write_prometheus` plugin on a different port; this dashboard is specifically tuned for the `rdma_exporter` metric names listed below.

## Importing the Dashboard
1. Open Grafana and navigate to **Dashboards → Import**.
2. Click **Upload JSON file** and select `dashboards/rdma_exporter_dashboard.json`, or paste its contents into the JSON textarea.
3. Choose your Prometheus data source when prompted (the default variable is named `Datasource`).
4. Save the dashboard; it will appear under the name **RDMA / RoCE Port Telemetry (rdma_exporter)**.

## Template Variables
The dashboard ships with template variables to scope queries:

| Variable | Label      | Description                                                                                          |
|----------|------------|------------------------------------------------------------------------------------------------------|
| `$ds`    | Datasource | Prometheus data source selector.                                                                     |
| `$job`   | Job        | Populated from `label_values(rdma_port_info, job)` to separate clusters or scrape jobs.             |
| `$instance` | Instance | Filters hosts via `label_values(rdma_port_info{job="$job"}, instance)`.                            |
| `$device`   | Device   | Narrows to specific RNICs using `label_values(..., device)`.                                        |
| `$port`     | Port     | Picks a single port (IB port number) per device.                                                    |
| `$interval` | Interval | Query time range shortcuts (1m, 5m, 15m) used in `rate()` windows.                                  |
| `$topn`     | Top N    | Controls the number of instances shown in top-k throughput leaderboards.                            |

## Panels at a Glance
| Panel Title                                     | Key Metrics                                                                                                                                    | Operational Insight                                                                                           |
|-------------------------------------------------|-------------------------------------------------------------------------------------------------------------------------------------------------|----------------------------------------------------------------------------------------------------------------|
| Overview: Non-ACTIVE Ports [count] / job        | `count(count by (job, instance, device, port) (rdma_port_info{state!="ACTIVE"}))`                                                             | Detects how many ports are down or degraded per job.                                                          |
| Overview: Port State Matrix                     | `rdma_port_info{...}`                                                                                                                           | Cross-filterable heatmap of port state, link layer, and metadata for fleet-wide audits.                       |
| Overview: Link Events [events/s] / job          | `rdma_link_downed_total`, `rdma_link_error_recovery_total` (both via `rate()`)                                                                 | Highlights flapping or unstable links and recovery activity.                                                  |
| Throughput: RX / TX Top-N [B/s] / instance      | `rdma_port_rcv_data_total`, `rdma_port_xmit_data_total`                                                                                        | Identifies busiest hosts or ports by receive/transmit volume.                                                 |
| Packets: Selected Port [pkt/s] / device         | `rdma_port_rcv_packets_total`, `rdma_port_xmit_packets_total`, `rdma_port_multicast_rcv_packets_total`, `rdma_port_unicast_rcv_packets_total` | Breaks down packet rates per direction and traffic class for the focused port.                                |
| Congestion: xmit_wait [ticks/s] / device        | `rdma_port_xmit_wait_total`                                                                                                                    | Surfaces fabric congestion that forces RNICs to delay packet transmission.                                   |
| Congestion: ECN & CNP Signals [events/s] / port | `rdma_np_cnp_sent_total`, `rdma_np_ecn_marked_roce_packets_total`, `rdma_rp_cnp_handled_total`, `rdma_rp_cnp_ignored_total`                     | Correlates ECN marks and congestion-notification packets to measure RoCEv2 congestion control behaviour.      |
| Congestion: Adaptive Retransmission [events/s]  | `rdma_roce_adp_retrans_total`, `rdma_roce_adp_retrans_to_total`                                                                               | Quantifies retransmissions triggered by adaptive timeouts (RoCEv2 congestion recovery).                       |
| Errors: Port Counters [events/s] / instance     | `rdma_port_rcv_errors_total`, `rdma_port_xmit_discards_total`, `rdma_port_rcv_remote_physical_errors_total`, `rdma_symbol_error_total`, etc.   | Aggregates error counters to locate faulty cabling, optics, or firmware anomalies.                           |
| Quality: TX Drop Ratio [%] / port               | `rdma_port_xmit_discards_total` vs. `rdma_port_xmit_packets_total`                                                                             | Visualizes drop ratio to catch oversubscription or buffer exhaustion before it hits SLAs.                    |

## Extending the Dashboard
- Duplicate panels and swap in any other `rdma_*_total` counters exposed by the exporter (e.g., `rdma_duplicate_request_total`).
- Adjust the `$interval` variable defaults if your Prometheus scrape interval is higher than 60 seconds.
- Pair the dashboard with Grafana alerts on critical expressions (e.g. sustained `rdma_link_downed_total` rates).


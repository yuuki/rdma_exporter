# RDMA Exporter Grafana Dashboard Design Specification

## 1. Purpose and Audience
- **Objective**: Provide operations teams with a reusable Grafana dashboard that surfaces RDMA (RoCE/InfiniBand) port health, throughput, congestion indicators, and error conditions, enabling rapid triage and guided drill-down.
- **Scope**: Environments running `rdma_exporter` on Linux hosts with metrics scraped by a Prometheus-compatible datasource (Prometheus, Mimir, VictoriaMetrics, etc.).
- **Metric Source of Truth**: `rdma_exporter` collector implementation (`internal/collector/collector.go`) defines label schema and unit semantics; note that `*_xmit/rcv_data_total` counters are reported in doublewords and require multiplication by four to express bytes per second.

## 2. Key References
1. Grafana Labs, “JSON model” – dashboard persistence, UID management, versioning, and provisioning guidance. <https://grafana.com/docs/grafana/latest/dashboards/build-dashboards/view-dashboard-json-model/>
2. Grafana Labs, “Create dashboard URL variables” – recommendations for variable definitions, default values, and URL interoperability. <https://grafana.com/docs/grafana-cloud/visualizations/dashboards/build-dashboards/create-dashboard-url-variables/>
3. Internal discussion memo “RDMA Exporter ダッシュボード設計書（公開版）” (2025-??-??) – captures prior design deliberations, PromQL conventions, and publication requirements.

## 3. Success Metrics and Non-Goals
- **Success metrics**
  - Single dashboard covers heterogeneous clusters; users swap targets via variables without editing JSON.
  - Time-to-triage for port-down or congestion events is under five minutes with prescribed drill-down panels.
  - Dashboard JSON stays stable under Git control with pinned `uid` and incremented `version`.
- **Non-goals**
  - Alert rule authoring beyond recommended starter policies.
  - Vendor-specific InfiniBand fabric telemetry (e.g., hardware-specific counters outside rdma_exporter scope).

## 4. Functional Requirements
- Visualize RDMA port state, link events, throughput, congestion, and error-rate metrics collected by `rdma_exporter`.
- Support multi-cluster deployments by exposing datasource and topology variables (`datasource` → `job` → `instance` → `device` → `port`).
- Surface PromQL expressions and interpretation guidance directly in panel descriptions for transparency and onboarding.
- Provide drill-down pathways (panel links, dashboard links) to deeper diagnostics such as logs or per-device dashboards.
- Maintain accessibility: clearly labeled units, avoid color-only differentiation, keep stat cards legible on light/dark themes.

## 5. Datasource and Variable Strategy
- **Datasource variable (`$ds`)**: `type: datasource`, `query: prometheus`, default to primary scraping endpoint.
- **Topology variables** (URL-compatible per Grafana guidance):
  - `$job`: `label_values(rdma_port_info, job)`
  - `$instance`: `label_values(rdma_port_info{job="$job"}, instance)`
  - `$device`: `label_values(rdma_port_info{job="$job", instance="$instance"}, device)`
  - `$port`: `label_values(rdma_port_info{job="$job", instance="$instance", device="$device"}, port)`
- **Performance alternative**: when `label_values()` is slow on large cardinalities, replace it with `query_result(...)` patterns, for example `query_result(count by (job) (rdma_port_info))` or `query_result(count by (device) (rdma_port_info{job="$job", instance="$instance"}))` to leverage server-side aggregation while respecting available labels.
- **Temporal control**: `$interval` (type `interval`, options `1m`,`5m`,`15m`, default `5m`) to align `rate()` windows with operator expectations.
- **Top-N granularity**: `$topn` (type `custom`, values `5,10,20`, default `10`) controls ranking panels.
- Ensure every variable sets `includeAll=false` to avoid overwhelming queries; expose `refresh:on dashboard load` for top-level variables to guarantee freshness.

## 6. Information Architecture and Layout
Organize panels into three horizontal rows across a 24-column grid, following “overview → compare → investigate” flow.

### Row 1 – Overview & SLO Guardrails
- **Stat: Non-ACTIVE Ports** – count of ports where `state != "ACTIVE"` within the selected topology.
- **Table: Port State Matrix** – columns `device`, `port`, `state`, `phys_state`, `link_width`, `link_speed`, `link_layer`.
- **Stat: Link Events per Second** – aggregate `link_downed` and `link_error_recovery` rates for anomaly spikes.

### Row 2 – Throughput & Packet Dynamics
- **Time series: RX Bytes/s Top-N** – `topk($topn, 4 * rate(rdma_port_rcv_data_total{...}[$interval]))` grouped by `port`.
- **Time series: TX Bytes/s Top-N** – symmetrical transmit perspective.
- **Time series: Selected Port Packet Rates** – juxtapose RX vs. TX packet rate and unicast/multicast split for the currently chosen `device/port`.
- **Time series: Transmit Waits** – `rate(rdma_port_xmit_wait_total{...}[$interval])` as congestion proxy.

### Row 3 – Congestion & Error Deep Dive
- **Time series: ECN / CNP Activity** – visualize `rdma_np_cnp_sent_total`, `rdma_np_ecn_marked_roce_packets_total`, `rdma_rp_cnp_handled_total`, `rdma_rp_cnp_ignored_total`.
- **Time series: Adaptive Retransmission / Timeout** – highlight reliability and congestion control behavior.
- **Stacked bars: Error Family Breakdown** – `rdma_port_rcv_errors_total`, `rdma_port_xmit_discards_total`, `rdma_port_rcv_remote_physical_errors_total`, `rdma_symbol_error_total`.
  Extend the stack with auxiliary counters exposed by the collector such as `rdma_port_rcv_switch_relay_errors_total`, `rdma_port_rcv_constraint_errors_total`, and `rdma_port_xmit_constraint_errors_total` to highlight switch-forwarding and congestion drops.
- **Stat / Time series: TX Drop Ratio (%)** – ratio of discards to transmitted packets with `clamp_min` to prevent division by zero.

## 7. Panel Naming, Tooltips, and Units
- Panels follow `Category: Metric [Unit] / Scope` convention, e.g., `Throughput: RX Top-$topn [B/s] / job` for consistent scanning.
- Populate panel descriptions with the PromQL query, a plain-language interpretation, and operational hints (e.g., thresholds, next steps).
- Enable legend placement at bottom with value/percent toggles for clarity; prefer shared crosshair tooltip for comparative reading.

## 8. PromQL Catalogue
| Panel | Expression (template variables inlined) | Notes |
|-------|-----------------------------------------|-------|
| Non-ACTIVE ports | `count(rdma_port_info{job="$job", instance="$instance", state!="ACTIVE"})` | Use stat panel with red threshold at `>0` |
| Link events/s | `sum(rate(rdma_link_downed_total{job="$job", instance="$instance"}[$interval])) + sum(rate(rdma_link_error_recovery_total{job="$job", instance="$instance"}[$interval]))` | Display as single stat with sparkline |
| RX Bytes/s Top-N | `topk($topn, 4 * rate(rdma_port_rcv_data_total{job="$job", instance="$instance"}[$interval]))` | Multiply by 4 to convert doublewords to bytes |
| TX Bytes/s Top-N | `topk($topn, 4 * rate(rdma_port_xmit_data_total{job="$job", instance="$instance"}[$interval]))` | Mirror of RX |
| Selected port RX pkts/s | `rate(rdma_port_rcv_packets_total{job="$job", instance="$instance", device="$device", port="$port"}[$interval])` | Pair with TX |
| Selected port TX pkts/s | `rate(rdma_port_xmit_packets_total{job="$job", instance="$instance", device="$device", port="$port"}[$interval])` | Pair with RX |
| Multicast vs. Unicast mix | `rate(rdma_port_multicast_rcv_packets_total{...}[$interval])` vs. `rate(rdma_port_unicast_rcv_packets_total{...}[$interval])` | Use dual-axis or field overrides |
| Transmit wait | `rate(rdma_port_xmit_wait_total{job="$job", instance="$instance", device="$device", port="$port"}[$interval])` | Highlight sustained non-zero periods |
| ECN / CNP signals | `rate(rdma_np_cnp_sent_total{...}[$interval])`, `rate(rdma_np_ecn_marked_roce_packets_total{...}[$interval])`, etc. | Group into repeating panel if needed |
| Adaptive retransmission | `rate(rdma_roce_adp_retrans_total{...}[$interval])`, `rate(rdma_roce_adp_retrans_to_total{...}[$interval])` | Watch for spikes |
| Error breakdown | `rate(rdma_port_rcv_errors_total{...}[$interval])`, `rate(rdma_port_xmit_discards_total{...}[$interval])`, `rate(rdma_port_rcv_remote_physical_errors_total{...}[$interval])`, `rate(rdma_symbol_error_total{...}[$interval])`, `rate(rdma_port_rcv_switch_relay_errors_total{...}[$interval])`, `rate(rdma_port_rcv_constraint_errors_total{...}[$interval])`, `rate(rdma_port_xmit_constraint_errors_total{...}[$interval])` | Stack by metric; toggle visibility for high-cardinality fabrics |
| TX drop ratio % | `100 * rate(rdma_port_xmit_discards_total{...}[$interval]) / clamp_min(rate(rdma_port_xmit_packets_total{...}[$interval]), 1e-6)` | Apply percent unit |

## 9. Alerting Recommendations (Grafana Alerting)
- `RDMA_Port_Down`: `count(rdma_port_info{state!="ACTIVE"}) > 0` sustained for 10m.
- `RDMA_Link_Downed_Spike`: `rate(rdma_link_downed_total[5m]) > 0`.
- `RDMA_TX_Drop_Ratio_High`: `rdma:tx_drop_ratio > 0.1` for 5m.
- `RDMA_RoCE_CNP_Ignored`: `rate(rdma_rp_cnp_ignored_total[5m]) > 0` indicates ECN misconfiguration.
- `RDMA_Scrape_Errors`: `increase(rdma_scrape_errors_total[15m]) > 0` to detect exporter failures.
Document alert rules alongside dashboard JSON under Git control; leverage provisioned alerting for consistent deployment.

## 10. Recording Rules (Prometheus)
```yaml
groups:
- name: rdma.rules
  interval: 30s
  rules:
  - record: rdma:bytes_rx_per_s
    expr: 4 * rate(rdma_port_rcv_data_total[1m])
  - record: rdma:bytes_tx_per_s
    expr: 4 * rate(rdma_port_xmit_data_total[1m])
  - record: rdma:pkts_rx_per_s
    expr: rate(rdma_port_rcv_packets_total[1m])
  - record: rdma:pkts_tx_per_s
    expr: rate(rdma_port_xmit_packets_total[1m])
  - record: rdma:tx_drop_ratio
    expr: 100 * rate(rdma_port_xmit_discards_total[5m]) / clamp_min(rate(rdma_port_xmit_packets_total[5m]), 1e-6)
```
Precompute heavy expressions to accelerate dashboards and keep query inspector responses under Grafana’s default timeout.

## 11. Implementation Guidance
- **JSON Model & Versioning**: Pin `uid`, increment `version` on every change, and store JSON alongside this design doc. Use `folderUid` for namespace consistency when provisioning. (See Grafana JSON model documentation.)
- **Provisioning**: Adopt YAML-based provisioning or Git Sync workflows so dashboards flow through pull requests before production rollout.
- **Library Panels**: Extract reusable tables or stat cards into Grafana library panels for other RDMA dashboards.
- **Annotations & Links**: Add dashboard links to driver log panels (e.g., Loki queries) and create panel links for per-device drill-down pages.
- **Import Experience**: Ensure dashboard imports cleanly via JSON/ID upload; prompt users only for the Prometheus datasource.

## 12. Publication Checklist
1. Validate dashboard locally (Grafana ≥ 9.5) with representative datasets and capture three screenshots (overview, throughput, error deep dive) following Grafana image guidelines.
2. Verify descriptions, tags (`rdma`, `roce`, `infiniband`, `networking`, `prometheus`), and minimum Grafana version metadata.
3. Upload to Grafana.com community gallery; retain dashboard ID for repository README updates.
4. Document import instructions (ID/URL/JSON) in project README.

## 13. Known Pitfalls and Mitigations
- **Per-node dashboard sprawl**: Rely on variables and drill-down links instead of duplicating dashboards.
- **Expensive variable queries**: Swap `label_values()` for `query_result(count by (...))` when scrape performance degrades.
- **Unit mismatches**: Enforce `×4` conversion for `_data_total` counters; highlight this in panel descriptions and recording rules.
- **Regenerating UID**: Prior to publishing, ensure JSON exports retain a fixed `uid` to avoid breaking embedded links or provisioning setups.
- **Grafana.com external sharing**: Grafana Labs’ community gallery validates dashboards against the “Export for sharing externally” format. The repository JSON is the provisioning format (no `__inputs` / `__requires` blocks), so re-export from Grafana UI with that option—or manually add the required metadata—before uploading to avoid the “Old dashboard JSON format” error.

## 14. Appendix – Future Enhancements
- Automate JSON generation via Grafana provisioning pipelines and include schema validation in CI.
- Extend dashboard links to per-switch telemetry dashboards once available.
- Evaluate Grafana 12+ Git Sync for two-way synchronization between hosted Grafana Cloud and repository-managed JSON.
- Prototype optional deep-dive panels that visualize additional collector counters (`port_rcv_switch_relay_errors`, `port_rcv_constraint_errors`, `port_xmit_constraint_errors`, etc.) when operators need finer-grained failure attribution.

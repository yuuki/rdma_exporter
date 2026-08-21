#!/bin/sh
set -eu

# Optional counter names. Override to own the full enabled set (set replaces the list).
: "${RDMA_OPTIONAL_COUNTERS:=cc_rx_ce_pkts,cc_rx_cnp_pkts,cc_tx_cnp_pkts}"

# Set to 1 to enable QP auto type and optional-counters on (mlx5, Linux 6.15+).
: "${RDMA_ENABLE_QP_COUNTERS:=0}"

if [ "$RDMA_ENABLE_QP_COUNTERS" = 1 ] && [ "$RDMA_OPTIONAL_COUNTERS" = "cc_rx_ce_pkts,cc_rx_cnp_pkts,cc_tx_cnp_pkts" ]; then
  RDMA_OPTIONAL_COUNTERS=cc_rx_ce_pkts,cc_rx_cnp_pkts,cc_tx_cnp_pkts,rdma_rx_bytes,rdma_tx_bytes,rdma_rx_packets,rdma_tx_packets
fi

status=0
for port_dir in /sys/class/infiniband/*/ports/*; do
  [ -d "$port_dir" ] || continue
  dev=$(basename "$(dirname "$(dirname "$port_dir")")")
  port=$(basename "$port_dir")
  link="$dev/$port"
  if ! rdma statistic mode supported link "$link" | grep -q cc_rx_ce_pkts; then
    continue
  fi
  rdma statistic set link "$link" optional-counters "$RDMA_OPTIONAL_COUNTERS" || status=1
  if [ "$RDMA_ENABLE_QP_COUNTERS" = 1 ]; then
    rdma statistic qp set link "$link" auto type on optional-counters on || status=1
  fi
done
exit "$status"

package collector

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestIsRepresentorPhysPortName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		want bool
	}{
		{name: "p0", want: false},
		{name: "", want: false},
		{name: "1", want: false},
		{name: "pf0vf1", want: true},
		{name: "pf0sf1", want: true},
		{name: "c1pf0vf0", want: true},
		{name: "c1pf0sf0", want: true},
		{name: "pf0hpf", want: true},
		{name: "c1pf0hpf", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isRepresentorPhysPortName(tc.name); got != tc.want {
				t.Fatalf("isRepresentorPhysPortName(%q) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestReadNetDevPhysPortNameFromTrimsValue(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	netDir := filepath.Join(root, "class", "net", "ens1f0np0")
	if err := os.MkdirAll(netDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(netDir, "phys_port_name"), []byte("pf0vf1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := readNetDevPhysPortNameFrom(root, "ens1f0np0")
	if got != "pf0vf1" {
		t.Fatalf("got %q, want pf0vf1", got)
	}
}

func TestEmitNetDevHWMetricsOrder(t *testing.T) {
	t.Parallel()

	stats := map[string]uint64{
		"rx_prio0_pause":                  1,
		"rx_prio9_discards":               2,
		"rx_prio2_packets":                3,
		"rx_prio7_marked":                 4,
		"rx_prio1_cong_discard":           5,
		"outbound_pci_stalled_wr":         6,
		"outbound_pci_stalled_rd_events":  7,
		"outbound_pci_buffer_overflow":    8,
		"tx_pci_signal_integrity":         9,
		"rx_err_lane_10_phy":              10,
		"rx_global_pause":                 11,
		"tx_global_pause_duration":        12,
		"rx_global_pause_transition":      13,
		"tx_global_pause_transition":      14,
		"tx_pause_storm_warning_events":   15,
		"tx_vport_rdma_multicast_packets": 16,
		"rx_vport_rdma_unicast_bytes":     17,
		"rx_vport_unicast_bytes":          18,
		"link_down_events_phy":            19,
		"dev_out_of_buffer":               20,
		"rx_packets":                      21,
	}

	cases := []struct {
		name      string
		skipVPort bool
		want      []string
	}{
		{
			name:      "with vport",
			skipVPort: false,
			want: []string{
				`rdma_netdev_dev_out_of_buffer_total{device="mlx5_0",netdev="ens1f0np0",port="1"} counter 20`,
				`rdma_phy_link_down_events_total{device="mlx5_0",netdev="ens1f0np0",port="1"} counter 19`,
				`rdma_pcie_outbound_buffer_overflow_total{device="mlx5_0",netdev="ens1f0np0",port="1"} counter 8`,
				`rdma_pcie_outbound_stalled_seconds_total{device="mlx5_0",netdev="ens1f0np0",op="rd",port="1"} counter 7`,
				`rdma_pcie_outbound_stalled_percent{device="mlx5_0",netdev="ens1f0np0",op="wr",port="1"} gauge 6`,
				`rdma_phy_rx_err_lane_total{device="mlx5_0",lane="10",netdev="ens1f0np0",port="1"} counter 10`,
				`rdma_netdev_global_pause_frames_total{device="mlx5_0",direction="rx",netdev="ens1f0np0",port="1"} counter 11`,
				`rdma_netdev_global_pause_transitions_total{device="mlx5_0",netdev="ens1f0np0",port="1"} counter 13`,
				`rdma_netdev_prio_cong_discard_total{device="mlx5_0",netdev="ens1f0np0",port="1",priority="1"} counter 5`,
				`rdma_netdev_prio_ecn_marked_total{device="mlx5_0",netdev="ens1f0np0",port="1",priority="7"} counter 4`,
				`rdma_netdev_vport_rdma_bytes_total{device="mlx5_0",direction="rx",netdev="ens1f0np0",port="1",traffic="unicast"} counter 17`,
				`rdma_netdev_global_pause_duration_total{device="mlx5_0",direction="tx",netdev="ens1f0np0",port="1"} counter 12`,
				`rdma_netdev_pause_storm_events_total{device="mlx5_0",netdev="ens1f0np0",port="1",severity="warning"} counter 15`,
				`rdma_pcie_signal_integrity_total{device="mlx5_0",direction="tx",netdev="ens1f0np0",port="1"} counter 9`,
				`rdma_netdev_vport_rdma_packets_total{device="mlx5_0",direction="tx",netdev="ens1f0np0",port="1",traffic="multicast"} counter 16`,
			},
		},
		{
			name:      "without vport",
			skipVPort: true,
			want: []string{
				`rdma_netdev_dev_out_of_buffer_total{device="mlx5_0",netdev="ens1f0np0",port="1"} counter 20`,
				`rdma_phy_link_down_events_total{device="mlx5_0",netdev="ens1f0np0",port="1"} counter 19`,
				`rdma_pcie_outbound_buffer_overflow_total{device="mlx5_0",netdev="ens1f0np0",port="1"} counter 8`,
				`rdma_pcie_outbound_stalled_seconds_total{device="mlx5_0",netdev="ens1f0np0",op="rd",port="1"} counter 7`,
				`rdma_pcie_outbound_stalled_percent{device="mlx5_0",netdev="ens1f0np0",op="wr",port="1"} gauge 6`,
				`rdma_phy_rx_err_lane_total{device="mlx5_0",lane="10",netdev="ens1f0np0",port="1"} counter 10`,
				`rdma_netdev_global_pause_frames_total{device="mlx5_0",direction="rx",netdev="ens1f0np0",port="1"} counter 11`,
				`rdma_netdev_global_pause_transitions_total{device="mlx5_0",netdev="ens1f0np0",port="1"} counter 13`,
				`rdma_netdev_prio_cong_discard_total{device="mlx5_0",netdev="ens1f0np0",port="1",priority="1"} counter 5`,
				`rdma_netdev_prio_ecn_marked_total{device="mlx5_0",netdev="ens1f0np0",port="1",priority="7"} counter 4`,
				`rdma_netdev_global_pause_duration_total{device="mlx5_0",direction="tx",netdev="ens1f0np0",port="1"} counter 12`,
				`rdma_netdev_pause_storm_events_total{device="mlx5_0",netdev="ens1f0np0",port="1",severity="warning"} counter 15`,
				`rdma_pcie_signal_integrity_total{device="mlx5_0",direction="tx",netdev="ens1f0np0",port="1"} counter 9`,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			c := New(&stubProvider{}, newDiscardLogger())
			got := emitted(t, func(ch chan<- prometheus.Metric) {
				c.emitNetDevHWMetrics(ch, "mlx5_0", "1", "ens1f0np0", stats, tc.skipVPort)
			})
			assertEmitted(t, got, tc.want)
		})
	}
}

// netDevHWSeriesFor turns every submatch of a family's pattern into a label,
// so a pattern with a capture group too many or too few would make
// MustNewConstMetric panic at scrape time.
func TestNetDevHWFamiliesLabelEverySubmatch(t *testing.T) {
	t.Parallel()

	c := New(&stubProvider{}, newDiscardLogger())
	for _, family := range c.netDevHWFamilies() {
		labels := make([]string, 3+family.pattern.NumSubexp()) // device, port, netdev, submatches
		if _, err := prometheus.NewConstMetric(family.desc, family.valueType, 0, labels...); err != nil {
			t.Errorf("pattern %s: submatches do not fit the descriptor labels: %v", family.pattern, err)
		}
	}
}

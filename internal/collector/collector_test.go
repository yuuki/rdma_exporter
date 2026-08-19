package collector

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"

	"github.com/yuuki/rdma_exporter/internal/rdma"
	"github.com/yuuki/rdma_exporter/internal/rdmanl"
)

type stubProvider struct {
	devices []rdma.Device
	err     error
}

func (s *stubProvider) Devices(context.Context) ([]rdma.Device, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.devices, nil
}

type stubNetDevStatsProvider struct {
	mu    sync.Mutex
	stats map[string]map[string]uint64
	errs  map[string]error
	calls map[string]int
}

func newStubNetDevStatsProvider() *stubNetDevStatsProvider {
	return &stubNetDevStatsProvider{
		stats: make(map[string]map[string]uint64),
		errs:  make(map[string]error),
		calls: make(map[string]int),
	}
}

func (s *stubNetDevStatsProvider) Stats(_ context.Context, netDev string) (map[string]uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.calls[netDev]++
	if err, ok := s.errs[netDev]; ok {
		return nil, err
	}

	src, ok := s.stats[netDev]
	if !ok {
		return map[string]uint64{}, nil
	}
	out := make(map[string]uint64, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out, nil
}

func (s *stubNetDevStatsProvider) CallCount(netDev string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[netDev]
}

type stubOptionalCounterProvider struct {
	mu         sync.Mutex
	prepareN   int
	counters   map[string][]rdmanl.OptionalCounter
	errs       map[string]error
	calls      map[string]int
	prepareErr error
}

func newStubOptionalCounterProvider() *stubOptionalCounterProvider {
	return &stubOptionalCounterProvider{
		counters: make(map[string][]rdmanl.OptionalCounter),
		errs:     make(map[string]error),
		calls:    make(map[string]int),
	}
}

func optionalKey(device string, port int) string {
	return device + "/" + strconv.Itoa(port)
}

func (s *stubOptionalCounterProvider) Prepare(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prepareN++
	return s.prepareErr
}

func (s *stubOptionalCounterProvider) Counters(_ context.Context, device string, port int) ([]rdmanl.OptionalCounter, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := optionalKey(device, port)
	s.calls[key]++
	if err, ok := s.errs[key]; ok {
		return nil, err
	}
	src := s.counters[key]
	out := make([]rdmanl.OptionalCounter, len(src))
	copy(out, src)
	return out, nil
}

func (s *stubOptionalCounterProvider) PrepareCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.prepareN
}

func (s *stubOptionalCounterProvider) CallCount(device string, port int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[optionalKey(device, port)]
}

func newDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestCollectorExportsMetrics(t *testing.T) {
	t.Parallel()

	provider := &stubProvider{
		devices: []rdma.Device{
			{
				Name:     "mlx5_0",
				PCIAddr:  "0000:1a:00.0",
				IsVF:     false,
				PFDevice: "",
				Ports: []rdma.Port{
					{
						ID: 1,
						Stats: map[string]uint64{
							"port_xmit_data": 10,
							"port_rcv_data":  5,
						},
						HwStats: map[string]uint64{
							"symbol_error": 1,
						},
						Attributes: rdma.PortAttributes{
							LinkLayer: "InfiniBand",
							State:     "ACTIVE",
							PhysState: "LinkUp",
							LinkWidth: "4X",
							LinkSpeed: "100 Gb/sec",
						},
					},
				},
			},
		},
	}

	c := New(provider, newDiscardLogger())
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	c.SetContext(context.Background())
	defer c.ResetContext()

	expected := `
# HELP rdma_port_info RDMA port metadata exported as labels.
# TYPE rdma_port_info gauge
rdma_port_info{device="mlx5_0",is_vf="false",link_layer="InfiniBand",link_speed="100 Gb/sec",link_width="4X",pci_addr="0000:1a:00.0",pf_device="",phys_state="LinkUp",port="1",state="ACTIVE"} 1
# HELP rdma_port_rcv_data_total The total number of data octets, divided by 4 (counting in double words, 32 bits), received on all VLs from the port.
# TYPE rdma_port_rcv_data_total counter
rdma_port_rcv_data_total{device="mlx5_0",port="1"} 5
# HELP rdma_port_xmit_data_total The total number of data octets, divided by 4, transmitted on all VLs from the port.
# TYPE rdma_port_xmit_data_total counter
rdma_port_xmit_data_total{device="mlx5_0",port="1"} 10
# HELP rdma_symbol_error_total Total number of minor link errors detected on one or more physical lanes.
# TYPE rdma_symbol_error_total counter
rdma_symbol_error_total{device="mlx5_0",port="1"} 1
`

	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected),
		"rdma_port_rcv_data_total", "rdma_port_xmit_data_total", "rdma_symbol_error_total", "rdma_port_info"); err != nil {
		t.Fatalf("unexpected metrics output: %v", err)
	}
}

func TestCollectorIncrementsErrorCounter(t *testing.T) {
	t.Parallel()

	provider := &stubProvider{err: errors.New("boom")}
	c := New(provider, newDiscardLogger())

	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	c.SetContext(context.Background())
	defer c.ResetContext()

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("unexpected gather error: %v", err)
	}

	value := findMetricValue(t, mfs, "rdma_scrape_errors_total")
	if value != 1 {
		t.Fatalf("expected scrape error counter to be 1, got %v", value)
	}
}

func TestCollectorExportsRoCEPFCMetrics(t *testing.T) {
	t.Parallel()

	provider := &stubProvider{
		devices: []rdma.Device{
			{
				Name: "mlx5_0",
				Ports: []rdma.Port{
					{
						ID: 1,
						Attributes: rdma.PortAttributes{
							LinkLayer: "Ethernet",
							NetDev:    "ens1f0np0",
						},
					},
				},
			},
		},
	}

	netDevProvider := newStubNetDevStatsProvider()
	netDevProvider.stats["ens1f0np0"] = map[string]uint64{
		"rx_prio0_pause":            10,
		"tx_prio3_pause":            20,
		"rx_prio4_pause_duration":   30,
		"tx_prio7_pause_transition": 40,
		"rx_prio2_packets":          50,
	}

	c := New(provider, newDiscardLogger(), WithNetDevStatsProvider(netDevProvider))
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	expected := `
# HELP rdma_roce_pfc_pause_duration_total Cumulative RoCEv2 PFC pause duration in microseconds from ethtool stats. Pause occupancy is rate()/1e6. Direction has the same meaning as rdma_roce_pfc_pause_frames_total.
# TYPE rdma_roce_pfc_pause_duration_total counter
rdma_roce_pfc_pause_duration_total{device="mlx5_0",direction="rx",netdev="ens1f0np0",port="1",priority="4"} 30
# HELP rdma_roce_pfc_pause_frames_total RoCEv2 PFC pause frames from ethtool stats. direction=rx: the peer XOFFed this NIC so this NIC cannot transmit on that priority. direction=tx: this NIC XOFFed the peer because it is not absorbing that priority.
# TYPE rdma_roce_pfc_pause_frames_total counter
rdma_roce_pfc_pause_frames_total{device="mlx5_0",direction="rx",netdev="ens1f0np0",port="1",priority="0"} 10
rdma_roce_pfc_pause_frames_total{device="mlx5_0",direction="tx",netdev="ens1f0np0",port="1",priority="3"} 20
# HELP rdma_roce_pfc_pause_transitions_total RoCEv2 PFC XOFF-to-XON transitions from ethtool stats. mlx5 exposes this for receive (direction=rx) only. Direction has the same meaning as rdma_roce_pfc_pause_frames_total.
# TYPE rdma_roce_pfc_pause_transitions_total counter
rdma_roce_pfc_pause_transitions_total{device="mlx5_0",direction="tx",netdev="ens1f0np0",port="1",priority="7"} 40
`

	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected),
		"rdma_roce_pfc_pause_frames_total",
		"rdma_roce_pfc_pause_duration_total",
		"rdma_roce_pfc_pause_transitions_total"); err != nil {
		t.Fatalf("unexpected pfc metrics output: %v", err)
	}
}

func TestCollectorSkipsRoCEPFCForInfiniBandPort(t *testing.T) {
	t.Parallel()

	provider := &stubProvider{
		devices: []rdma.Device{
			{
				Name: "mlx5_0",
				Ports: []rdma.Port{
					{
						ID: 1,
						Attributes: rdma.PortAttributes{
							LinkLayer: "InfiniBand",
							NetDev:    "ens1f0np0",
						},
					},
				},
			},
		},
	}
	netDevProvider := newStubNetDevStatsProvider()

	c := New(provider, newDiscardLogger(), WithNetDevStatsProvider(netDevProvider))
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	if _, err := reg.Gather(); err != nil {
		t.Fatalf("unexpected gather error: %v", err)
	}

	if got := netDevProvider.CallCount("ens1f0np0"); got != 0 {
		t.Fatalf("expected netdev provider not to be called, got %d", got)
	}
}

func TestCollectorSkipsRoCEPFCWhenNetDevMissing(t *testing.T) {
	t.Parallel()

	provider := &stubProvider{
		devices: []rdma.Device{
			{
				Name: "mlx5_0",
				Ports: []rdma.Port{
					{
						ID: 1,
						Attributes: rdma.PortAttributes{
							LinkLayer: "Ethernet",
						},
					},
				},
			},
		},
	}
	netDevProvider := newStubNetDevStatsProvider()

	c := New(provider, newDiscardLogger(), WithNetDevStatsProvider(netDevProvider))
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	if _, err := reg.Gather(); err != nil {
		t.Fatalf("unexpected gather error: %v", err)
	}

	if got := netDevProvider.CallCount(""); got != 0 {
		t.Fatalf("expected netdev provider not to be called, got %d", got)
	}
}

func TestCollectorIncrementsRoCEPFCErrorCounter(t *testing.T) {
	t.Parallel()

	provider := &stubProvider{
		devices: []rdma.Device{
			{
				Name: "mlx5_0",
				Ports: []rdma.Port{
					{
						ID: 1,
						Attributes: rdma.PortAttributes{
							LinkLayer: "Ethernet",
							NetDev:    "ens1f0np0",
						},
					},
				},
			},
		},
	}
	netDevProvider := newStubNetDevStatsProvider()
	netDevProvider.errs["ens1f0np0"] = errors.New("boom")

	c := New(provider, newDiscardLogger(), WithNetDevStatsProvider(netDevProvider))
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("unexpected gather error: %v", err)
	}

	value := findMetricValue(t, mfs, "rdma_roce_pfc_scrape_errors_total")
	if value != 1 {
		t.Fatalf("expected roce pfc scrape error counter to be 1, got %v", value)
	}
}

func TestCollectorSkipsRoCEPFCForVirtualFunction(t *testing.T) {
	t.Parallel()

	provider := &stubProvider{
		devices: []rdma.Device{
			{
				Name: "mlx5_12",
				IsVF: true,
				Ports: []rdma.Port{
					{
						ID: 1,
						Attributes: rdma.PortAttributes{
							LinkLayer: "Ethernet",
							NetDev:    "enp26s0v0",
						},
					},
				},
			},
		},
	}
	netDevProvider := newStubNetDevStatsProvider()
	netDevProvider.stats["enp26s0v0"] = map[string]uint64{
		"rx_prio3_pause": 99,
	}

	c := New(provider, newDiscardLogger(), WithNetDevStatsProvider(netDevProvider))
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	if _, err := reg.Gather(); err != nil {
		t.Fatalf("unexpected gather error: %v", err)
	}

	if got := netDevProvider.CallCount("enp26s0v0"); got != 0 {
		t.Fatalf("expected netdev provider not to be called for VF, got %d calls", got)
	}
}

func TestCollectorPFCMixedPFAndVF(t *testing.T) {
	t.Parallel()

	provider := &stubProvider{
		devices: []rdma.Device{
			{
				Name: "mlx5_0",
				IsVF: false,
				Ports: []rdma.Port{
					{
						ID: 1,
						Attributes: rdma.PortAttributes{
							LinkLayer: "Ethernet",
							NetDev:    "enp26s0np0",
						},
					},
				},
			},
			{
				Name: "mlx5_12",
				IsVF: true,
				Ports: []rdma.Port{
					{
						ID: 1,
						Attributes: rdma.PortAttributes{
							LinkLayer: "Ethernet",
							NetDev:    "enp26s0v0",
						},
					},
				},
			},
		},
	}

	netDevProvider := newStubNetDevStatsProvider()
	netDevProvider.stats["enp26s0np0"] = map[string]uint64{
		"tx_prio3_pause": 7,
	}
	netDevProvider.stats["enp26s0v0"] = map[string]uint64{
		"tx_prio3_pause": 99,
	}

	c := New(provider, newDiscardLogger(), WithNetDevStatsProvider(netDevProvider))
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	expected := `
# HELP rdma_roce_pfc_pause_frames_total RoCEv2 PFC pause frames from ethtool stats. direction=rx: the peer XOFFed this NIC so this NIC cannot transmit on that priority. direction=tx: this NIC XOFFed the peer because it is not absorbing that priority.
# TYPE rdma_roce_pfc_pause_frames_total counter
rdma_roce_pfc_pause_frames_total{device="mlx5_0",direction="tx",netdev="enp26s0np0",port="1",priority="3"} 7
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected),
		"rdma_roce_pfc_pause_frames_total"); err != nil {
		t.Fatalf("unexpected pfc metrics output: %v", err)
	}

	if got := netDevProvider.CallCount("enp26s0v0"); got != 0 {
		t.Fatalf("expected VF netdev provider not to be called, got %d calls", got)
	}
}

func TestCollectorFetchesNetDevStatsOncePerScrape(t *testing.T) {
	t.Parallel()

	provider := &stubProvider{
		devices: []rdma.Device{
			{
				Name: "mlx5_0",
				Ports: []rdma.Port{
					{
						ID: 1,
						Attributes: rdma.PortAttributes{
							LinkLayer: "Ethernet",
							NetDev:    "ens1f0np0",
						},
					},
					{
						ID: 2,
						Attributes: rdma.PortAttributes{
							LinkLayer: "Ethernet",
							NetDev:    "ens1f0np0",
						},
					},
				},
			},
		},
	}
	netDevProvider := newStubNetDevStatsProvider()
	netDevProvider.stats["ens1f0np0"] = map[string]uint64{
		"rx_prio0_pause": 1,
	}

	c := New(provider, newDiscardLogger(), WithNetDevStatsProvider(netDevProvider))
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	if _, err := reg.Gather(); err != nil {
		t.Fatalf("unexpected gather error: %v", err)
	}

	if got := netDevProvider.CallCount("ens1f0np0"); got != 1 {
		t.Fatalf("expected netdev provider to be called once, got %d", got)
	}
}

func TestCollectorOmitsNetDevHWMetricsByDefault(t *testing.T) {
	t.Parallel()

	provider, netDevProvider := ethernetPFWithStats(map[string]uint64{
		"rx_prio3_buf_discard":    4,
		"outbound_pci_stalled_rd": 12,
		"rx_corrected_bits_phy":   8,
		"rx_prio0_pause":          1,
	})

	c := New(provider, newDiscardLogger(), WithNetDevStatsProvider(netDevProvider))
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	if err := testutil.GatherAndCompare(reg, strings.NewReader(""),
		"rdma_netdev_prio_buf_discard_total",
		"rdma_pcie_outbound_stalled_percent",
		"rdma_phy_rx_corrected_bits_total"); err != nil {
		t.Fatalf("expected no netdev hardware metrics without the opt-in: %v", err)
	}
}

func TestCollectorExportsNetDevHWMetrics(t *testing.T) {
	t.Parallel()

	provider, netDevProvider := ethernetPFWithStats(map[string]uint64{
		"rx_prio3_buf_discard":           4,
		"rx_prio3_cong_discard":          5,
		"rx_prio3_discards":              6,
		"rx_prio4_marked":                7,
		"dev_out_of_buffer":              8,
		"rx_out_of_buffer":               9,
		"rx_discards_phy":                10,
		"outbound_pci_stalled_rd":        11,
		"outbound_pci_stalled_wr":        12,
		"outbound_pci_stalled_rd_events": 13,
		"outbound_pci_stalled_wr_events": 14,
		"outbound_pci_buffer_overflow":   15,
		"rx_pci_signal_integrity":        16,
		"tx_pci_signal_integrity":        17,
		"rx_corrected_bits_phy":          18,
		"rx_pcs_symbol_err_phy":          19,
		"rx_bits_phy":                    20,
		"rx_err_lane_0_phy":              21,
		"rx_err_lane_1_phy":              22,
		"rx_crc_errors_phy":              23,
		"link_down_events_phy":           24,
		"rx_prio0_pause":                 1,
		"rx0_packets":                    99,
		"rx_prio2_packets":               50,
	})

	c := New(provider, newDiscardLogger(),
		WithNetDevStatsProvider(netDevProvider),
		WithNetDevHWMetrics(true),
	)
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	expected := `
# HELP rdma_netdev_dev_out_of_buffer_total Number of times a device-owned queue lacked receive buffers. Ethtool dev_out_of_buffer; distinct from the sysfs QP WQE counter out_of_buffer.
# TYPE rdma_netdev_dev_out_of_buffer_total counter
rdma_netdev_dev_out_of_buffer_total{device="mlx5_0",netdev="ens1f0np0",port="1"} 8
# HELP rdma_netdev_prio_buf_discard_total Packets discarded due to lack of per-host receive buffers. Ethtool rx_prio[p]_buf_discard.
# TYPE rdma_netdev_prio_buf_discard_total counter
rdma_netdev_prio_buf_discard_total{device="mlx5_0",netdev="ens1f0np0",port="1",priority="3"} 4
# HELP rdma_netdev_prio_cong_discard_total Packets discarded due to per-host congestion. Ethtool rx_prio[p]_cong_discard.
# TYPE rdma_netdev_prio_cong_discard_total counter
rdma_netdev_prio_cong_discard_total{device="mlx5_0",netdev="ens1f0np0",port="1",priority="3"} 5
# HELP rdma_netdev_prio_discards_total Packets discarded due to lack of receive buffers. Ethtool rx_prio[p]_discards.
# TYPE rdma_netdev_prio_discards_total counter
rdma_netdev_prio_discards_total{device="mlx5_0",netdev="ens1f0np0",port="1",priority="3"} 6
# HELP rdma_netdev_prio_ecn_marked_total Packets ECN-marked due to per-host congestion. Ethtool rx_prio[p]_marked.
# TYPE rdma_netdev_prio_ecn_marked_total counter
rdma_netdev_prio_ecn_marked_total{device="mlx5_0",netdev="ens1f0np0",port="1",priority="4"} 7
# HELP rdma_netdev_rx_discards_phy_total Packets dropped on the physical port due to lack of buffers. Ethtool rx_discards_phy.
# TYPE rdma_netdev_rx_discards_phy_total counter
rdma_netdev_rx_discards_phy_total{device="mlx5_0",netdev="ens1f0np0",port="1"} 10
# HELP rdma_netdev_rx_out_of_buffer_total Times the receive queue had no software buffers for incoming traffic. Ethtool rx_out_of_buffer; distinct from the sysfs QP WQE counter out_of_buffer.
# TYPE rdma_netdev_rx_out_of_buffer_total counter
rdma_netdev_rx_out_of_buffer_total{device="mlx5_0",netdev="ens1f0np0",port="1"} 9
# HELP rdma_pcie_outbound_buffer_overflow_total Packets dropped due to outbound PCI buffer overflow. Ethtool outbound_pci_buffer_overflow.
# TYPE rdma_pcie_outbound_buffer_overflow_total counter
rdma_pcie_outbound_buffer_overflow_total{device="mlx5_0",netdev="ens1f0np0",port="1"} 15
# HELP rdma_pcie_outbound_stalled_percent Percentage of the last 1 second that outbound PCI was stalled (kernel 0-100). Sampled at scrape time; stalls shorter than the scrape interval can be missed. Use rdma_pcie_outbound_stalled_seconds_total for alerting.
# TYPE rdma_pcie_outbound_stalled_percent gauge
rdma_pcie_outbound_stalled_percent{device="mlx5_0",netdev="ens1f0np0",op="rd",port="1"} 11
rdma_pcie_outbound_stalled_percent{device="mlx5_0",netdev="ens1f0np0",op="wr",port="1"} 12
# HELP rdma_pcie_outbound_stalled_seconds_total Cumulative seconds during which outbound PCI stall exceeded 30 percent. Primary stall signal; rate() is the fraction of time above the threshold.
# TYPE rdma_pcie_outbound_stalled_seconds_total counter
rdma_pcie_outbound_stalled_seconds_total{device="mlx5_0",netdev="ens1f0np0",op="rd",port="1"} 13
rdma_pcie_outbound_stalled_seconds_total{device="mlx5_0",netdev="ens1f0np0",op="wr",port="1"} 14
# HELP rdma_pcie_signal_integrity_total PCIe physical-layer signal integrity errors. Ethtool {rx,tx}_pci_signal_integrity.
# TYPE rdma_pcie_signal_integrity_total counter
rdma_pcie_signal_integrity_total{device="mlx5_0",direction="rx",netdev="ens1f0np0",port="1"} 16
rdma_pcie_signal_integrity_total{device="mlx5_0",direction="tx",netdev="ens1f0np0",port="1"} 17
# HELP rdma_phy_link_down_events_total Times the physical link operative state changed to down. Ethtool link_down_events_phy.
# TYPE rdma_phy_link_down_events_total counter
rdma_phy_link_down_events_total{device="mlx5_0",netdev="ens1f0np0",port="1"} 24
# HELP rdma_phy_rx_bits_total Bits that could have been received on the physical port. Denominator for interval FEC/BER ratios. Ethtool rx_bits_phy.
# TYPE rdma_phy_rx_bits_total counter
rdma_phy_rx_bits_total{device="mlx5_0",netdev="ens1f0np0",port="1"} 20
# HELP rdma_phy_rx_corrected_bits_total FEC-corrected bits on the physical port. Ethtool rx_corrected_bits_phy.
# TYPE rdma_phy_rx_corrected_bits_total counter
rdma_phy_rx_corrected_bits_total{device="mlx5_0",netdev="ens1f0np0",port="1"} 18
# HELP rdma_phy_rx_crc_errors_total Packets dropped due to FCS errors on the physical port. Ethtool rx_crc_errors_phy.
# TYPE rdma_phy_rx_crc_errors_total counter
rdma_phy_rx_crc_errors_total{device="mlx5_0",netdev="ens1f0np0",port="1"} 23
# HELP rdma_phy_rx_err_lane_total Physical raw errors per lane before FEC. Ethtool rx_err_lane_[l]_phy.
# TYPE rdma_phy_rx_err_lane_total counter
rdma_phy_rx_err_lane_total{device="mlx5_0",lane="0",netdev="ens1f0np0",port="1"} 21
rdma_phy_rx_err_lane_total{device="mlx5_0",lane="1",netdev="ens1f0np0",port="1"} 22
# HELP rdma_phy_rx_pcs_symbol_err_total Uncorrected or FEC-inactive symbol errors on the physical port. Ethtool rx_pcs_symbol_err_phy.
# TYPE rdma_phy_rx_pcs_symbol_err_total counter
rdma_phy_rx_pcs_symbol_err_total{device="mlx5_0",netdev="ens1f0np0",port="1"} 19
`

	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected),
		"rdma_netdev_prio_buf_discard_total",
		"rdma_netdev_prio_cong_discard_total",
		"rdma_netdev_prio_discards_total",
		"rdma_netdev_prio_ecn_marked_total",
		"rdma_netdev_dev_out_of_buffer_total",
		"rdma_netdev_rx_out_of_buffer_total",
		"rdma_netdev_rx_discards_phy_total",
		"rdma_pcie_outbound_stalled_percent",
		"rdma_pcie_outbound_stalled_seconds_total",
		"rdma_pcie_outbound_buffer_overflow_total",
		"rdma_pcie_signal_integrity_total",
		"rdma_phy_rx_corrected_bits_total",
		"rdma_phy_rx_pcs_symbol_err_total",
		"rdma_phy_rx_bits_total",
		"rdma_phy_rx_err_lane_total",
		"rdma_phy_rx_crc_errors_total",
		"rdma_phy_link_down_events_total",
	); err != nil {
		t.Fatalf("unexpected netdev hardware metrics output: %v", err)
	}
}

func TestCollectorNetDevHWMetricsAreSparse(t *testing.T) {
	t.Parallel()

	provider, netDevProvider := ethernetPFWithStats(map[string]uint64{
		"rx_prio3_buf_discard": 4,
	})

	c := New(provider, newDiscardLogger(),
		WithNetDevStatsProvider(netDevProvider),
		WithNetDevHWMetrics(true),
	)
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	expected := `
# HELP rdma_netdev_prio_buf_discard_total Packets discarded due to lack of per-host receive buffers. Ethtool rx_prio[p]_buf_discard.
# TYPE rdma_netdev_prio_buf_discard_total counter
rdma_netdev_prio_buf_discard_total{device="mlx5_0",netdev="ens1f0np0",port="1",priority="3"} 4
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected),
		"rdma_netdev_prio_buf_discard_total",
		"rdma_netdev_prio_cong_discard_total",
		"rdma_netdev_prio_discards_total"); err != nil {
		t.Fatalf("expected sparse priority emission: %v", err)
	}
}

func TestCollectorNetDevHWMetricsDedupesSharedNetDev(t *testing.T) {
	t.Parallel()

	provider := &stubProvider{
		devices: []rdma.Device{
			{
				Name: "mlx5_0",
				Ports: []rdma.Port{
					{
						ID:         1,
						Attributes: rdma.PortAttributes{LinkLayer: "Ethernet", NetDev: "ens1f0np0"},
					},
					{
						ID:         2,
						Attributes: rdma.PortAttributes{LinkLayer: "Ethernet", NetDev: "ens1f0np0"},
					},
				},
			},
		},
	}
	netDevProvider := newStubNetDevStatsProvider()
	netDevProvider.stats["ens1f0np0"] = map[string]uint64{
		"rx_prio0_pause":       1,
		"rx_prio3_buf_discard": 4,
	}

	c := New(provider, newDiscardLogger(),
		WithNetDevStatsProvider(netDevProvider),
		WithNetDevHWMetrics(true),
	)
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	expected := `
# HELP rdma_netdev_prio_buf_discard_total Packets discarded due to lack of per-host receive buffers. Ethtool rx_prio[p]_buf_discard.
# TYPE rdma_netdev_prio_buf_discard_total counter
rdma_netdev_prio_buf_discard_total{device="mlx5_0",netdev="ens1f0np0",port="1",priority="3"} 4
# HELP rdma_roce_pfc_pause_frames_total RoCEv2 PFC pause frames from ethtool stats. direction=rx: the peer XOFFed this NIC so this NIC cannot transmit on that priority. direction=tx: this NIC XOFFed the peer because it is not absorbing that priority.
# TYPE rdma_roce_pfc_pause_frames_total counter
rdma_roce_pfc_pause_frames_total{device="mlx5_0",direction="rx",netdev="ens1f0np0",port="1",priority="0"} 1
rdma_roce_pfc_pause_frames_total{device="mlx5_0",direction="rx",netdev="ens1f0np0",port="2",priority="0"} 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected),
		"rdma_roce_pfc_pause_frames_total",
		"rdma_netdev_prio_buf_discard_total"); err != nil {
		t.Fatalf("unexpected shared-netdev metrics: %v", err)
	}

	if got := netDevProvider.CallCount("ens1f0np0"); got != 1 {
		t.Fatalf("expected netdev provider to be called once, got %d", got)
	}
}

func TestCollectorSkipsNetDevHWMetricsForVirtualFunction(t *testing.T) {
	t.Parallel()

	provider := &stubProvider{
		devices: []rdma.Device{
			{
				Name: "mlx5_12",
				IsVF: true,
				Ports: []rdma.Port{
					{
						ID:         1,
						Attributes: rdma.PortAttributes{LinkLayer: "Ethernet", NetDev: "enp26s0v0"},
					},
				},
			},
		},
	}
	netDevProvider := newStubNetDevStatsProvider()
	netDevProvider.stats["enp26s0v0"] = map[string]uint64{
		"rx_prio3_buf_discard": 99,
	}

	c := New(provider, newDiscardLogger(),
		WithNetDevStatsProvider(netDevProvider),
		WithNetDevHWMetrics(true),
	)
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	if err := testutil.GatherAndCompare(reg, strings.NewReader(""),
		"rdma_netdev_prio_buf_discard_total"); err != nil {
		t.Fatalf("expected VF netdev hardware metrics to be omitted: %v", err)
	}
	if got := netDevProvider.CallCount("enp26s0v0"); got != 0 {
		t.Fatalf("expected VF netdev provider not to be called, got %d", got)
	}
}

func TestCollectorOmitsPFCWhenDisabled(t *testing.T) {
	t.Parallel()

	provider, netDevProvider := ethernetPFWithStats(map[string]uint64{
		"rx_prio0_pause":       1,
		"rx_prio3_buf_discard": 4,
	})

	c := New(provider, newDiscardLogger(),
		WithNetDevStatsProvider(netDevProvider),
		WithRoCEPFCMetrics(false),
		WithNetDevHWMetrics(true),
	)
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	if err := testutil.GatherAndCompare(reg, strings.NewReader(""),
		"rdma_roce_pfc_pause_frames_total"); err != nil {
		t.Fatalf("expected PFC metrics to be omitted: %v", err)
	}

	expected := `
# HELP rdma_netdev_prio_buf_discard_total Packets discarded due to lack of per-host receive buffers. Ethtool rx_prio[p]_buf_discard.
# TYPE rdma_netdev_prio_buf_discard_total counter
rdma_netdev_prio_buf_discard_total{device="mlx5_0",netdev="ens1f0np0",port="1",priority="3"} 4
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected),
		"rdma_netdev_prio_buf_discard_total"); err != nil {
		t.Fatalf("expected netdev hardware metrics with PFC disabled: %v", err)
	}
}

func TestCollectorIncrementsNetDevHWErrorCounterOnly(t *testing.T) {
	t.Parallel()

	provider, netDevProvider := ethernetPFWithStats(nil)
	netDevProvider.errs["ens1f0np0"] = errors.New("boom")

	c := New(provider, newDiscardLogger(),
		WithNetDevStatsProvider(netDevProvider),
		WithRoCEPFCMetrics(false),
		WithNetDevHWMetrics(true),
	)
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("unexpected gather error: %v", err)
	}

	if got := findMetricValue(t, mfs, "rdma_netdev_scrape_errors_total"); got != 1 {
		t.Fatalf("expected netdev scrape error counter to be 1, got %v", got)
	}
	if got := findMetricValue(t, mfs, "rdma_roce_pfc_scrape_errors_total"); got != 0 {
		t.Fatalf("expected PFC scrape error counter to stay 0, got %v", got)
	}
}

func TestCollectorIncrementsBothNetDevErrorCountersWhenBothEnabled(t *testing.T) {
	t.Parallel()

	provider, netDevProvider := ethernetPFWithStats(nil)
	netDevProvider.errs["ens1f0np0"] = errors.New("boom")

	c := New(provider, newDiscardLogger(),
		WithNetDevStatsProvider(netDevProvider),
		WithNetDevHWMetrics(true),
	)
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("unexpected gather error: %v", err)
	}

	if got := findMetricValue(t, mfs, "rdma_roce_pfc_scrape_errors_total"); got != 1 {
		t.Fatalf("expected PFC scrape error counter to be 1, got %v", got)
	}
	if got := findMetricValue(t, mfs, "rdma_netdev_scrape_errors_total"); got != 1 {
		t.Fatalf("expected netdev scrape error counter to be 1, got %v", got)
	}
}

func TestCollectorExportsOptionalCounters(t *testing.T) {
	t.Parallel()

	provider := &stubProvider{
		devices: []rdma.Device{
			{
				Name: "mlx5_0",
				Ports: []rdma.Port{
					{ID: 1},
				},
			},
		},
	}
	opt := newStubOptionalCounterProvider()
	opt.counters["mlx5_0/1"] = []rdmanl.OptionalCounter{
		{Name: "cc_rx_ce_pkts", Enabled: true, Value: 11, HasValue: true},
		{Name: "cc_rx_cnp_pkts", Enabled: false},
		{Name: "cc_tx_cnp_pkts", Enabled: true, Value: 22, HasValue: true},
	}

	c := New(provider, newDiscardLogger(), WithOptionalCounterProvider(opt))
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	expected := `
# HELP rdma_cc_rx_ce_pkts_total The number of received RoCEv2 packets marked Congestion Experienced (ECN CE). Optional mlx5 counter read via RDMA netlink; not present in sysfs hw_counters.
# TYPE rdma_cc_rx_ce_pkts_total counter
rdma_cc_rx_ce_pkts_total{device="mlx5_0",port="1"} 11
# HELP rdma_cc_tx_cnp_pkts_total The number of congestion notification packets (CNP) transmitted. Optional mlx5 counter read via RDMA netlink; not present in sysfs hw_counters.
# TYPE rdma_cc_tx_cnp_pkts_total counter
rdma_cc_tx_cnp_pkts_total{device="mlx5_0",port="1"} 22
# HELP rdma_optional_counter_enabled Whether an optional RDMA hardware counter is enabled on the port. 1 means currently enabled; 0 means supported but disabled. The exporter never enables counters.
# TYPE rdma_optional_counter_enabled gauge
rdma_optional_counter_enabled{counter="cc_rx_ce_pkts",device="mlx5_0",port="1"} 1
rdma_optional_counter_enabled{counter="cc_rx_cnp_pkts",device="mlx5_0",port="1"} 0
rdma_optional_counter_enabled{counter="cc_tx_cnp_pkts",device="mlx5_0",port="1"} 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected),
		"rdma_optional_counter_enabled",
		"rdma_cc_rx_ce_pkts_total",
		"rdma_cc_rx_cnp_pkts_total",
		"rdma_cc_tx_cnp_pkts_total",
	); err != nil {
		t.Fatalf("unexpected optional counter metrics: %v", err)
	}
}

func TestCollectorOptionalCountersIncludeVirtualFunctions(t *testing.T) {
	t.Parallel()

	provider := &stubProvider{
		devices: []rdma.Device{
			{
				Name: "mlx5_12",
				IsVF: true,
				Ports: []rdma.Port{
					{ID: 1},
				},
			},
		},
	}
	opt := newStubOptionalCounterProvider()
	opt.counters["mlx5_12/1"] = []rdmanl.OptionalCounter{
		{Name: "cc_rx_ce_pkts", Enabled: true, Value: 3, HasValue: true},
	}

	c := New(provider, newDiscardLogger(), WithOptionalCounterProvider(opt))
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	expected := `
# HELP rdma_cc_rx_ce_pkts_total The number of received RoCEv2 packets marked Congestion Experienced (ECN CE). Optional mlx5 counter read via RDMA netlink; not present in sysfs hw_counters.
# TYPE rdma_cc_rx_ce_pkts_total counter
rdma_cc_rx_ce_pkts_total{device="mlx5_12",port="1"} 3
# HELP rdma_optional_counter_enabled Whether an optional RDMA hardware counter is enabled on the port. 1 means currently enabled; 0 means supported but disabled. The exporter never enables counters.
# TYPE rdma_optional_counter_enabled gauge
rdma_optional_counter_enabled{counter="cc_rx_ce_pkts",device="mlx5_12",port="1"} 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected),
		"rdma_optional_counter_enabled",
		"rdma_cc_rx_ce_pkts_total",
	); err != nil {
		t.Fatalf("unexpected VF optional counter metrics: %v", err)
	}
}

func TestCollectorSkipsOptionalTotalWhenPresentInSysfs(t *testing.T) {
	t.Parallel()

	provider := &stubProvider{
		devices: []rdma.Device{
			{
				Name: "mlx5_0",
				Ports: []rdma.Port{
					{
						ID: 1,
						HwStats: map[string]uint64{
							"cc_rx_ce_pkts": 5,
						},
					},
				},
			},
		},
	}
	opt := newStubOptionalCounterProvider()
	opt.counters["mlx5_0/1"] = []rdmanl.OptionalCounter{
		{Name: "cc_rx_ce_pkts", Enabled: true, Value: 99, HasValue: true},
	}

	c := New(provider, newDiscardLogger(), WithOptionalCounterProvider(opt))
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	expected := `
# HELP rdma_cc_rx_ce_pkts_total The number of received RoCEv2 packets marked Congestion Experienced (ECN CE). Optional mlx5 counter read via RDMA netlink; not present in sysfs hw_counters.
# TYPE rdma_cc_rx_ce_pkts_total counter
rdma_cc_rx_ce_pkts_total{device="mlx5_0",port="1"} 5
# HELP rdma_optional_counter_enabled Whether an optional RDMA hardware counter is enabled on the port. 1 means currently enabled; 0 means supported but disabled. The exporter never enables counters.
# TYPE rdma_optional_counter_enabled gauge
rdma_optional_counter_enabled{counter="cc_rx_ce_pkts",device="mlx5_0",port="1"} 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected),
		"rdma_cc_rx_ce_pkts_total",
		"rdma_optional_counter_enabled",
	); err != nil {
		t.Fatalf("unexpected overlapping optional metrics: %v", err)
	}
}

func TestCollectorIncrementsOptionalCounterError(t *testing.T) {
	t.Parallel()

	provider := &stubProvider{
		devices: []rdma.Device{
			{
				Name:  "mlx5_0",
				Ports: []rdma.Port{{ID: 1}},
			},
		},
	}
	opt := newStubOptionalCounterProvider()
	opt.errs["mlx5_0/1"] = errors.New("boom")

	c := New(provider, newDiscardLogger(), WithOptionalCounterProvider(opt))
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("unexpected gather error: %v", err)
	}
	value := findMetricValue(t, mfs, "rdma_optional_counter_scrape_errors_total")
	if value != 1 {
		t.Fatalf("expected optional counter scrape errors to be 1, got %v", value)
	}
}

func TestCollectorOmitsUndocumentedOptionalTotals(t *testing.T) {
	t.Parallel()

	provider := &stubProvider{
		devices: []rdma.Device{
			{
				Name:  "mlx5_0",
				Ports: []rdma.Port{{ID: 1}},
			},
		},
	}
	opt := newStubOptionalCounterProvider()
	opt.counters["mlx5_0/1"] = []rdmanl.OptionalCounter{
		{Name: "rdma_rx_packets", Enabled: true, Value: 99, HasValue: true},
		{Name: "cc_rx_ce_pkts", Enabled: true, Value: 11, HasValue: true},
	}

	c := New(provider, newDiscardLogger(), WithOptionalCounterProvider(opt))
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	expected := `
# HELP rdma_cc_rx_ce_pkts_total The number of received RoCEv2 packets marked Congestion Experienced (ECN CE). Optional mlx5 counter read via RDMA netlink; not present in sysfs hw_counters.
# TYPE rdma_cc_rx_ce_pkts_total counter
rdma_cc_rx_ce_pkts_total{device="mlx5_0",port="1"} 11
# HELP rdma_optional_counter_enabled Whether an optional RDMA hardware counter is enabled on the port. 1 means currently enabled; 0 means supported but disabled. The exporter never enables counters.
# TYPE rdma_optional_counter_enabled gauge
rdma_optional_counter_enabled{counter="cc_rx_ce_pkts",device="mlx5_0",port="1"} 1
rdma_optional_counter_enabled{counter="rdma_rx_packets",device="mlx5_0",port="1"} 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected),
		"rdma_optional_counter_enabled",
		"rdma_cc_rx_ce_pkts_total",
		"rdma_rdma_rx_packets_total",
	); err != nil {
		t.Fatalf("unexpected optional counter metrics: %v", err)
	}
}

func TestCollectorOptionalEnabledWithoutValueIncrementsError(t *testing.T) {
	t.Parallel()

	provider := &stubProvider{
		devices: []rdma.Device{
			{
				Name:  "mlx5_0",
				Ports: []rdma.Port{{ID: 1}},
			},
		},
	}
	opt := newStubOptionalCounterProvider()
	opt.counters["mlx5_0/1"] = []rdmanl.OptionalCounter{
		{Name: "cc_rx_ce_pkts", Enabled: true, HasValue: false},
	}

	c := New(provider, newDiscardLogger(), WithOptionalCounterProvider(opt))
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("unexpected gather error: %v", err)
	}
	value := findMetricValue(t, mfs, "rdma_optional_counter_scrape_errors_total")
	if value != 1 {
		t.Fatalf("expected enabled-without-value to increment scrape errors, got %v", value)
	}
}

func TestCollectorOptionalPrepareFailureSkipsPorts(t *testing.T) {
	t.Parallel()

	provider := &stubProvider{
		devices: []rdma.Device{
			{
				Name:  "mlx5_0",
				Ports: []rdma.Port{{ID: 1}},
			},
		},
	}
	opt := newStubOptionalCounterProvider()
	opt.prepareErr = errors.New("dump failed")
	opt.counters["mlx5_0/1"] = []rdmanl.OptionalCounter{
		{Name: "cc_rx_ce_pkts", Enabled: true, Value: 1, HasValue: true},
	}

	c := New(provider, newDiscardLogger(), WithOptionalCounterProvider(opt))
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("unexpected gather error: %v", err)
	}
	value := findMetricValue(t, mfs, "rdma_optional_counter_scrape_errors_total")
	if value != 1 {
		t.Fatalf("expected prepare failure to increment scrape errors, got %v", value)
	}
	if got := opt.CallCount("mlx5_0", 1); got != 0 {
		t.Fatalf("expected Counters not to run after Prepare failure, got %d", got)
	}
}

func TestCollectorPreparesOptionalCountersOncePerScrape(t *testing.T) {
	t.Parallel()

	provider := &stubProvider{
		devices: []rdma.Device{
			{
				Name: "mlx5_0",
				Ports: []rdma.Port{
					{ID: 1},
					{ID: 2},
				},
			},
		},
	}
	opt := newStubOptionalCounterProvider()

	c := New(provider, newDiscardLogger(), WithOptionalCounterProvider(opt))
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	if _, err := reg.Gather(); err != nil {
		t.Fatalf("unexpected gather error: %v", err)
	}
	if got := opt.PrepareCount(); got != 1 {
		t.Fatalf("expected Prepare once per scrape, got %d", got)
	}
	if got := opt.CallCount("mlx5_0", 1); got != 1 {
		t.Fatalf("expected Counters for port 1 once, got %d", got)
	}
	if got := opt.CallCount("mlx5_0", 2); got != 1 {
		t.Fatalf("expected Counters for port 2 once, got %d", got)
	}
}

func TestCollectorOmitsOptionalMetricsWithoutProvider(t *testing.T) {
	t.Parallel()

	provider := &stubProvider{
		devices: []rdma.Device{
			{
				Name:  "mlx5_0",
				Ports: []rdma.Port{{ID: 1}},
			},
		},
	}
	c := New(provider, newDiscardLogger())
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("unexpected gather error: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() == "rdma_optional_counter_scrape_errors_total" || mf.GetName() == "rdma_optional_counter_enabled" {
			t.Fatalf("unexpected metric %s without optional provider", mf.GetName())
		}
	}
}

func ethernetPFWithStats(stats map[string]uint64) (*stubProvider, *stubNetDevStatsProvider) {
	provider := &stubProvider{
		devices: []rdma.Device{
			{
				Name: "mlx5_0",
				Ports: []rdma.Port{
					{
						ID: 1,
						Attributes: rdma.PortAttributes{
							LinkLayer: "Ethernet",
							NetDev:    "ens1f0np0",
						},
					},
				},
			},
		},
	}
	netDevProvider := newStubNetDevStatsProvider()
	if stats != nil {
		netDevProvider.stats["ens1f0np0"] = stats
	}
	return provider, netDevProvider
}

func findMetricValue(t *testing.T, families []*dto.MetricFamily, name string) float64 {
	t.Helper()
	for _, mf := range families {
		if mf.GetName() != name {
			continue
		}
		if len(mf.Metric) == 0 {
			return 0
		}
		return mf.Metric[0].GetCounter().GetValue()
	}
	t.Fatalf("metric %s not found", name)
	return 0
}

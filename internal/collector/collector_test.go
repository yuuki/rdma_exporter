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

type stubQPProvider struct {
	mu         sync.Mutex
	prepareN   int
	prepareErr error
	modes      map[string]rdmanl.QPMode
	sets       map[string][]rdmanl.QPSet
	modeErrs   map[string]error
	setErrs    map[string]error
	modeCalls  map[string]int
	setCalls   map[string]int
}

func newStubQPProvider() *stubQPProvider {
	return &stubQPProvider{
		modes:     make(map[string]rdmanl.QPMode),
		sets:      make(map[string][]rdmanl.QPSet),
		modeErrs:  make(map[string]error),
		setErrs:   make(map[string]error),
		modeCalls: make(map[string]int),
		setCalls:  make(map[string]int),
	}
}

func (s *stubQPProvider) Prepare(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prepareN++
	return s.prepareErr
}

func (s *stubQPProvider) QPMode(_ context.Context, device string, port int) (rdmanl.QPMode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := optionalKey(device, port)
	s.modeCalls[key]++
	if err, ok := s.modeErrs[key]; ok {
		return rdmanl.QPMode{}, err
	}
	return s.modes[key], nil
}

func (s *stubQPProvider) QPSets(_ context.Context, device string, port int) ([]rdmanl.QPSet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := optionalKey(device, port)
	s.setCalls[key]++
	if err, ok := s.setErrs[key]; ok {
		return nil, err
	}
	src := s.sets[key]
	out := make([]rdmanl.QPSet, len(src))
	copy(out, src)
	return out, nil
}

func (s *stubQPProvider) PrepareCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.prepareN
}

func (s *stubQPProvider) ModeCount(device string, port int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.modeCalls[optionalKey(device, port)]
}

func (s *stubQPProvider) SetCount(device string, port int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setCalls[optionalKey(device, port)]
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

func TestCollectorExportsLifespanAsGauge(t *testing.T) {
	t.Parallel()

	provider := &stubProvider{
		devices: []rdma.Device{
			{
				Name: "mlx5_0",
				Ports: []rdma.Port{
					{
						ID: 1,
						HwStats: map[string]uint64{
							"lifespan":     10,
							"symbol_error": 1,
						},
					},
				},
			},
		},
	}

	c := New(provider, newDiscardLogger())
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	expected := `
# HELP rdma_lifespan_milliseconds Maximum period in milliseconds between hardware counter updates. Two consecutive reads within this period might return the same values. This is a sysfs configuration knob (kernel default 10, writable range 0-10000); the exporter does not write it.
# TYPE rdma_lifespan_milliseconds gauge
rdma_lifespan_milliseconds{device="mlx5_0",port="1"} 10
# HELP rdma_symbol_error_total Total number of minor link errors detected on one or more physical lanes.
# TYPE rdma_symbol_error_total counter
rdma_symbol_error_total{device="mlx5_0",port="1"} 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected),
		"rdma_lifespan_milliseconds",
		"rdma_lifespan_total",
		"rdma_symbol_error_total",
	); err != nil {
		t.Fatalf("unexpected lifespan metrics: %v", err)
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
		"rx_prio3_buf_discard":          4,
		"outbound_pci_stalled_rd":       12,
		"rx_corrected_bits_phy":         8,
		"rx_prio0_pause":                1,
		"rx_global_pause":               1,
		"tx_global_pause":               2,
		"rx_global_pause_duration":      3,
		"tx_global_pause_duration":      4,
		"rx_global_pause_transition":    5,
		"tx_pause_storm_warning_events": 6,
		"tx_pause_storm_error_events":   7,
		"rx_vport_rdma_unicast_bytes":   8,
	})

	c := New(provider, newDiscardLogger(), WithNetDevStatsProvider(netDevProvider))
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	if err := testutil.GatherAndCompare(reg, strings.NewReader(""),
		"rdma_netdev_prio_buf_discard_total",
		"rdma_pcie_outbound_stalled_percent",
		"rdma_phy_rx_corrected_bits_total",
		"rdma_netdev_global_pause_frames_total",
		"rdma_netdev_global_pause_duration_total",
		"rdma_netdev_global_pause_transitions_total",
		"rdma_netdev_pause_storm_events_total",
		"rdma_netdev_vport_rdma_bytes_total",
		"rdma_netdev_vport_rdma_packets_total"); err != nil {
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

func TestCollectorExportsGlobalPauseAndPauseStorm(t *testing.T) {
	t.Parallel()

	provider, netDevProvider := ethernetPFWithStats(map[string]uint64{
		"rx_global_pause":               1,
		"tx_global_pause":               2,
		"rx_global_pause_duration":      3,
		"tx_global_pause_duration":      4,
		"rx_global_pause_transition":    5,
		"tx_pause_storm_warning_events": 6,
		"tx_pause_storm_error_events":   7,
		"rx_prio0_pause":                9,
	})

	c := New(provider, newDiscardLogger(),
		WithNetDevStatsProvider(netDevProvider),
		WithRoCEPFCMetrics(false),
		WithNetDevHWMetrics(true),
	)
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	expected := `
# HELP rdma_netdev_global_pause_duration_total Cumulative IEEE 802.3x pause duration in microseconds from ethtool. Occupancy is rate()/1e6. Direction has the same meaning as rdma_netdev_global_pause_frames_total. Present only when global pause mode is enabled.
# TYPE rdma_netdev_global_pause_duration_total counter
rdma_netdev_global_pause_duration_total{device="mlx5_0",direction="rx",netdev="ens1f0np0",port="1"} 3
rdma_netdev_global_pause_duration_total{device="mlx5_0",direction="tx",netdev="ens1f0np0",port="1"} 4
# HELP rdma_netdev_global_pause_frames_total IEEE 802.3x pause frames on the physical port from ethtool. direction=rx: pause frames received (this NIC was asked to stop transmitting). direction=tx: pause frames transmitted (this NIC asked the peer to stop transmitting). Present only when global pause mode is enabled, not PFC. Observation only.
# TYPE rdma_netdev_global_pause_frames_total counter
rdma_netdev_global_pause_frames_total{device="mlx5_0",direction="rx",netdev="ens1f0np0",port="1"} 1
rdma_netdev_global_pause_frames_total{device="mlx5_0",direction="tx",netdev="ens1f0np0",port="1"} 2
# HELP rdma_netdev_global_pause_transitions_total IEEE 802.3x XOFF-to-XON transitions on the physical port from ethtool rx_global_pause_transition. mlx5 exposes receive only. Present only when global pause mode is enabled.
# TYPE rdma_netdev_global_pause_transitions_total counter
rdma_netdev_global_pause_transitions_total{device="mlx5_0",netdev="ens1f0np0",port="1"} 5
# HELP rdma_netdev_pause_storm_events_total Times the device sent pause frames for a long period. severity=warning: stalled past a watermark. severity=error: timed out and pause transmission was disabled; drops may have occurred while pause TX was off. Ethtool tx_pause_storm_{warning,error}_events.
# TYPE rdma_netdev_pause_storm_events_total counter
rdma_netdev_pause_storm_events_total{device="mlx5_0",netdev="ens1f0np0",port="1",severity="error"} 7
rdma_netdev_pause_storm_events_total{device="mlx5_0",netdev="ens1f0np0",port="1",severity="warning"} 6
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected),
		"rdma_netdev_global_pause_frames_total",
		"rdma_netdev_global_pause_duration_total",
		"rdma_netdev_global_pause_transitions_total",
		"rdma_netdev_pause_storm_events_total",
		"rdma_roce_pfc_pause_frames_total",
	); err != nil {
		t.Fatalf("unexpected global pause metrics: %v", err)
	}
}

func TestCollectorExportsVPortRDMATraffic(t *testing.T) {
	t.Parallel()

	provider, netDevProvider := ethernetPFWithStats(map[string]uint64{
		"rx_vport_rdma_unicast_bytes":     1,
		"tx_vport_rdma_unicast_bytes":     2,
		"rx_vport_rdma_multicast_bytes":   0,
		"tx_vport_rdma_multicast_bytes":   4,
		"rx_vport_rdma_unicast_packets":   5,
		"tx_vport_rdma_unicast_packets":   6,
		"rx_vport_rdma_multicast_packets": 7,
		"tx_vport_rdma_multicast_packets": 8,
		"rx_vport_unicast_bytes":          99,
		"tx_vport_unicast_packets":        98,
		"rx_vport_broadcast_bytes":        97,
		"vport_loopback_bytes":            96,
		"rx_steer_missed_packets":         95,
		"rx_packets":                      94,
		"rx_prio0_pause":                  9,
	})

	c := New(provider, newDiscardLogger(),
		WithNetDevStatsProvider(netDevProvider),
		WithRoCEPFCMetrics(false),
		WithNetDevHWMetrics(true),
		WithNetDevPhysPortName(func(string) string { return "p0" }),
	)
	reg := prometheus.NewPedanticRegistry()
	reg.MustRegister(c)

	expected := `
# HELP rdma_netdev_vport_rdma_bytes_total RDMA octets steered to or from this netdev's function vport. Ethtool {rx,tx}_vport_rdma_{unicast,multicast}_bytes. Not a physical-port *_phy total and not a sum of other function vports. Distinct from sysfs port_rcv_data and rdma_qp_{rx,tx}_bytes_total. Omitted when sriov_totalvfs is absent.
# TYPE rdma_netdev_vport_rdma_bytes_total counter
rdma_netdev_vport_rdma_bytes_total{device="mlx5_0",direction="rx",netdev="ens1f0np0",port="1",traffic="multicast"} 0
rdma_netdev_vport_rdma_bytes_total{device="mlx5_0",direction="rx",netdev="ens1f0np0",port="1",traffic="unicast"} 1
rdma_netdev_vport_rdma_bytes_total{device="mlx5_0",direction="tx",netdev="ens1f0np0",port="1",traffic="multicast"} 4
rdma_netdev_vport_rdma_bytes_total{device="mlx5_0",direction="tx",netdev="ens1f0np0",port="1",traffic="unicast"} 2
# HELP rdma_netdev_vport_rdma_packets_total RDMA packets steered to or from this netdev's function vport. Ethtool {rx,tx}_vport_rdma_{unicast,multicast}_packets. Not a physical-port *_phy total and not a sum of other function vports. Distinct from sysfs port packet counters and rdma_qp_{rx,tx}_packets_total. Omitted when sriov_totalvfs is absent.
# TYPE rdma_netdev_vport_rdma_packets_total counter
rdma_netdev_vport_rdma_packets_total{device="mlx5_0",direction="rx",netdev="ens1f0np0",port="1",traffic="multicast"} 7
rdma_netdev_vport_rdma_packets_total{device="mlx5_0",direction="rx",netdev="ens1f0np0",port="1",traffic="unicast"} 5
rdma_netdev_vport_rdma_packets_total{device="mlx5_0",direction="tx",netdev="ens1f0np0",port="1",traffic="multicast"} 8
rdma_netdev_vport_rdma_packets_total{device="mlx5_0",direction="tx",netdev="ens1f0np0",port="1",traffic="unicast"} 6
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected),
		"rdma_netdev_vport_rdma_bytes_total",
		"rdma_netdev_vport_rdma_packets_total",
		"rdma_roce_pfc_pause_frames_total",
	); err != nil {
		t.Fatalf("unexpected vport RDMA metrics: %v", err)
	}
}

func TestCollectorOmitsAmbiguousNetDevVPortRDMA(t *testing.T) {
	t.Parallel()

	provider := &stubProvider{
		devices: []rdma.Device{
			{
				Name:     "mlx5_0",
				PCIAddr:  "0000:1a:00.0",
				HasSRIOV: true,
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
		"rx_prio0_pause":                1,
		"rx_prio3_buf_discard":          4,
		"rx_vport_rdma_unicast_bytes":   10,
		"rx_vport_rdma_unicast_packets": 11,
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
		"rdma_netdev_prio_buf_discard_total",
		"rdma_netdev_vport_rdma_bytes_total",
		"rdma_netdev_vport_rdma_packets_total"); err != nil {
		t.Fatalf("expected ambiguous netdev to omit only vport RDMA: %v", err)
	}
}

func TestCollectorOmitsNonPCIFunctionVPortRDMA(t *testing.T) {
	t.Parallel()

	provider, netDevProvider := ethernetPFWithStats(map[string]uint64{
		"rx_prio3_buf_discard":          4,
		"rx_vport_rdma_unicast_bytes":   10,
		"rx_vport_rdma_unicast_packets": 11,
	})
	provider.devices[0].PCIAddr = "mlx5_core.sf.1"

	c := New(provider, newDiscardLogger(),
		WithNetDevStatsProvider(netDevProvider),
		WithRoCEPFCMetrics(false),
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
		"rdma_netdev_vport_rdma_bytes_total",
		"rdma_netdev_vport_rdma_packets_total"); err != nil {
		t.Fatalf("expected non-PCI function to omit only vport RDMA: %v", err)
	}
}

func TestCollectorOmitsRepresentorVPortRDMA(t *testing.T) {
	t.Parallel()

	for _, physPortName := range []string{"pf0vf1", "pf0sf1", "c1pf0vf0", "c1pf0sf0", "pf0hpf"} {
		t.Run(physPortName, func(t *testing.T) {
			t.Parallel()

			provider, netDevProvider := ethernetPFWithStats(map[string]uint64{
				"rx_prio3_buf_discard":          4,
				"rx_vport_rdma_unicast_bytes":   10,
				"rx_vport_rdma_unicast_packets": 11,
			})

			c := New(provider, newDiscardLogger(),
				WithNetDevStatsProvider(netDevProvider),
				WithRoCEPFCMetrics(false),
				WithNetDevHWMetrics(true),
				WithNetDevPhysPortName(func(string) string { return physPortName }),
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
				"rdma_netdev_vport_rdma_bytes_total",
				"rdma_netdev_vport_rdma_packets_total"); err != nil {
				t.Fatalf("expected representor %q to omit only vport RDMA: %v", physPortName, err)
			}
		})
	}
}

func TestCollectorVPortRDMAMetricsAreSparse(t *testing.T) {
	t.Parallel()

	provider, netDevProvider := ethernetPFWithStats(map[string]uint64{
		"rx_vport_rdma_unicast_bytes":   1,
		"tx_vport_rdma_unicast_packets": 6,
	})

	c := New(provider, newDiscardLogger(),
		WithNetDevStatsProvider(netDevProvider),
		WithRoCEPFCMetrics(false),
		WithNetDevHWMetrics(true),
		WithNetDevPhysPortName(func(string) string { return "p0" }),
	)
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	expected := `
# HELP rdma_netdev_vport_rdma_bytes_total RDMA octets steered to or from this netdev's function vport. Ethtool {rx,tx}_vport_rdma_{unicast,multicast}_bytes. Not a physical-port *_phy total and not a sum of other function vports. Distinct from sysfs port_rcv_data and rdma_qp_{rx,tx}_bytes_total. Omitted when sriov_totalvfs is absent.
# TYPE rdma_netdev_vport_rdma_bytes_total counter
rdma_netdev_vport_rdma_bytes_total{device="mlx5_0",direction="rx",netdev="ens1f0np0",port="1",traffic="unicast"} 1
# HELP rdma_netdev_vport_rdma_packets_total RDMA packets steered to or from this netdev's function vport. Ethtool {rx,tx}_vport_rdma_{unicast,multicast}_packets. Not a physical-port *_phy total and not a sum of other function vports. Distinct from sysfs port packet counters and rdma_qp_{rx,tx}_packets_total. Omitted when sriov_totalvfs is absent.
# TYPE rdma_netdev_vport_rdma_packets_total counter
rdma_netdev_vport_rdma_packets_total{device="mlx5_0",direction="tx",netdev="ens1f0np0",port="1",traffic="unicast"} 6
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected),
		"rdma_netdev_vport_rdma_bytes_total",
		"rdma_netdev_vport_rdma_packets_total"); err != nil {
		t.Fatalf("expected sparse vport RDMA emission: %v", err)
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
		"rdma_netdev_prio_discards_total",
		"rdma_netdev_global_pause_frames_total",
		"rdma_netdev_global_pause_duration_total",
		"rdma_netdev_global_pause_transitions_total",
		"rdma_netdev_pause_storm_events_total",
		"rdma_netdev_vport_rdma_bytes_total",
		"rdma_netdev_vport_rdma_packets_total"); err != nil {
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
		"rx_prio3_buf_discard":          99,
		"rx_vport_rdma_unicast_bytes":   88,
		"rx_vport_rdma_unicast_packets": 87,
	}

	c := New(provider, newDiscardLogger(),
		WithNetDevStatsProvider(netDevProvider),
		WithNetDevHWMetrics(true),
	)
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	if err := testutil.GatherAndCompare(reg, strings.NewReader(""),
		"rdma_netdev_prio_buf_discard_total",
		"rdma_netdev_vport_rdma_bytes_total",
		"rdma_netdev_vport_rdma_packets_total"); err != nil {
		t.Fatalf("expected VF netdev hardware metrics to be omitted: %v", err)
	}
	if got := netDevProvider.CallCount("enp26s0v0"); got != 0 {
		t.Fatalf("expected VF netdev provider not to be called, got %d", got)
	}
}

func TestCollectorOmitsVPortRDMAWithoutSRIOVKeepsHW(t *testing.T) {
	t.Parallel()

	provider := &stubProvider{
		devices: []rdma.Device{
			{
				Name:     "mlx5_0",
				PCIAddr:  "0000:00:04.0",
				IsVF:     false,
				HasSRIOV: false,
				Ports: []rdma.Port{
					{
						ID: 1,
						Attributes: rdma.PortAttributes{
							LinkLayer: "Ethernet",
							NetDev:    "ens4",
						},
					},
				},
			},
		},
	}
	netDevProvider := newStubNetDevStatsProvider()
	netDevProvider.stats["ens4"] = map[string]uint64{
		"rx_prio3_buf_discard":          4,
		"rx_vport_rdma_unicast_bytes":   88,
		"rx_vport_rdma_unicast_packets": 87,
	}

	c := New(provider, newDiscardLogger(),
		WithNetDevStatsProvider(netDevProvider),
		WithRoCEPFCMetrics(false),
		WithNetDevHWMetrics(true),
		WithNetDevPhysPortName(func(string) string { return "p0" }),
	)
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	expected := `
# HELP rdma_netdev_prio_buf_discard_total Packets discarded due to lack of per-host receive buffers. Ethtool rx_prio[p]_buf_discard.
# TYPE rdma_netdev_prio_buf_discard_total counter
rdma_netdev_prio_buf_discard_total{device="mlx5_0",netdev="ens4",port="1",priority="3"} 4
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected),
		"rdma_netdev_prio_buf_discard_total",
		"rdma_netdev_vport_rdma_bytes_total",
		"rdma_netdev_vport_rdma_packets_total"); err != nil {
		t.Fatalf("expected missing sriov_totalvfs to omit only vport RDMA: %v", err)
	}
	if got := netDevProvider.CallCount("ens4"); got != 1 {
		t.Fatalf("expected netdev provider to be called once, got %d", got)
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
		{Name: "rdma_foo", Enabled: true, Value: 99, HasValue: true},
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
rdma_optional_counter_enabled{counter="rdma_foo",device="mlx5_0",port="1"} 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected),
		"rdma_optional_counter_enabled",
		"rdma_cc_rx_ce_pkts_total",
		"rdma_foo_total",
		"rdma_rdma_foo_total",
		"rdma_optional_rdma_foo_total",
		"rdma_optional_foo_total",
	); err != nil {
		t.Fatalf("unexpected optional counter metrics: %v", err)
	}
}

func TestOptionalTrafficMetricName(t *testing.T) {
	t.Parallel()

	want := map[string]string{
		"rdma_rx_bytes":   "rdma_optional_rx_bytes_total",
		"rdma_tx_bytes":   "rdma_optional_tx_bytes_total",
		"rdma_rx_packets": "rdma_optional_rx_packets_total",
		"rdma_tx_packets": "rdma_optional_tx_packets_total",
	}
	if len(optionalTrafficSpecs) != len(want) {
		t.Fatalf("optionalTrafficSpecs has %d entries, want %d", len(optionalTrafficSpecs), len(want))
	}
	for netlinkName, metricName := range want {
		got, ok := optionalTrafficMetricName(netlinkName)
		if !ok {
			t.Fatalf("optionalTrafficMetricName(%q) missing", netlinkName)
		}
		if got != metricName {
			t.Fatalf("optionalTrafficMetricName(%q)=%q, want %q", netlinkName, got, metricName)
		}
		if strings.Contains(got, "rdma_optional_rdma_") {
			t.Fatalf("optionalTrafficMetricName(%q) kept a doubled rdma_ prefix: %q", netlinkName, got)
		}
	}
	if _, ok := optionalTrafficMetricName("rdma_foo"); ok {
		t.Fatal("optionalTrafficMetricName(rdma_foo) should be unmapped")
	}
	if _, ok := optionalTrafficMetricName("cc_rx_ce_pkts"); ok {
		t.Fatal("optionalTrafficMetricName(cc_rx_ce_pkts) should stay on the cc_* path")
	}

	sysfsName := buildMetricName("rx_bytes", map[string]metricEntry{})
	if sysfsName != "rdma_rx_bytes_total" {
		t.Fatalf("sysfs buildMetricName(rx_bytes)=%q, want rdma_rx_bytes_total", sysfsName)
	}
	if qpCounterMetricName("rdma_rx_bytes") != "rdma_qp_rx_bytes_total" {
		t.Fatalf("qpCounterMetricName(rdma_rx_bytes)=%q, want rdma_qp_rx_bytes_total", qpCounterMetricName("rdma_rx_bytes"))
	}

	for _, help := range []string{optionalTrafficBytesHelp, optionalTrafficPacketsHelp} {
		lower := strings.ToLower(help)
		if strings.Contains(lower, "ethtool") {
			t.Fatalf("optional traffic HELP must not mention ethtool: %q", help)
		}
		if strings.Contains(lower, "all qp") {
			t.Fatalf("optional traffic HELP must not say all QPs: %q", help)
		}
		for _, phrase := range []string{"flow counter", "Linux 6.15", "never enables"} {
			if !strings.Contains(help, phrase) {
				t.Fatalf("optional traffic HELP missing %q: %q", phrase, help)
			}
		}
	}
}

func TestCollectorExportsOptionalTrafficTotals(t *testing.T) {
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
		{Name: "rdma_rx_bytes", Enabled: true, Value: 11, HasValue: true},
		{Name: "rdma_tx_bytes", Enabled: true, Value: 22, HasValue: true},
		{Name: "rdma_rx_packets", Enabled: true, Value: 33, HasValue: true},
		{Name: "rdma_tx_packets", Enabled: true, Value: 44, HasValue: true},
		{Name: "cc_rx_ce_pkts", Enabled: true, Value: 55, HasValue: true},
	}

	c := New(provider, newDiscardLogger(), WithOptionalCounterProvider(opt))
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	expected := `
# HELP rdma_cc_rx_ce_pkts_total The number of received RoCEv2 packets marked Congestion Experienced (ECN CE). Optional mlx5 counter read via RDMA netlink; not present in sysfs hw_counters.
# TYPE rdma_cc_rx_ce_pkts_total counter
rdma_cc_rx_ce_pkts_total{device="mlx5_0",port="1"} 55
# HELP rdma_optional_counter_enabled Whether an optional RDMA hardware counter is enabled on the port. 1 means currently enabled; 0 means supported but disabled. The exporter never enables counters.
# TYPE rdma_optional_counter_enabled gauge
rdma_optional_counter_enabled{counter="cc_rx_ce_pkts",device="mlx5_0",port="1"} 1
rdma_optional_counter_enabled{counter="rdma_rx_bytes",device="mlx5_0",port="1"} 1
rdma_optional_counter_enabled{counter="rdma_rx_packets",device="mlx5_0",port="1"} 1
rdma_optional_counter_enabled{counter="rdma_tx_bytes",device="mlx5_0",port="1"} 1
rdma_optional_counter_enabled{counter="rdma_tx_packets",device="mlx5_0",port="1"} 1
# HELP rdma_optional_rx_bytes_total ` + optionalTrafficBytesHelp + `
# TYPE rdma_optional_rx_bytes_total counter
rdma_optional_rx_bytes_total{device="mlx5_0",port="1"} 11
# HELP rdma_optional_rx_packets_total ` + optionalTrafficPacketsHelp + `
# TYPE rdma_optional_rx_packets_total counter
rdma_optional_rx_packets_total{device="mlx5_0",port="1"} 33
# HELP rdma_optional_tx_bytes_total ` + optionalTrafficBytesHelp + `
# TYPE rdma_optional_tx_bytes_total counter
rdma_optional_tx_bytes_total{device="mlx5_0",port="1"} 22
# HELP rdma_optional_tx_packets_total ` + optionalTrafficPacketsHelp + `
# TYPE rdma_optional_tx_packets_total counter
rdma_optional_tx_packets_total{device="mlx5_0",port="1"} 44
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected),
		"rdma_optional_counter_enabled",
		"rdma_cc_rx_ce_pkts_total",
		"rdma_optional_rx_bytes_total",
		"rdma_optional_tx_bytes_total",
		"rdma_optional_rx_packets_total",
		"rdma_optional_tx_packets_total",
		"rdma_rx_bytes_total",
		"rdma_rdma_rx_bytes_total",
		"rdma_optional_rdma_rx_bytes_total",
	); err != nil {
		t.Fatalf("unexpected optional traffic metrics: %v", err)
	}
}

func TestCollectorDescribesOptionalTrafficDescs(t *testing.T) {
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
		{Name: "rdma_rx_bytes", Enabled: true, Value: 1, HasValue: true},
		{Name: "rdma_tx_bytes", Enabled: true, Value: 2, HasValue: true},
		{Name: "rdma_rx_packets", Enabled: true, Value: 3, HasValue: true},
		{Name: "rdma_tx_packets", Enabled: true, Value: 4, HasValue: true},
	}

	c := New(provider, newDiscardLogger(), WithOptionalCounterProvider(opt))
	reg := prometheus.NewPedanticRegistry()
	reg.MustRegister(c)

	expected := `
# HELP rdma_optional_rx_bytes_total ` + optionalTrafficBytesHelp + `
# TYPE rdma_optional_rx_bytes_total counter
rdma_optional_rx_bytes_total{device="mlx5_0",port="1"} 1
# HELP rdma_optional_rx_packets_total ` + optionalTrafficPacketsHelp + `
# TYPE rdma_optional_rx_packets_total counter
rdma_optional_rx_packets_total{device="mlx5_0",port="1"} 3
# HELP rdma_optional_tx_bytes_total ` + optionalTrafficBytesHelp + `
# TYPE rdma_optional_tx_bytes_total counter
rdma_optional_tx_bytes_total{device="mlx5_0",port="1"} 2
# HELP rdma_optional_tx_packets_total ` + optionalTrafficPacketsHelp + `
# TYPE rdma_optional_tx_packets_total counter
rdma_optional_tx_packets_total{device="mlx5_0",port="1"} 4
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected),
		"rdma_optional_rx_bytes_total",
		"rdma_optional_tx_bytes_total",
		"rdma_optional_rx_packets_total",
		"rdma_optional_tx_packets_total",
	); err != nil {
		t.Fatalf("pedantic optional traffic describe/collect: %v", err)
	}
}

func TestCollectorOptionalTrafficDisabledAndMissingValue(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		counter   rdmanl.OptionalCounter
		wantError bool
	}{
		{name: "rx_bytes_disabled", counter: rdmanl.OptionalCounter{Name: "rdma_rx_bytes", Enabled: false, Value: 11, HasValue: true}},
		{name: "tx_bytes_disabled", counter: rdmanl.OptionalCounter{Name: "rdma_tx_bytes", Enabled: false, Value: 22, HasValue: true}},
		{name: "rx_packets_disabled", counter: rdmanl.OptionalCounter{Name: "rdma_rx_packets", Enabled: false, Value: 33, HasValue: true}},
		{name: "tx_packets_disabled", counter: rdmanl.OptionalCounter{Name: "rdma_tx_packets", Enabled: false, Value: 44, HasValue: true}},
		{name: "rx_bytes_no_value", counter: rdmanl.OptionalCounter{Name: "rdma_rx_bytes", Enabled: true, HasValue: false}, wantError: true},
		{name: "tx_bytes_no_value", counter: rdmanl.OptionalCounter{Name: "rdma_tx_bytes", Enabled: true, HasValue: false}, wantError: true},
		{name: "rx_packets_no_value", counter: rdmanl.OptionalCounter{Name: "rdma_rx_packets", Enabled: true, HasValue: false}, wantError: true},
		{name: "tx_packets_no_value", counter: rdmanl.OptionalCounter{Name: "rdma_tx_packets", Enabled: true, HasValue: false}, wantError: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
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
			opt.counters["mlx5_0/1"] = []rdmanl.OptionalCounter{tc.counter}

			c := New(provider, newDiscardLogger(), WithOptionalCounterProvider(opt))
			reg := prometheus.NewRegistry()
			reg.MustRegister(c)

			metricName, ok := optionalTrafficMetricName(tc.counter.Name)
			if !ok {
				t.Fatalf("missing metric mapping for %q", tc.counter.Name)
			}
			mfs, err := reg.Gather()
			if err != nil {
				t.Fatalf("unexpected gather error: %v", err)
			}
			for _, mf := range mfs {
				name := mf.GetName()
				if name == metricName || strings.Contains(name, "rdma_rdma_") || strings.Contains(name, "rdma_optional_rdma_") {
					t.Fatalf("unexpected %s when %s", name, tc.name)
				}
			}
			gotError := findMetricValue(t, mfs, "rdma_optional_counter_scrape_errors_total")
			wantError := 0.0
			if tc.wantError {
				wantError = 1
			}
			if gotError != wantError {
				t.Fatalf("scrape errors = %v, want %v", gotError, wantError)
			}
			enabled := 0.0
			if tc.counter.Enabled {
				enabled = 1
			}
			if got := findLabeledGauge(t, mfs, "rdma_optional_counter_enabled", "counter", tc.counter.Name); got != enabled {
				t.Fatalf("enabled gauge = %v, want %v", got, enabled)
			}
		})
	}
}

func TestCollectorOptionalTrafficWithSysfsRxBytes(t *testing.T) {
	t.Parallel()

	provider := &stubProvider{
		devices: []rdma.Device{
			{
				Name: "mlx5_0",
				Ports: []rdma.Port{
					{
						ID: 1,
						HwStats: map[string]uint64{
							"rx_bytes": 5,
						},
					},
				},
			},
		},
	}
	opt := newStubOptionalCounterProvider()
	opt.counters["mlx5_0/1"] = []rdmanl.OptionalCounter{
		{Name: "rdma_rx_bytes", Enabled: true, Value: 99, HasValue: true},
	}

	c := New(provider, newDiscardLogger(), WithOptionalCounterProvider(opt))
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	expected := `
# HELP rdma_optional_counter_enabled Whether an optional RDMA hardware counter is enabled on the port. 1 means currently enabled; 0 means supported but disabled. The exporter never enables counters.
# TYPE rdma_optional_counter_enabled gauge
rdma_optional_counter_enabled{counter="rdma_rx_bytes",device="mlx5_0",port="1"} 1
# HELP rdma_optional_rx_bytes_total ` + optionalTrafficBytesHelp + `
# TYPE rdma_optional_rx_bytes_total counter
rdma_optional_rx_bytes_total{device="mlx5_0",port="1"} 99
# HELP rdma_rx_bytes_total RDMA port hardware counter sourced from sysfs hw_counters.
# TYPE rdma_rx_bytes_total counter
rdma_rx_bytes_total{device="mlx5_0",port="1"} 5
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected),
		"rdma_rx_bytes_total",
		"rdma_optional_rx_bytes_total",
		"rdma_optional_counter_enabled",
	); err != nil {
		t.Fatalf("unexpected sysfs rx_bytes collision metrics: %v", err)
	}
}

func TestCollectorSkipsOptionalTrafficWhenSysfsHasRawName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		netlinkName    string
		sysfsMetric    string
		optionalMetric string
	}{
		{"rdma_rx_bytes", "rdma_rdma_rx_bytes_total", "rdma_optional_rx_bytes_total"},
		{"rdma_tx_bytes", "rdma_rdma_tx_bytes_total", "rdma_optional_tx_bytes_total"},
		{"rdma_rx_packets", "rdma_rdma_rx_packets_total", "rdma_optional_rx_packets_total"},
		{"rdma_tx_packets", "rdma_rdma_tx_packets_total", "rdma_optional_tx_packets_total"},
	}

	for _, tc := range cases {
		t.Run(tc.netlinkName, func(t *testing.T) {
			t.Parallel()

			provider := &stubProvider{
				devices: []rdma.Device{
					{
						Name: "mlx5_0",
						Ports: []rdma.Port{
							{
								ID: 1,
								HwStats: map[string]uint64{
									tc.netlinkName: 5,
								},
							},
						},
					},
				},
			}
			opt := newStubOptionalCounterProvider()
			opt.counters["mlx5_0/1"] = []rdmanl.OptionalCounter{
				{Name: tc.netlinkName, Enabled: true, Value: 99, HasValue: true},
			}

			c := New(provider, newDiscardLogger(), WithOptionalCounterProvider(opt))
			reg := prometheus.NewRegistry()
			reg.MustRegister(c)

			expected := `
# HELP rdma_optional_counter_enabled Whether an optional RDMA hardware counter is enabled on the port. 1 means currently enabled; 0 means supported but disabled. The exporter never enables counters.
# TYPE rdma_optional_counter_enabled gauge
rdma_optional_counter_enabled{counter="` + tc.netlinkName + `",device="mlx5_0",port="1"} 1
# HELP ` + tc.sysfsMetric + ` RDMA port hardware counter sourced from sysfs hw_counters.
# TYPE ` + tc.sysfsMetric + ` counter
` + tc.sysfsMetric + `{device="mlx5_0",port="1"} 5
`
			if err := testutil.GatherAndCompare(reg, strings.NewReader(expected),
				tc.sysfsMetric,
				tc.optionalMetric,
				"rdma_optional_counter_enabled",
			); err != nil {
				t.Fatalf("unexpected raw-name skip metrics: %v", err)
			}
		})
	}
}

func TestCollectorOptionalAndQPTrafficAreDistinct(t *testing.T) {
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
		{Name: "rdma_rx_bytes", Enabled: true, Value: 10, HasValue: true},
	}
	qp := newStubQPProvider()
	qp.modes["mlx5_0/1"] = rdmanl.QPMode{Mode: "auto", MaskType: true}
	qp.sets["mlx5_0/1"] = []rdmanl.QPSet{
		{
			Mode:   "auto",
			QPType: "RC",
			Stats:  map[string]uint64{"rdma_rx_bytes": 3},
		},
	}

	c := New(provider, newDiscardLogger(), WithOptionalCounterProvider(opt), WithQPCounterProvider(qp))
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	expected := `
# HELP rdma_optional_rx_bytes_total ` + optionalTrafficBytesHelp + `
# TYPE rdma_optional_rx_bytes_total counter
rdma_optional_rx_bytes_total{device="mlx5_0",port="1"} 10
# HELP rdma_qp_rx_bytes_total ` + qpCounterHelp + `
# TYPE rdma_qp_rx_bytes_total counter
rdma_qp_rx_bytes_total{device="mlx5_0",port="1",qp_type="RC"} 3
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected),
		"rdma_optional_rx_bytes_total",
		"rdma_qp_rx_bytes_total",
	); err != nil {
		t.Fatalf("unexpected optional+QP traffic metrics: %v", err)
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
		if strings.HasPrefix(mf.GetName(), "rdma_optional_") {
			t.Fatalf("unexpected metric %s without optional provider", mf.GetName())
		}
		if strings.HasPrefix(mf.GetName(), "rdma_qp_") {
			t.Fatalf("unexpected metric %s without QP provider", mf.GetName())
		}
	}
}

func TestCollectorExportsAutoTypeQPCounters(t *testing.T) {
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
	qp := newStubQPProvider()
	qp.modes["mlx5_0/1"] = rdmanl.QPMode{Mode: "auto", MaskType: true}
	qp.sets["mlx5_0/1"] = []rdmanl.QPSet{
		{
			Mode:   "auto",
			QPType: "RC",
			Stats: map[string]uint64{
				"out_of_buffer":     7,
				"duplicate_request": 3,
			},
		},
		{
			Mode:   "manual",
			QPType: "RC",
			Stats: map[string]uint64{
				"out_of_buffer": 99,
			},
		},
		{
			Mode:   "auto",
			QPType: "UD",
			HasPID: true,
			Stats: map[string]uint64{
				"out_of_buffer": 88,
			},
		},
	}

	c := New(provider, newDiscardLogger(), WithQPCounterProvider(qp))
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	expected := `
# HELP rdma_qp_auto_mask Whether auto-mode grouping includes this criterion. type and pid are independent bits.
# TYPE rdma_qp_auto_mask gauge
rdma_qp_auto_mask{criteria="pid",device="mlx5_0",port="1"} 0
rdma_qp_auto_mask{criteria="type",device="mlx5_0",port="1"} 1
# HELP rdma_qp_counter_mode QP statistic counter bind mode on the port. One series per mode; value 1 is the current mode.
# TYPE rdma_qp_counter_mode gauge
rdma_qp_counter_mode{device="mlx5_0",mode="auto",port="1"} 1
rdma_qp_counter_mode{device="mlx5_0",mode="manual",port="1"} 0
rdma_qp_counter_mode{device="mlx5_0",mode="none",port="1"} 0
# HELP rdma_qp_duplicate_request_total ` + qpCounterHelp + `
# TYPE rdma_qp_duplicate_request_total counter
rdma_qp_duplicate_request_total{device="mlx5_0",port="1",qp_type="RC"} 3
# HELP rdma_qp_out_of_buffer_total ` + qpCounterHelp + `
# TYPE rdma_qp_out_of_buffer_total counter
rdma_qp_out_of_buffer_total{device="mlx5_0",port="1",qp_type="RC"} 7
# HELP rdma_qp_scrape_status Result of the last QP counter dump for this port. overflow means the receive budget was exceeded and totals were omitted.
# TYPE rdma_qp_scrape_status gauge
rdma_qp_scrape_status{device="mlx5_0",port="1",result="error"} 0
rdma_qp_scrape_status{device="mlx5_0",port="1",result="ok"} 1
rdma_qp_scrape_status{device="mlx5_0",port="1",result="overflow"} 0
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected),
		"rdma_qp_counter_mode",
		"rdma_qp_auto_mask",
		"rdma_qp_scrape_status",
		"rdma_qp_out_of_buffer_total",
		"rdma_qp_duplicate_request_total",
	); err != nil {
		t.Fatalf("unexpected QP counter metrics: %v", err)
	}
}

func TestCollectorOmitsUndocumentedQPTotals(t *testing.T) {
	t.Parallel()

	provider := &stubProvider{
		devices: []rdma.Device{
			{
				Name:  "mlx5_0",
				Ports: []rdma.Port{{ID: 1}},
			},
		},
	}
	qp := newStubQPProvider()
	qp.modes["mlx5_0/1"] = rdmanl.QPMode{Mode: "auto", MaskType: true}
	qp.sets["mlx5_0/1"] = []rdmanl.QPSet{
		{
			Mode:   "auto",
			QPType: "RC",
			Stats: map[string]uint64{
				"out_of_buffer": 4,
				"weird_counter": 123,
			},
		},
	}

	c := New(provider, newDiscardLogger(), WithQPCounterProvider(qp))
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	expected := `
# HELP rdma_qp_out_of_buffer_total ` + qpCounterHelp + `
# TYPE rdma_qp_out_of_buffer_total counter
rdma_qp_out_of_buffer_total{device="mlx5_0",port="1",qp_type="RC"} 4
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected),
		"rdma_qp_out_of_buffer_total",
		"rdma_qp_weird_counter_total",
	); err != nil {
		t.Fatalf("unexpected QP allowlist metrics: %v", err)
	}
}

func TestCollectorExportsQPOptionalTraffic(t *testing.T) {
	t.Parallel()

	provider := &stubProvider{
		devices: []rdma.Device{
			{
				Name:  "mlx5_0",
				Ports: []rdma.Port{{ID: 1}},
			},
		},
	}
	qp := newStubQPProvider()
	qp.modes["mlx5_0/1"] = rdmanl.QPMode{Mode: "auto", MaskType: true}
	qp.sets["mlx5_0/1"] = []rdmanl.QPSet{
		{
			Mode:   "auto",
			QPType: "RC",
			Stats: map[string]uint64{
				"rdma_rx_bytes":   1,
				"rdma_tx_bytes":   2,
				"rdma_rx_packets": 3,
				"rdma_tx_packets": 4,
				"out_of_buffer":   5,
			},
		},
		{
			Mode:   "manual",
			QPType: "RC",
			Stats: map[string]uint64{
				"rdma_rx_bytes": 99,
			},
		},
	}

	c := New(provider, newDiscardLogger(), WithQPCounterProvider(qp))
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	expected := `
# HELP rdma_qp_out_of_buffer_total ` + qpCounterHelp + `
# TYPE rdma_qp_out_of_buffer_total counter
rdma_qp_out_of_buffer_total{device="mlx5_0",port="1",qp_type="RC"} 5
# HELP rdma_qp_rx_bytes_total ` + qpCounterHelp + `
# TYPE rdma_qp_rx_bytes_total counter
rdma_qp_rx_bytes_total{device="mlx5_0",port="1",qp_type="RC"} 1
# HELP rdma_qp_rx_packets_total ` + qpCounterHelp + `
# TYPE rdma_qp_rx_packets_total counter
rdma_qp_rx_packets_total{device="mlx5_0",port="1",qp_type="RC"} 3
# HELP rdma_qp_tx_bytes_total ` + qpCounterHelp + `
# TYPE rdma_qp_tx_bytes_total counter
rdma_qp_tx_bytes_total{device="mlx5_0",port="1",qp_type="RC"} 2
# HELP rdma_qp_tx_packets_total ` + qpCounterHelp + `
# TYPE rdma_qp_tx_packets_total counter
rdma_qp_tx_packets_total{device="mlx5_0",port="1",qp_type="RC"} 4
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected),
		"rdma_qp_out_of_buffer_total",
		"rdma_qp_rx_bytes_total",
		"rdma_qp_rx_packets_total",
		"rdma_qp_tx_bytes_total",
		"rdma_qp_tx_packets_total",
		"rdma_qp_rdma_rx_bytes_total",
	); err != nil {
		t.Fatalf("unexpected QP optional traffic metrics: %v", err)
	}
}

func TestCollectorQPEmptyDumpEmitsModeOnly(t *testing.T) {
	t.Parallel()

	provider := &stubProvider{
		devices: []rdma.Device{
			{
				Name:  "mlx5_0",
				Ports: []rdma.Port{{ID: 1}},
			},
		},
	}
	qp := newStubQPProvider()
	qp.modes["mlx5_0/1"] = rdmanl.QPMode{Mode: "auto", MaskType: true}

	c := New(provider, newDiscardLogger(), WithQPCounterProvider(qp))
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	expected := `
# HELP rdma_qp_auto_mask Whether auto-mode grouping includes this criterion. type and pid are independent bits.
# TYPE rdma_qp_auto_mask gauge
rdma_qp_auto_mask{criteria="pid",device="mlx5_0",port="1"} 0
rdma_qp_auto_mask{criteria="type",device="mlx5_0",port="1"} 1
# HELP rdma_qp_counter_mode QP statistic counter bind mode on the port. One series per mode; value 1 is the current mode.
# TYPE rdma_qp_counter_mode gauge
rdma_qp_counter_mode{device="mlx5_0",mode="auto",port="1"} 1
rdma_qp_counter_mode{device="mlx5_0",mode="manual",port="1"} 0
rdma_qp_counter_mode{device="mlx5_0",mode="none",port="1"} 0
# HELP rdma_qp_scrape_status Result of the last QP counter dump for this port. overflow means the receive budget was exceeded and totals were omitted.
# TYPE rdma_qp_scrape_status gauge
rdma_qp_scrape_status{device="mlx5_0",port="1",result="error"} 0
rdma_qp_scrape_status{device="mlx5_0",port="1",result="ok"} 1
rdma_qp_scrape_status{device="mlx5_0",port="1",result="overflow"} 0
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected),
		"rdma_qp_counter_mode",
		"rdma_qp_auto_mask",
		"rdma_qp_scrape_status",
		"rdma_qp_out_of_buffer_total",
	); err != nil {
		t.Fatalf("unexpected empty QP dump metrics: %v", err)
	}
}

func TestCollectorQPDumpOverflowOmitsTotals(t *testing.T) {
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
		{Name: "cc_rx_ce_pkts", Enabled: true, Value: 11, HasValue: true},
	}
	qp := newStubQPProvider()
	qp.modes["mlx5_0/1"] = rdmanl.QPMode{Mode: "auto", MaskType: true}
	qp.sets["mlx5_0/1"] = []rdmanl.QPSet{
		{
			Mode:   "auto",
			QPType: "RC",
			Stats:  map[string]uint64{"out_of_buffer": 7},
		},
	}
	qp.setErrs["mlx5_0/1"] = rdmanl.ErrDumpOverflow

	c := New(provider, newDiscardLogger(), WithOptionalCounterProvider(opt), WithQPCounterProvider(qp))
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	expected := `
# HELP rdma_cc_rx_ce_pkts_total The number of received RoCEv2 packets marked Congestion Experienced (ECN CE). Optional mlx5 counter read via RDMA netlink; not present in sysfs hw_counters.
# TYPE rdma_cc_rx_ce_pkts_total counter
rdma_cc_rx_ce_pkts_total{device="mlx5_0",port="1"} 11
# HELP rdma_qp_scrape_errors_total Total number of errors encountered while scraping QP counters via netlink, including dump overflow.
# TYPE rdma_qp_scrape_errors_total counter
rdma_qp_scrape_errors_total 1
# HELP rdma_qp_scrape_status Result of the last QP counter dump for this port. overflow means the receive budget was exceeded and totals were omitted.
# TYPE rdma_qp_scrape_status gauge
rdma_qp_scrape_status{device="mlx5_0",port="1",result="error"} 0
rdma_qp_scrape_status{device="mlx5_0",port="1",result="ok"} 0
rdma_qp_scrape_status{device="mlx5_0",port="1",result="overflow"} 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected),
		"rdma_cc_rx_ce_pkts_total",
		"rdma_qp_scrape_status",
		"rdma_qp_scrape_errors_total",
		"rdma_qp_out_of_buffer_total",
	); err != nil {
		t.Fatalf("unexpected overflow QP metrics: %v", err)
	}
	if got := opt.CallCount("mlx5_0", 1); got != 1 {
		t.Fatalf("expected optional counters to run despite QP overflow, got %d", got)
	}
}

func TestCollectorQPCountersIncludeVirtualFunctions(t *testing.T) {
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
	qp := newStubQPProvider()
	qp.modes["mlx5_12/1"] = rdmanl.QPMode{Mode: "auto", MaskType: true}
	qp.sets["mlx5_12/1"] = []rdmanl.QPSet{
		{
			Mode:   "auto",
			QPType: "RC",
			Stats:  map[string]uint64{"out_of_buffer": 2},
		},
	}

	c := New(provider, newDiscardLogger(), WithQPCounterProvider(qp))
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	expected := `
# HELP rdma_qp_out_of_buffer_total ` + qpCounterHelp + `
# TYPE rdma_qp_out_of_buffer_total counter
rdma_qp_out_of_buffer_total{device="mlx5_12",port="1",qp_type="RC"} 2
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected),
		"rdma_qp_out_of_buffer_total",
	); err != nil {
		t.Fatalf("unexpected VF QP metrics: %v", err)
	}
	if got := qp.ModeCount("mlx5_12", 1); got != 1 {
		t.Fatalf("expected QPMode for VF once, got %d", got)
	}
	if got := qp.SetCount("mlx5_12", 1); got != 1 {
		t.Fatalf("expected QPSets for VF once, got %d", got)
	}
}

func TestCollectorSumsDuplicateAutoTypeSets(t *testing.T) {
	t.Parallel()

	provider := &stubProvider{
		devices: []rdma.Device{
			{
				Name:  "mlx5_0",
				Ports: []rdma.Port{{ID: 1}},
			},
		},
	}
	qp := newStubQPProvider()
	qp.modes["mlx5_0/1"] = rdmanl.QPMode{Mode: "auto", MaskType: true}
	qp.sets["mlx5_0/1"] = []rdmanl.QPSet{
		{
			Mode:   "auto",
			QPType: "RC",
			Stats:  map[string]uint64{"out_of_buffer": 3},
		},
		{
			Mode:   "auto",
			QPType: "RC",
			Stats:  map[string]uint64{"out_of_buffer": 4},
		},
		{
			Mode:   "manual",
			QPType: "RC",
			Stats:  map[string]uint64{"out_of_buffer": 99},
		},
	}

	c := New(provider, newDiscardLogger(), WithQPCounterProvider(qp))
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	expected := `
# HELP rdma_qp_out_of_buffer_total ` + qpCounterHelp + `
# TYPE rdma_qp_out_of_buffer_total counter
rdma_qp_out_of_buffer_total{device="mlx5_0",port="1",qp_type="RC"} 7
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected),
		"rdma_qp_out_of_buffer_total",
	); err != nil {
		t.Fatalf("unexpected summed QP metrics: %v", err)
	}
}

func TestCollectorQPUnsupportedOmitsModeAndOk(t *testing.T) {
	t.Parallel()

	provider := &stubProvider{
		devices: []rdma.Device{
			{
				Name:  "mlx5_0",
				Ports: []rdma.Port{{ID: 1}},
			},
		},
	}
	qp := newStubQPProvider()
	qp.modeErrs["mlx5_0/1"] = rdmanl.ErrQPUnsupported

	c := New(provider, newDiscardLogger(), WithQPCounterProvider(qp))
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	expected := `
# HELP rdma_qp_scrape_errors_total Total number of errors encountered while scraping QP counters via netlink, including dump overflow.
# TYPE rdma_qp_scrape_errors_total counter
rdma_qp_scrape_errors_total 0
# HELP rdma_qp_scrape_status Result of the last QP counter dump for this port. overflow means the receive budget was exceeded and totals were omitted.
# TYPE rdma_qp_scrape_status gauge
rdma_qp_scrape_status{device="mlx5_0",port="1",result="error"} 1
rdma_qp_scrape_status{device="mlx5_0",port="1",result="ok"} 0
rdma_qp_scrape_status{device="mlx5_0",port="1",result="overflow"} 0
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected),
		"rdma_qp_counter_mode",
		"rdma_qp_auto_mask",
		"rdma_qp_scrape_status",
		"rdma_qp_scrape_errors_total",
		"rdma_qp_out_of_buffer_total",
	); err != nil {
		t.Fatalf("unexpected unsupported QP metrics: %v", err)
	}
}

func TestCollectorQPOverflowContinuesOtherPorts(t *testing.T) {
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
	qp := newStubQPProvider()
	qp.modes["mlx5_0/1"] = rdmanl.QPMode{Mode: "auto", MaskType: true}
	qp.modes["mlx5_0/2"] = rdmanl.QPMode{Mode: "auto", MaskType: true}
	qp.setErrs["mlx5_0/1"] = rdmanl.ErrDumpOverflow
	qp.sets["mlx5_0/2"] = []rdmanl.QPSet{
		{
			Mode:   "auto",
			QPType: "RC",
			Stats:  map[string]uint64{"out_of_buffer": 5},
		},
	}

	c := New(provider, newDiscardLogger(), WithQPCounterProvider(qp))
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	expected := `
# HELP rdma_qp_out_of_buffer_total ` + qpCounterHelp + `
# TYPE rdma_qp_out_of_buffer_total counter
rdma_qp_out_of_buffer_total{device="mlx5_0",port="2",qp_type="RC"} 5
# HELP rdma_qp_scrape_status Result of the last QP counter dump for this port. overflow means the receive budget was exceeded and totals were omitted.
# TYPE rdma_qp_scrape_status gauge
rdma_qp_scrape_status{device="mlx5_0",port="1",result="error"} 0
rdma_qp_scrape_status{device="mlx5_0",port="1",result="ok"} 0
rdma_qp_scrape_status{device="mlx5_0",port="1",result="overflow"} 1
rdma_qp_scrape_status{device="mlx5_0",port="2",result="error"} 0
rdma_qp_scrape_status{device="mlx5_0",port="2",result="ok"} 1
rdma_qp_scrape_status{device="mlx5_0",port="2",result="overflow"} 0
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected),
		"rdma_qp_out_of_buffer_total",
		"rdma_qp_scrape_status",
	); err != nil {
		t.Fatalf("unexpected multi-port overflow metrics: %v", err)
	}
}

func TestCollectorOmitsQPMetricsWithoutProvider(t *testing.T) {
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
		if strings.HasPrefix(mf.GetName(), "rdma_qp_") {
			t.Fatalf("unexpected metric %s without QP provider", mf.GetName())
		}
	}
}

func ethernetPFWithStats(stats map[string]uint64) (*stubProvider, *stubNetDevStatsProvider) {
	provider := &stubProvider{
		devices: []rdma.Device{
			{
				Name:     "mlx5_0",
				PCIAddr:  "0000:1a:00.0",
				HasSRIOV: true,
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

func findLabeledGauge(t *testing.T, families []*dto.MetricFamily, name, label, value string) float64 {
	t.Helper()
	for _, mf := range families {
		if mf.GetName() != name {
			continue
		}
		for _, metric := range mf.Metric {
			for _, pair := range metric.GetLabel() {
				if pair.GetName() == label && pair.GetValue() == value {
					return metric.GetGauge().GetValue()
				}
			}
		}
		t.Fatalf("metric %s with %s=%q not found", name, label, value)
	}
	t.Fatalf("metric %s not found", name)
	return 0
}

package collector

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"

	"github.com/yuuki/rdma_exporter/internal/rdma"
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

func newDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestCollectorExportsMetrics(t *testing.T) {
	t.Parallel()

	provider := &stubProvider{
		devices: []rdma.Device{
			{
				Name: "mlx5_0",
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
rdma_port_info{device="mlx5_0",link_layer="InfiniBand",link_speed="100 Gb/sec",link_width="4X",phys_state="LinkUp",port="1",state="ACTIVE"} 1
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
# HELP rdma_roce_pfc_pause_duration_total RoCEv2 PFC pause duration counter sourced from ethtool stats.
# TYPE rdma_roce_pfc_pause_duration_total counter
rdma_roce_pfc_pause_duration_total{device="mlx5_0",direction="rx",netdev="ens1f0np0",port="1",priority="4"} 30
# HELP rdma_roce_pfc_pause_frames_total RoCEv2 PFC pause frame counter sourced from ethtool stats.
# TYPE rdma_roce_pfc_pause_frames_total counter
rdma_roce_pfc_pause_frames_total{device="mlx5_0",direction="rx",netdev="ens1f0np0",port="1",priority="0"} 10
rdma_roce_pfc_pause_frames_total{device="mlx5_0",direction="tx",netdev="ens1f0np0",port="1",priority="3"} 20
# HELP rdma_roce_pfc_pause_transitions_total RoCEv2 PFC pause transition counter sourced from ethtool stats.
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

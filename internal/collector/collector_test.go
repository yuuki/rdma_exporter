package collector

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
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
							"symbol_errors": 1,
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
# HELP rdma_port_symbol_errors_total RDMA port hardware counter sourced from sysfs hw_counters.
# TYPE rdma_port_symbol_errors_total counter
rdma_port_symbol_errors_total{device="mlx5_0",port="1"} 1
# HELP rdma_port_info RDMA port metadata exported as labels.
# TYPE rdma_port_info gauge
rdma_port_info{device="mlx5_0",link_layer="InfiniBand",link_speed="100 Gb/sec",link_width="4X",phys_state="LinkUp",port="1",state="ACTIVE"} 1
# HELP rdma_port_rcv_data_total RDMA port counter sourced from sysfs counters.
# TYPE rdma_port_rcv_data_total counter
rdma_port_rcv_data_total{device="mlx5_0",port="1"} 5
# HELP rdma_port_xmit_data_total RDMA port counter sourced from sysfs counters.
# TYPE rdma_port_xmit_data_total counter
rdma_port_xmit_data_total{device="mlx5_0",port="1"} 10
`

	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected),
		"rdma_port_rcv_data_total", "rdma_port_xmit_data_total", "rdma_port_symbol_errors_total", "rdma_port_info"); err != nil {
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

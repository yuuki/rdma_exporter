package collector

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/yuuki/rdma_exporter/internal/rdma"
	"github.com/yuuki/rdma_exporter/internal/rdmanl"
)

// benchHost models a busy RoCE host: eight dual-port mlx5 devices, each port
// with the documented counters and hw_counters and an Ethernet netdev that
// reports PFC, buffer and PHY ethtool stats.
func benchHost() (*stubProvider, *stubNetDevStatsProvider, *stubOptionalCounterProvider, *stubQPProvider) {
	names := slices.Sorted(maps.Keys(metricSpecs))
	ethtool := map[string]uint64{"rx_discards_phy": 1, "rx_bits_phy": 2, "rx_corrected_bits_phy": 3, "link_down_events_phy": 4}
	for prio := range 8 {
		for _, suffix := range []string{"pause", "pause_duration", "pause_transition"} {
			ethtool[fmt.Sprintf("rx_prio%d_%s", prio, suffix)] = uint64(prio)
			ethtool[fmt.Sprintf("tx_prio%d_%s", prio, suffix)] = uint64(prio)
		}
		ethtool[fmt.Sprintf("rx_prio%d_discards", prio)] = uint64(prio)
	}

	provider := &stubProvider{}
	netdevs := newStubNetDevStatsProvider()
	optional := newStubOptionalCounterProvider()
	qp := newStubQPProvider()
	for d := range 8 {
		device := rdma.Device{Name: fmt.Sprintf("mlx5_%d", d), PCIAddr: fmt.Sprintf("0000:%02x:00.0", 0x1a+d), HasSRIOV: true}
		for p := 1; p <= 2; p++ {
			netdev := fmt.Sprintf("ens%df%dnp%d", d, p-1, p-1)
			port := rdma.Port{ID: p, Stats: map[string]uint64{}, HwStats: map[string]uint64{}, Attributes: rdma.PortAttributes{
				LinkLayer: "Ethernet", State: "ACTIVE", PhysState: "LINK_UP", LinkWidth: "4X", LinkSpeed: "100 Gb/sec", NetDev: netdev,
			}}
			for i, name := range names {
				if _, optional := optionalCounterValueNames[name]; optional {
					continue // netlink-only on real hardware
				}
				if i%2 == 0 {
					port.Stats[name] = uint64(i)
				} else {
					port.HwStats[name] = uint64(i)
				}
			}
			device.Ports = append(device.Ports, port)
			netdevs.stats[netdev] = ethtool
			optional.counters[optionalKey(device.Name, p)] = []rdmanl.OptionalCounter{
				{Name: "cc_rx_ce_pkts", Enabled: true, Value: 1, HasValue: true},
				{Name: "cc_rx_cnp_pkts", Enabled: true, Value: 2, HasValue: true},
				{Name: "rdma_rx_bytes", Enabled: false},
			}
			qp.modes[optionalKey(device.Name, p)] = rdmanl.QPMode{Mode: "auto", MaskType: true}
			qp.sets[optionalKey(device.Name, p)] = []rdmanl.QPSet{
				{Mode: "auto", QPType: "RC", Stats: map[string]uint64{"out_of_buffer": 1, "rx_write_requests": 2}},
				{Mode: "auto", QPType: "UD", Stats: map[string]uint64{"out_of_buffer": 3}},
			}
		}
		provider.devices = append(provider.devices, device)
	}
	return provider, netdevs, optional, qp
}

// BenchmarkGather measures a whole scrape of the registry, as /metrics runs
// it, for the sysfs families alone and with every collector enabled.
func BenchmarkGather(b *testing.B) {
	provider, netdevs, optional, qp := benchHost()
	for _, bc := range []struct {
		name string
		opts []Option
	}{
		{name: "sysfs"},
		{name: "all", opts: []Option{
			WithNetDevStatsProvider(netdevs),
			WithNetDevPhysPortName(func(string) string { return "" }),
			WithOptionalCounterProvider(optional),
			WithQPCounterProvider(qp),
		}},
	} {
		b.Run(bc.name, func(b *testing.B) {
			benchmarkGather(b, New(provider, newDiscardLogger(), bc.opts...))
		})
	}
}

// BenchmarkGatherSysfsFixtures scrapes each readable fixture tree of
// internal/rdma through the real sysfs provider.
func BenchmarkGatherSysfsFixtures(b *testing.B) {
	root := filepath.Join("..", "rdma", "testdata", "sysfs")
	entries, err := os.ReadDir(root)
	if err != nil {
		b.Fatal(err)
	}
	for _, entry := range entries {
		provider := rdma.NewSysfsProvider()
		provider.SetSysfsRoot(filepath.Join(root, entry.Name()))
		if devices, err := provider.Devices(context.Background()); err != nil || len(devices) == 0 {
			continue // error and empty scenarios
		}
		b.Run(entry.Name(), func(b *testing.B) {
			benchmarkGather(b, New(provider, newDiscardLogger()))
		})
	}
}

func benchmarkGather(b *testing.B, c *RdmaCollector) {
	b.Helper()
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := reg.Gather(); err != nil {
			b.Fatal(err)
		}
	}
}

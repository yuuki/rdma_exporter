package collector

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/model"

	"github.com/yuuki/rdma_exporter/internal/rdma"
	"github.com/yuuki/rdma_exporter/internal/rdmanl"
)

// FuzzCollectLabelValues feeds arbitrary strings into every label value the
// collector copies from sysfs, ethtool and RDMA netlink: device and netdev
// names, port attributes, PCI addresses, optional counter names and ethtool
// stat names. Netdev names in particular are chosen by whoever holds
// CAP_NET_ADMIN in the netdev's namespace and may be any bytes except '/',
// ':' and whitespace. Whatever they contain, a scrape must succeed: one
// rejected series fails the whole /metrics response.
func FuzzCollectLabelValues(f *testing.F) {
	f.Add("mlx5_0", "ens1f0np0", "Ethernet", "ACTIVE", "LINK_UP", "4X", "100 Gb/sec", "0000:1a:00.0", "", "cc_rx_ce_pkts", "rx_prio3_pause")
	f.Add("mlx5_bond_0", "bond0", "InfiniBand", "INIT", "POLLING", "2X", "200 Gb/sec", "", "mlx5_0", "rdma_rx_bytes", "rx_vport_rdma_unicast_bytes")
	f.Add("rdmap0s0", "", "", "", "", "", "", "0000:00:06.0", "", "", "rx_err_lane_0_phy")
	f.Add("irdma0", "eth0", "Ethernet", "4: ACTIVE", "5: LinkUp", "4X", "25 Gb/sec (1X EDR)", "0000:3b:00.0", "", "cc_tx_cnp_pkts", "tx_pause_storm_error_events")

	f.Fuzz(func(t *testing.T, device, netDev, linkLayer, state, physState, linkWidth, linkSpeed, pciAddr, pfDevice, optionalName, ethtoolStat string) {
		attrs := rdma.PortAttributes{
			LinkLayer: linkLayer,
			State:     state,
			PhysState: physState,
			LinkWidth: linkWidth,
			LinkSpeed: linkSpeed,
			NetDev:    netDev,
		}
		ethernet := attrs
		ethernet.LinkLayer = "Ethernet"
		provider := &stubProvider{devices: []rdma.Device{{
			Name:     device,
			PCIAddr:  pciAddr,
			HasSRIOV: true,
			PFDevice: pfDevice,
			Ports: []rdma.Port{
				// Port 1 always reaches the ethtool families; port 2 carries
				// the fuzzed link layer.
				{ID: 1, Stats: map[string]uint64{"port_xmit_data": 1}, HwStats: map[string]uint64{"out_of_sequence": 2}, Attributes: ethernet},
				{ID: 2, Stats: map[string]uint64{"port_rcv_data": 3}, Attributes: attrs},
			},
		}}}

		netdevs := newStubNetDevStatsProvider()
		netdevs.stats[netDev] = map[string]uint64{
			ethtoolStat:                   4,
			"rx_prio0_pause":              5,
			"rx_vport_rdma_unicast_bytes": 6,
			"rx_bits_phy":                 7,
		}

		// Netlink reports each counter name once per port.
		optional := newStubOptionalCounterProvider()
		for _, port := range []int{1, 2} {
			counters := []rdmanl.OptionalCounter{{Name: optionalName, Enabled: true, Value: 8, HasValue: true}}
			if optionalName != "cc_rx_cnp_pkts" {
				counters = append(counters, rdmanl.OptionalCounter{Name: "cc_rx_cnp_pkts", Enabled: true, Value: 9, HasValue: true})
			}
			optional.counters[optionalKey(device, port)] = counters
		}

		qp := newStubQPProvider()
		qp.modes[optionalKey(device, 1)] = rdmanl.QPMode{Mode: "auto", MaskType: true}
		qp.sets[optionalKey(device, 1)] = []rdmanl.QPSet{{Mode: "auto", QPType: "RC", Stats: map[string]uint64{"out_of_buffer": 10}}}

		c := New(provider, newDiscardLogger(),
			WithNetDevStatsProvider(netdevs),
			WithNetDevPhysPortName(func(string) string { return "" }),
			WithOptionalCounterProvider(optional),
			WithQPCounterProvider(qp),
		)
		reg := prometheus.NewRegistry()
		reg.MustRegister(c)

		families, err := reg.Gather()
		if err != nil {
			t.Fatalf("Gather: %v", err)
		}
		for _, mf := range families {
			for _, m := range mf.GetMetric() {
				for _, lp := range m.GetLabel() {
					if !utf8.ValidString(lp.GetValue()) {
						t.Fatalf("%s: label %s=%q is not valid UTF-8", mf.GetName(), lp.GetName(), lp.GetValue())
					}
				}
			}
		}

		// Valid label values pass through unchanged; invalid bytes become
		// U+FFFD and nothing else changes.
		info := portInfoLabels(t, families, "2")
		for label, raw := range map[string]string{
			"device":     device,
			"netdev":     netDev,
			"link_layer": linkLayer,
			"state":      state,
			"phys_state": physState,
			"link_width": linkWidth,
			"link_speed": linkSpeed,
			"pci_addr":   pciAddr,
			"pf_device":  pfDevice,
		} {
			if want := strings.ToValidUTF8(raw, "\uFFFD"); info[label] != want {
				t.Fatalf("rdma_port_info %s = %q, want %q", label, info[label], want)
			}
		}
	})
}

// portInfoLabels returns the labels of the rdma_port_info series of port.
func portInfoLabels(t *testing.T, families []*dto.MetricFamily, port string) map[string]string {
	t.Helper()
	for _, mf := range families {
		if mf.GetName() != "rdma_port_info" {
			continue
		}
		for _, m := range mf.GetMetric() {
			labels := make(map[string]string, len(m.GetLabel()))
			for _, lp := range m.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}
			if labels["port"] == port {
				return labels
			}
		}
	}
	t.Fatalf("no rdma_port_info series for port %s", port)
	return nil
}

// FuzzStatMetricName checks the metric names built from sysfs counter file
// names and netlink counter names: whatever the name, the descriptor must be
// a valid metric with the device and port labels, legacy-valid so that older
// Prometheus servers accept it, and stable across scrapes.
func FuzzStatMetricName(f *testing.F) {
	for _, seed := range []string{
		"port_xmit_data", "VL15_dropped", "symbol_error", "lifespan", "np_ecn_marked_roce_packets",
		"rx-icrc.encapsulated", "1st_counter", "", "_", "ÄÖ", "\xff", "rdma_rx_bytes",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, stat string) {
		name := buildMetricName(canonicalDocName(stat), map[string]metricEntry{})
		if !model.LegacyValidation.IsValidMetricName(name) {
			t.Fatalf("metric name %q for stat %q is not a legacy-valid metric name", name, stat)
		}
		if !strings.HasPrefix(name, "rdma_") || !strings.HasSuffix(name, "_total") {
			t.Fatalf("metric name %q for stat %q lacks the rdma_ prefix or _total suffix", name, stat)
		}

		// Only the descriptor caches; New would dominate the run time.
		c := &RdmaCollector{
			portStatMetrics:        make(map[string]metricEntry),
			portStatLookup:         make(map[string]string),
			portHwMetrics:          make(map[string]metricEntry),
			portHwStatLookup:       make(map[string]string),
			portOptionalMetrics:    make(map[string]metricEntry),
			portOptionalStatLookup: make(map[string]string),
		}
		for kind, descFor := range map[string]func(string) *prometheus.Desc{
			"counters":    c.statMetricDesc,
			"hw_counters": c.hwMetricDesc,
			"optional":    c.optionalMetricDesc,
		} {
			desc := descFor(stat)
			if _, err := prometheus.NewConstMetric(desc, prometheus.CounterValue, 1, "mlx5_0", "1"); err != nil {
				t.Fatalf("%s descriptor for stat %q is unusable: %v", kind, stat, err)
			}
			if again := descFor(stat); again != desc {
				t.Fatalf("%s descriptor for stat %q changed between calls: %v then %v", kind, stat, desc, again)
			}
		}
	})
}

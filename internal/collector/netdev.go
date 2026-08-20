package collector

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/yuuki/rdma_exporter/internal/rdma"
)

var (
	netdevPrioStatPattern          = regexp.MustCompile(`^rx_prio([0-7])_(buf_discard|cong_discard|discards|marked)$`)
	pciStallPercentPattern         = regexp.MustCompile(`^outbound_pci_stalled_(rd|wr)$`)
	pciStallSecondsPattern         = regexp.MustCompile(`^outbound_pci_stalled_(rd|wr)_events$`)
	pciSignalPattern               = regexp.MustCompile(`^(rx|tx)_pci_signal_integrity$`)
	phyLanePattern                 = regexp.MustCompile(`^rx_err_lane_([0-9]+)_phy$`)
	globalPauseFramesPattern       = regexp.MustCompile(`^(rx|tx)_global_pause$`)
	globalPauseDurationPattern     = regexp.MustCompile(`^(rx|tx)_global_pause_duration$`)
	globalPauseTransitionPattern   = regexp.MustCompile(`^rx_global_pause_transition$`)
	pauseStormEventsPattern        = regexp.MustCompile(`^tx_pause_storm_(warning|error)_events$`)
	vportRDMAPattern               = regexp.MustCompile(`^(rx|tx)_vport_rdma_(unicast|multicast)_(bytes|packets)$`)
	pciFunctionBDFPattern          = regexp.MustCompile(`(?i)^[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-9a-f]$`)
	representorPhysPortNamePattern = regexp.MustCompile(`^(c[0-9]+)?pf[0-9]+(hpf|(vf|sf)[0-9]+)$`)
)

func (c *RdmaCollector) emitNetDevHWMetrics(
	ch chan<- prometheus.Metric,
	deviceName, portID, netDev string,
	stats map[string]uint64,
	skipVPort bool,
) {
	for _, name := range sortedKeys(stats) {
		value := float64(stats[name])
		if matches := netdevPrioStatPattern.FindStringSubmatch(name); matches != nil {
			desc := c.netdevPrioDesc(matches[2])
			if desc == nil {
				continue
			}
			ch <- prometheus.MustNewConstMetric(desc, prometheus.CounterValue, value, deviceName, portID, netDev, matches[1])
			continue
		}
		if matches := pciStallPercentPattern.FindStringSubmatch(name); matches != nil {
			ch <- prometheus.MustNewConstMetric(
				c.pcieOutboundStalledPercentDesc,
				prometheus.GaugeValue,
				value,
				deviceName, portID, netDev, matches[1],
			)
			continue
		}
		if matches := pciStallSecondsPattern.FindStringSubmatch(name); matches != nil {
			ch <- prometheus.MustNewConstMetric(
				c.pcieOutboundStalledSecondsDesc,
				prometheus.CounterValue,
				value,
				deviceName, portID, netDev, matches[1],
			)
			continue
		}
		if matches := pciSignalPattern.FindStringSubmatch(name); matches != nil {
			ch <- prometheus.MustNewConstMetric(
				c.pcieSignalIntegrityDesc,
				prometheus.CounterValue,
				value,
				deviceName, portID, netDev, matches[1],
			)
			continue
		}
		if matches := phyLanePattern.FindStringSubmatch(name); matches != nil {
			ch <- prometheus.MustNewConstMetric(
				c.phyRxErrLaneDesc,
				prometheus.CounterValue,
				value,
				deviceName, portID, netDev, matches[1],
			)
			continue
		}
		if matches := globalPauseFramesPattern.FindStringSubmatch(name); matches != nil {
			ch <- prometheus.MustNewConstMetric(
				c.netdevGlobalPauseFramesDesc,
				prometheus.CounterValue,
				value,
				deviceName, portID, netDev, matches[1],
			)
			continue
		}
		if matches := globalPauseDurationPattern.FindStringSubmatch(name); matches != nil {
			ch <- prometheus.MustNewConstMetric(
				c.netdevGlobalPauseDurationDesc,
				prometheus.CounterValue,
				value,
				deviceName, portID, netDev, matches[1],
			)
			continue
		}
		if globalPauseTransitionPattern.MatchString(name) {
			ch <- prometheus.MustNewConstMetric(
				c.netdevGlobalPauseTransitionsDesc,
				prometheus.CounterValue,
				value,
				deviceName, portID, netDev,
			)
			continue
		}
		if matches := pauseStormEventsPattern.FindStringSubmatch(name); matches != nil {
			ch <- prometheus.MustNewConstMetric(
				c.netdevPauseStormEventsDesc,
				prometheus.CounterValue,
				value,
				deviceName, portID, netDev, matches[1],
			)
			continue
		}
		if matches := vportRDMAPattern.FindStringSubmatch(name); matches != nil {
			if skipVPort {
				continue
			}
			desc := c.netdevVPortRDMABytesDesc
			if matches[3] == "packets" {
				desc = c.netdevVPortRDMAPacketsDesc
			}
			ch <- prometheus.MustNewConstMetric(
				desc,
				prometheus.CounterValue,
				value,
				deviceName, portID, netDev, matches[1], matches[2],
			)
			continue
		}

		desc, ok := c.netDevPlainCounterDesc(name)
		if !ok {
			continue
		}
		ch <- prometheus.MustNewConstMetric(desc, prometheus.CounterValue, value, deviceName, portID, netDev)
	}
}

func (c *RdmaCollector) netdevPrioDesc(kind string) *prometheus.Desc {
	switch kind {
	case "buf_discard":
		return c.netdevPrioBufDiscardDesc
	case "cong_discard":
		return c.netdevPrioCongDiscardDesc
	case "discards":
		return c.netdevPrioDiscardsDesc
	case "marked":
		return c.netdevPrioECNMarkedDesc
	default:
		return nil
	}
}

func (c *RdmaCollector) netDevPlainCounterDesc(name string) (*prometheus.Desc, bool) {
	switch name {
	case "dev_out_of_buffer":
		return c.netdevDevOutOfBufferDesc, true
	case "rx_out_of_buffer":
		return c.netdevRxOutOfBufferDesc, true
	case "rx_discards_phy":
		return c.netdevRxDiscardsPhyDesc, true
	case "outbound_pci_buffer_overflow":
		return c.pcieOutboundOverflowDesc, true
	case "rx_corrected_bits_phy":
		return c.phyRxCorrectedBitsDesc, true
	case "rx_pcs_symbol_err_phy":
		return c.phyRxPCSSymbolErrDesc, true
	case "rx_bits_phy":
		return c.phyRxBitsDesc, true
	case "rx_crc_errors_phy":
		return c.phyRxCRCErrorsDesc, true
	case "link_down_events_phy":
		return c.phyLinkDownEventsDesc, true
	default:
		return nil, false
	}
}

func ethernetNetDevOwners(devices []rdma.Device) map[string]int {
	owners := make(map[string]int)
	for _, device := range devices {
		if device.IsVF {
			continue
		}
		for _, port := range device.Ports {
			if port.Attributes.LinkLayer != "Ethernet" || port.Attributes.NetDev == "" {
				continue
			}
			owners[port.Attributes.NetDev]++
		}
	}
	return owners
}

func isPCIFunctionBDF(pciAddr string) bool {
	return pciFunctionBDFPattern.MatchString(pciAddr)
}

func isRepresentorPhysPortName(name string) bool {
	return representorPhysPortNamePattern.MatchString(name)
}

func (c *RdmaCollector) lookupPhysPortName(netdev string) string {
	if c.physPortName != nil {
		return c.physPortName(netdev)
	}
	return readNetDevPhysPortName(netdev)
}

func readNetDevPhysPortName(netdev string) string {
	return readNetDevPhysPortNameFrom("/sys", netdev)
}

func readNetDevPhysPortNameFrom(root, netdev string) string {
	if netdev == "" || strings.ContainsAny(netdev, `/\:`) || strings.Contains(netdev, "..") {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(root, "class", "net", netdev, "phys_port_name"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

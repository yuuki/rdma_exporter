package collector

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/yuuki/rdma_exporter/internal/rdma"
	"github.com/yuuki/rdma_exporter/internal/rdmanl"
)

// Provider defines the subset of the rdma.Provider interface required by the collector.
type Provider interface {
	Devices(ctx context.Context) ([]rdma.Device, error)
}

// NetDevStatsProvider fetches ethtool-like statistics for a network device.
type NetDevStatsProvider interface {
	Stats(ctx context.Context, netDev string) (map[string]uint64, error)
}

// OptionalCounterProvider fetches optional RDMA hardware counters via netlink.
type OptionalCounterProvider interface {
	Prepare(ctx context.Context) error
	Counters(ctx context.Context, device string, port int) ([]rdmanl.OptionalCounter, error)
}

// QPCounterProvider fetches live bound QP counter sets via netlink GET/DUMP.
type QPCounterProvider interface {
	Prepare(ctx context.Context) error
	QPMode(ctx context.Context, device string, port int) (rdmanl.QPMode, error)
	QPSets(ctx context.Context, device string, port int) ([]rdmanl.QPSet, error)
}

// Option configures collector behavior.
type Option func(*RdmaCollector)

// RdmaCollector implements prometheus.Collector for RDMA device metrics.
type RdmaCollector struct {
	provider Provider
	logger   *slog.Logger

	portInfoDesc *prometheus.Desc

	lifespanMillisecondsDesc *prometheus.Desc

	portStatMetrics  map[string]metricEntry
	portStatLookup   map[string]string
	portHwMetrics    map[string]metricEntry
	portHwStatLookup map[string]string

	portOptionalMetrics    map[string]metricEntry
	portOptionalStatLookup map[string]string

	rocePFCPauseFramesDesc      *prometheus.Desc
	rocePFCPauseDurationDesc    *prometheus.Desc
	rocePFCPauseTransitionsDesc *prometheus.Desc

	netdevPrioBufDiscardDesc  *prometheus.Desc
	netdevPrioCongDiscardDesc *prometheus.Desc
	netdevPrioDiscardsDesc    *prometheus.Desc
	netdevPrioECNMarkedDesc   *prometheus.Desc
	netdevDevOutOfBufferDesc  *prometheus.Desc
	netdevRxOutOfBufferDesc   *prometheus.Desc
	netdevRxDiscardsPhyDesc   *prometheus.Desc

	pcieOutboundStalledPercentDesc *prometheus.Desc
	pcieOutboundStalledSecondsDesc *prometheus.Desc
	pcieOutboundOverflowDesc       *prometheus.Desc
	pcieSignalIntegrityDesc        *prometheus.Desc

	phyRxCorrectedBitsDesc *prometheus.Desc
	phyRxPCSSymbolErrDesc  *prometheus.Desc
	phyRxBitsDesc          *prometheus.Desc
	phyRxErrLaneDesc       *prometheus.Desc
	phyRxCRCErrorsDesc     *prometheus.Desc
	phyLinkDownEventsDesc  *prometheus.Desc

	netdevGlobalPauseFramesDesc      *prometheus.Desc
	netdevGlobalPauseDurationDesc    *prometheus.Desc
	netdevGlobalPauseTransitionsDesc *prometheus.Desc
	netdevPauseStormEventsDesc       *prometheus.Desc
	netdevVPortRDMABytesDesc         *prometheus.Desc
	netdevVPortRDMAPacketsDesc       *prometheus.Desc

	optionalCounterEnabledDesc *prometheus.Desc
	qpCounterModeDesc          *prometheus.Desc
	qpAutoMaskDesc             *prometheus.Desc
	qpScrapeStatusDesc         *prometheus.Desc
	qpValueDescs               map[string]*prometheus.Desc

	scrapeErrors                prometheus.Counter
	rocePFCScrapeErrors         prometheus.Counter
	netDevHWScrapeErrors        prometheus.Counter
	optionalCounterScrapeErrors prometheus.Counter
	qpScrapeErrors              prometheus.Counter

	netDevStatsProvider     NetDevStatsProvider
	optionalCounterProvider OptionalCounterProvider
	qpCounterProvider       QPCounterProvider
	collectRoCEPFC          bool
	collectNetDevHW         bool
	physPortName            func(string) string

	collectMu sync.Mutex
	ctxValue  atomic.Pointer[context.Context]
}

type metricEntry struct {
	desc    *prometheus.Desc
	docName string
}

type metricSpec struct {
	DocName string
	Help    string
}

var (
	rocePFCStatPattern = regexp.MustCompile(`^(rx|tx)_prio([0-7])_pause(?:_(duration|transition))?$`)

	// ref. "Understanding mlx5 Linux Counters and Status Parameters", https://enterprise-support.nvidia.com/s/article/understanding-mlx5-linux-counters-and-status-parameters
	metricSpecs = map[string]metricSpec{
		"port_rcv_data": {
			DocName: "port_rcv_data",
			Help:    "The total number of data octets, divided by 4 (counting in double words, 32 bits), received on all VLs from the port.",
		},
		"port_rcv_packets": {
			DocName: "port_rcv_packets",
			Help:    "Total number of packets (may include packets containing errors).",
		},
		"port_multicast_rcv_packets": {
			DocName: "port_multicast_rcv_packets",
			Help:    "Total number of multicast packets, including multicast packets containing errors.",
		},
		"port_unicast_rcv_packets": {
			DocName: "port_unicast_rcv_packets",
			Help:    "Total number of unicast packets, including unicast packets containing errors.",
		},
		"port_xmit_data": {
			DocName: "port_xmit_data",
			Help:    "The total number of data octets, divided by 4, transmitted on all VLs from the port.",
		},
		"port_xmit_packets": {
			DocName: "port_xmit_packets",
			Help:    "Total number of packets transmitted on all VLs from this port (may include packets with errors).",
		},
		"port_multicast_xmit_packets": {
			DocName: "port_multicast_xmit_packets",
			Help:    "Total number of multicast packets transmitted on all VLs from the port (may include multicast packets with errors).",
		},
		"port_unicast_xmit_packets": {
			DocName: "port_unicast_xmit_packets",
			Help:    "Total number of unicast packets transmitted on all VLs from the port (may include unicast packets with errors).",
		},
		"port_rcv_switch_relay_errors": {
			DocName: "port_rcv_switch_relay_errors",
			Help:    "Total number of packets received on the port that were discarded because they could not be forwarded by the switch relay.",
		},
		"port_rcv_errors": {
			DocName: "port_rcv_errors",
			Help:    "Total number of packets containing an error that were received on the port.",
		},
		"port_rcv_constraint_errors": {
			DocName: "port_rcv_constraint_errors",
			Help:    "Total number of packets received on the switch physical port that are discarded.",
		},
		"local_link_integrity_errors": {
			DocName: "local_link_integrity_errors",
			Help:    "Number of times that the count of local physical errors exceeded the threshold specified by LocalPhyErrors.",
		},
		"port_xmit_wait": {
			DocName: "port_xmit_wait",
			Help:    "Number of ticks during which the port had data to transmit but no data was sent during the entire tick.",
		},
		"port_xmit_discards": {
			DocName: "port_xmit_discards",
			Help:    "Total number of outbound packets discarded by the port because the port is down or congested.",
		},
		"port_xmit_constraint_errors": {
			DocName: "port_xmit_constraint_errors",
			Help:    "Total number of packets not transmitted from the switch physical port.",
		},
		"port_rcv_remote_physical_errors": {
			DocName: "port_rcv_remote_physical_errors",
			Help:    "Total number of packets marked with the EBP delimiter received on the port.",
		},
		"symbol_error": {
			DocName: "symbol_error",
			Help:    "Total number of minor link errors detected on one or more physical lanes.",
		},
		"VL15_dropped": {
			DocName: "VL15_dropped",
			Help:    "Number of incoming VL15 packets dropped due to resource limitations.",
		},
		"link_error_recovery": {
			DocName: "link_error_recovery",
			Help:    "Total number of times the Port Training state machine successfully completed the link error recovery process.",
		},
		"link_downed": {
			DocName: "link_downed",
			Help:    "Total number of times the Port Training state machine failed the link error recovery process and downed the link.",
		},
		"duplicate_request": {
			DocName: "duplicate_request",
			Help:    "Number of received packets. A duplicate request is a request that had been previously executed.",
		},
		"implied_nak_seq_err": {
			DocName: "implied_nak_seq_err",
			Help:    "Number of times the requester decided an ACK with a PSN larger than the expected PSN for an RDMA read or response.",
		},
		"lifespan": {
			DocName: "lifespan",
			Help:    "Maximum period in milliseconds between hardware counter updates. Two consecutive reads within this period might return the same values. This is a sysfs configuration knob (kernel default 10, writable range 0-10000); the exporter does not write it.",
		},
		"local_ack_timeout_err": {
			DocName: "local_ack_timeout_err",
			Help:    "The number of times QP's ack timer expired for RC, XRC, DCT QPs at the sender side. The QP retry limit was not exceeded, therefore it is still a recoverable error.",
		},
		"np_cnp_sent": {
			DocName: "np_cnp_sent",
			Help:    "The number of CNP packets sent by the Notification Point when it noticed congestion experienced in the RoCEv2 IP header (ECN bits). The counter was added in MLNX_OFED 4.1.",
		},
		"np_ecn_marked_roce_packets": {
			DocName: "np_ecn_marked_roce_packets",
			Help:    "The number of RoCEv2 packets received by the notification point which were marked for experiencing congestion (ECN bits were ‘11’ on the ingress RoCE traffic). The counter was added in MLNX_OFED 4.1.",
		},
		"out_of_buffer": {
			DocName: "out_of_buffer",
			Help:    "The number of drops that occurred due to lack of WQE for the associated QPs.",
		},
		"out_of_sequence": {
			DocName: "out_of_sequence",
			Help:    "The number of out-of-sequence packets received.",
		},
		"packet_seq_err": {
			DocName: "packet_seq_err",
			Help:    "The number of received NAK sequence error packets. The QP retry limit was not exceeded.",
		},
		"req_cqe_error": {
			DocName: "req_cqe_error",
			Help:    "The number of times requester detected CQEs completed with errors. Added in MLNX_OFED 4.1.",
		},
		"req_cqe_flush_error": {
			DocName: "req_cqe_flush_error",
			Help:    "The number of times requester detected CQEs completed with flushed errors. Added in MLNX_OFED 4.1.",
		},
		"req_remote_access_errors": {
			DocName: "req_remote_access_errors",
			Help:    "The number of times requester detected remote access errors. Added in MLNX_OFED 4.1.",
		},
		"req_remote_invalid_request": {
			DocName: "req_remote_invalid_request",
			Help:    "The number of times requester detected remote invalid request errors. Added in MLNX_OFED 4.1.",
		},
		"resp_cqe_error": {
			DocName: "resp_cqe_error",
			Help:    "The number of times responder detected CQEs completed with errors. Added in MLNX_OFED 4.1.",
		},
		"resp_cqe_flush_error": {
			DocName: "resp_cqe_flush_error",
			Help:    "The number of times responder detected CQEs completed with flushed errors. Added in MLNX_OFED 4.1.",
		},
		"resp_local_length_error": {
			DocName: "resp_local_length_error",
			Help:    "The number of times responder detected local length errors. Added in MLNX_OFED 4.1.",
		},
		"resp_remote_access_errors": {
			DocName: "resp_remote_access_errors",
			Help:    "The number of times responder detected remote access errors. Added in MLNX_OFED 4.1.",
		},
		"rnr_nak_retry_err": {
			DocName: "rnr_nak_retry_err",
			Help:    "The number of received RNR NAK packets. The QP retry limit was not exceeded.",
		},
		"roce_adp_retrans": {
			DocName: "roce_adp_retrans",
			Help:    "Counts the number of adaptive retransmissions for RoCE traffic. Added in MLNX_OFED rev 5.0-1.0.0.0 and kernel v5.6.0.",
		},
		"roce_adp_retrans_to": {
			DocName: "roce_adp_retrans_to",
			Help:    "Counts the number of times RoCE traffic reached timeout due to adaptive retransmission. Added in MLNX_OFED rev 5.0-1.0.0.0 and kernel v5.6.0.",
		},
		"roce_slow_restart": {
			DocName: "roce_slow_restart",
			Help:    "Counts the number of times RoCE slow restart was used. Added in MLNX_OFED rev 5.0-1.0.0.0 and kernel v5.6.0.",
		},
		"roce_slow_restart_cnps": {
			DocName: "roce_slow_restart_cnps",
			Help:    "Counts the number of times RoCE slow restart generated CNP packets. Added in MLNX_OFED rev 5.0-1.0.0.0 and kernel v5.6.0.",
		},
		"roce_slow_restart_trans": {
			DocName: "roce_slow_restart_trans",
			Help:    "Counts the number of times RoCE slow restart changed state to slow restart. Added in MLNX_OFED rev 5.0-1.0.0.0 and kernel v5.6.0.",
		},
		"rp_cnp_handled": {
			DocName: "rp_cnp_handled",
			Help:    "The number of CNP packets handled by the Reaction Point HCA to throttle the transmission rate. Added in MLNX_OFED 4.1.",
		},
		"rp_cnp_ignored": {
			DocName: "rp_cnp_ignored",
			Help:    "The number of CNP packets received and ignored by the Reaction Point HCA. This counter should not raise if RoCE Congestion Control was enabled in the network. If this counter rises, verify that ECN was enabled on the adapter. Added in MLNX_OFED 4.1.",
		},
		"cc_rx_ce_pkts": {
			DocName: "cc_rx_ce_pkts",
			Help:    "The number of received RoCEv2 packets marked Congestion Experienced (ECN CE). Optional mlx5 counter read via RDMA netlink; not present in sysfs hw_counters.",
		},
		"cc_rx_cnp_pkts": {
			DocName: "cc_rx_cnp_pkts",
			Help:    "The number of congestion notification packets (CNP) received. Optional mlx5 counter read via RDMA netlink; not present in sysfs hw_counters.",
		},
		"cc_tx_cnp_pkts": {
			DocName: "cc_tx_cnp_pkts",
			Help:    "The number of congestion notification packets (CNP) transmitted. Optional mlx5 counter read via RDMA netlink; not present in sysfs hw_counters.",
		},
		"rx_atomic_requests": {
			DocName: "rx_atomic_requests",
			Help:    "The number of received ATOMIC requests for the associated QPs.",
		},
		"rx_dct_connect": {
			DocName: "rx_dct_connect",
			Help:    "The number of received connection requests for the associated DCTs.",
		},
		"rx_icrc_encapsulated": {
			DocName: "rx_icrc_encapsulated",
			Help:    "The number of RoCE packets with ICRC errors. This counter was added in MLNX_OFED 4.4 and kernel 4.19.",
		},
		"rx_read_requests": {
			DocName: "rx_read_requests",
			Help:    "The number of received READ requests for the associated QPs.",
		},
		"rx_write_requests": {
			DocName: "rx_write_requests",
			Help:    "The number of received WRITE requests for the associated QPs.",
		},
	}

	metricHelpByDocName = buildMetricHelpByDocName()

	// Values are emitted only for these optional counters. Other optional names
	// still appear on rdma_optional_counter_enabled so operators can see them.
	optionalCounterValueNames = map[string]struct{}{
		"cc_rx_ce_pkts":  {},
		"cc_rx_cnp_pkts": {},
		"cc_tx_cnp_pkts": {},
	}

	qpCounterValueNames = []string{
		"duplicate_request",
		"implied_nak_seq_err",
		"local_ack_timeout_err",
		"packet_seq_err",
		"rnr_nak_retry_err",
		"out_of_buffer",
		"rx_write_requests",
		"rx_read_requests",
		"rx_atomic_requests",
		"rdma_rx_bytes",
		"rdma_tx_bytes",
		"rdma_rx_packets",
		"rdma_tx_packets",
	}

	qpCounterHelp = "Live auto-type bound user QP aggregate on this port. Port sysfs rdma_<name>_total already includes default + running sets + history; do not add these series to it. Not per-LQPN. A successful dump can still contain retained values if the hardware query failed."
)

func qpCounterMetricName(dumpName string) string {
	return "rdma_qp_" + strings.TrimPrefix(dumpName, "rdma_") + "_total"
}

type rocePFCMetricKind int

const (
	rocePFCMetricKindFrames rocePFCMetricKind = iota
	rocePFCMetricKindDuration
	rocePFCMetricKindTransitions
)

type netDevStatsCacheEntry struct {
	stats map[string]uint64
	err   error
}

func buildMetricHelpByDocName() map[string]string {
	help := make(map[string]string, len(metricSpecs))
	for _, spec := range metricSpecs {
		if spec.DocName == "" || spec.Help == "" {
			continue
		}
		help[spec.DocName] = spec.Help
	}
	return help
}

func (c *RdmaCollector) hwMetricDesc(stat string) *prometheus.Desc {
	docName := canonicalDocName(stat)
	return c.metricDesc(stat, docName, "RDMA port hardware counter sourced from sysfs hw_counters.", c.portHwMetrics, c.portHwStatLookup)
}

func (c *RdmaCollector) optionalMetricDesc(stat string) *prometheus.Desc {
	docName := canonicalDocName(stat)
	return c.metricDesc(stat, docName, "RDMA optional hardware counter sourced from RDMA netlink.", c.portOptionalMetrics, c.portOptionalStatLookup)
}

func (c *RdmaCollector) statMetricDesc(stat string) *prometheus.Desc {
	docName := canonicalDocName(stat)
	return c.metricDesc(stat, docName, "RDMA port counter sourced from sysfs counters.", c.portStatMetrics, c.portStatLookup)
}

func (c *RdmaCollector) metricDesc(stat, docName, fallback string, entries map[string]metricEntry, lookup map[string]string) *prometheus.Desc {
	if metricName, ok := lookup[stat]; ok {
		if entry, exists := entries[metricName]; exists {
			return entry.desc
		}
	}

	metricName := buildMetricName(docName, entries)
	help := metricDocHelp(docName, fallback)
	desc := prometheus.NewDesc(
		metricName,
		help,
		[]string{"device", "port"},
		nil,
	)

	entries[metricName] = metricEntry{
		desc:    desc,
		docName: docName,
	}
	lookup[stat] = metricName

	return desc
}

func buildMetricName(docName string, existing map[string]metricEntry) string {
	base := sanitizeStatName(docName)
	metricName := fmt.Sprintf("rdma_%s_total", base)

	if entry, ok := existing[metricName]; ok && entry.docName != docName {
		h := fnv.New32a()
		_, _ = h.Write([]byte(docName))
		metricName = fmt.Sprintf("rdma_%s_%x_total", base, h.Sum32())
	}

	return metricName
}

func metricDocHelp(docName, fallback string) string {
	if help, ok := metricHelpByDocName[docName]; ok {
		return help
	}
	return fallback
}

func sanitizeStatName(stat string) string {
	if stat == "" {
		return "unknown"
	}

	var b strings.Builder
	b.Grow(len(stat))
	for i, r := range stat {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(unicode.ToLower(r))
		case r >= '0' && r <= '9':
			if i == 0 {
				b.WriteRune('_')
			}
			b.WriteRune(r)
		case r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}

	res := b.String()
	if res == "" {
		res = "unknown"
	}
	if res[0] >= '0' && res[0] <= '9' {
		res = "_" + res
	}

	return res
}

func canonicalDocName(stat string) string {
	if spec, ok := metricSpecs[stat]; ok && spec.DocName != "" {
		return spec.DocName
	}
	sanitized := sanitizeStatName(stat)
	if spec, ok := metricSpecs[sanitized]; ok && spec.DocName != "" {
		return spec.DocName
	}
	if sanitized == "" {
		return "unknown"
	}
	return sanitized
}

// New creates a new RDMA collector with the provided provider and logger.
func New(provider Provider, logger *slog.Logger, opts ...Option) *RdmaCollector {
	if logger == nil {
		logger = slog.Default()
	}

	c := &RdmaCollector{
		provider: provider,
		logger:   logger,
		portInfoDesc: prometheus.NewDesc(
			"rdma_port_info",
			"RDMA port metadata exported as labels.",
			[]string{
				"device", "port",
				"link_layer", "state", "phys_state", "link_width", "link_speed",
				// SR-IOV VF/PF identification labels.
				// pci_addr matches the pciAddr label in sriov_kubepoddevice, enabling join queries.
				"pci_addr",
				// is_vf is "true" for Virtual Functions, "false" for Physical Functions.
				"is_vf",
				// pf_device is the IB device name of the parent PF (e.g. "mlx5_0").
				// Empty for PF devices.
				"pf_device",
			},
			nil,
		),
		lifespanMillisecondsDesc: prometheus.NewDesc(
			"rdma_lifespan_milliseconds",
			metricDocHelp("lifespan", "Maximum period in milliseconds between hardware counter updates."),
			[]string{"device", "port"},
			nil,
		),
		rocePFCPauseFramesDesc: prometheus.NewDesc(
			"rdma_roce_pfc_pause_frames_total",
			"RoCEv2 PFC pause frames from ethtool stats. direction=rx: the peer XOFFed this NIC so this NIC cannot transmit on that priority. direction=tx: this NIC XOFFed the peer because it is not absorbing that priority.",
			[]string{"device", "port", "netdev", "direction", "priority"},
			nil,
		),
		rocePFCPauseDurationDesc: prometheus.NewDesc(
			"rdma_roce_pfc_pause_duration_total",
			"Cumulative RoCEv2 PFC pause duration in microseconds from ethtool stats. Pause occupancy is rate()/1e6. Direction has the same meaning as rdma_roce_pfc_pause_frames_total.",
			[]string{"device", "port", "netdev", "direction", "priority"},
			nil,
		),
		rocePFCPauseTransitionsDesc: prometheus.NewDesc(
			"rdma_roce_pfc_pause_transitions_total",
			"RoCEv2 PFC XOFF-to-XON transitions from ethtool stats. mlx5 exposes this for receive (direction=rx) only. Direction has the same meaning as rdma_roce_pfc_pause_frames_total.",
			[]string{"device", "port", "netdev", "direction", "priority"},
			nil,
		),
		netdevPrioBufDiscardDesc: prometheus.NewDesc(
			"rdma_netdev_prio_buf_discard_total",
			"Packets discarded due to lack of per-host receive buffers. Ethtool rx_prio[p]_buf_discard.",
			[]string{"device", "port", "netdev", "priority"},
			nil,
		),
		netdevPrioCongDiscardDesc: prometheus.NewDesc(
			"rdma_netdev_prio_cong_discard_total",
			"Packets discarded due to per-host congestion. Ethtool rx_prio[p]_cong_discard.",
			[]string{"device", "port", "netdev", "priority"},
			nil,
		),
		netdevPrioDiscardsDesc: prometheus.NewDesc(
			"rdma_netdev_prio_discards_total",
			"Packets discarded due to lack of receive buffers. Ethtool rx_prio[p]_discards.",
			[]string{"device", "port", "netdev", "priority"},
			nil,
		),
		netdevPrioECNMarkedDesc: prometheus.NewDesc(
			"rdma_netdev_prio_ecn_marked_total",
			"Packets ECN-marked due to per-host congestion. Ethtool rx_prio[p]_marked.",
			[]string{"device", "port", "netdev", "priority"},
			nil,
		),
		netdevDevOutOfBufferDesc: prometheus.NewDesc(
			"rdma_netdev_dev_out_of_buffer_total",
			"Number of times a device-owned queue lacked receive buffers. Ethtool dev_out_of_buffer; distinct from the sysfs QP WQE counter out_of_buffer.",
			[]string{"device", "port", "netdev"},
			nil,
		),
		netdevRxOutOfBufferDesc: prometheus.NewDesc(
			"rdma_netdev_rx_out_of_buffer_total",
			"Times the receive queue had no software buffers for incoming traffic. Ethtool rx_out_of_buffer; distinct from the sysfs QP WQE counter out_of_buffer.",
			[]string{"device", "port", "netdev"},
			nil,
		),
		netdevRxDiscardsPhyDesc: prometheus.NewDesc(
			"rdma_netdev_rx_discards_phy_total",
			"Packets dropped on the physical port due to lack of buffers. Ethtool rx_discards_phy.",
			[]string{"device", "port", "netdev"},
			nil,
		),
		pcieOutboundStalledPercentDesc: prometheus.NewDesc(
			"rdma_pcie_outbound_stalled_percent",
			"Percentage of the last 1 second that outbound PCI was stalled (kernel 0-100). Sampled at scrape time; stalls shorter than the scrape interval can be missed. Use rdma_pcie_outbound_stalled_seconds_total for alerting.",
			[]string{"device", "port", "netdev", "op"},
			nil,
		),
		pcieOutboundStalledSecondsDesc: prometheus.NewDesc(
			"rdma_pcie_outbound_stalled_seconds_total",
			"Cumulative seconds during which outbound PCI stall exceeded 30 percent. Primary stall signal; rate() is the fraction of time above the threshold.",
			[]string{"device", "port", "netdev", "op"},
			nil,
		),
		pcieOutboundOverflowDesc: prometheus.NewDesc(
			"rdma_pcie_outbound_buffer_overflow_total",
			"Packets dropped due to outbound PCI buffer overflow. Ethtool outbound_pci_buffer_overflow.",
			[]string{"device", "port", "netdev"},
			nil,
		),
		pcieSignalIntegrityDesc: prometheus.NewDesc(
			"rdma_pcie_signal_integrity_total",
			"PCIe physical-layer signal integrity errors. Ethtool {rx,tx}_pci_signal_integrity.",
			[]string{"device", "port", "netdev", "direction"},
			nil,
		),
		phyRxCorrectedBitsDesc: prometheus.NewDesc(
			"rdma_phy_rx_corrected_bits_total",
			"FEC-corrected bits on the physical port. Ethtool rx_corrected_bits_phy.",
			[]string{"device", "port", "netdev"},
			nil,
		),
		phyRxPCSSymbolErrDesc: prometheus.NewDesc(
			"rdma_phy_rx_pcs_symbol_err_total",
			"Uncorrected or FEC-inactive symbol errors on the physical port. Ethtool rx_pcs_symbol_err_phy.",
			[]string{"device", "port", "netdev"},
			nil,
		),
		phyRxBitsDesc: prometheus.NewDesc(
			"rdma_phy_rx_bits_total",
			"Bits that could have been received on the physical port. Denominator for interval FEC/BER ratios. Ethtool rx_bits_phy.",
			[]string{"device", "port", "netdev"},
			nil,
		),
		phyRxErrLaneDesc: prometheus.NewDesc(
			"rdma_phy_rx_err_lane_total",
			"Physical raw errors per lane before FEC. Ethtool rx_err_lane_[l]_phy.",
			[]string{"device", "port", "netdev", "lane"},
			nil,
		),
		phyRxCRCErrorsDesc: prometheus.NewDesc(
			"rdma_phy_rx_crc_errors_total",
			"Packets dropped due to FCS errors on the physical port. Ethtool rx_crc_errors_phy.",
			[]string{"device", "port", "netdev"},
			nil,
		),
		phyLinkDownEventsDesc: prometheus.NewDesc(
			"rdma_phy_link_down_events_total",
			"Times the physical link operative state changed to down. Ethtool link_down_events_phy.",
			[]string{"device", "port", "netdev"},
			nil,
		),
		netdevGlobalPauseFramesDesc: prometheus.NewDesc(
			"rdma_netdev_global_pause_frames_total",
			"IEEE 802.3x pause frames on the physical port from ethtool. direction=rx: pause frames received (this NIC was asked to stop transmitting). direction=tx: pause frames transmitted (this NIC asked the peer to stop transmitting). Present only when global pause mode is enabled, not PFC. Observation only.",
			[]string{"device", "port", "netdev", "direction"},
			nil,
		),
		netdevGlobalPauseDurationDesc: prometheus.NewDesc(
			"rdma_netdev_global_pause_duration_total",
			"Cumulative IEEE 802.3x pause duration in microseconds from ethtool. Occupancy is rate()/1e6. Direction has the same meaning as rdma_netdev_global_pause_frames_total. Present only when global pause mode is enabled.",
			[]string{"device", "port", "netdev", "direction"},
			nil,
		),
		netdevGlobalPauseTransitionsDesc: prometheus.NewDesc(
			"rdma_netdev_global_pause_transitions_total",
			"IEEE 802.3x XOFF-to-XON transitions on the physical port from ethtool rx_global_pause_transition. mlx5 exposes receive only. Present only when global pause mode is enabled.",
			[]string{"device", "port", "netdev"},
			nil,
		),
		netdevPauseStormEventsDesc: prometheus.NewDesc(
			"rdma_netdev_pause_storm_events_total",
			"Times the device sent pause frames for a long period. severity=warning: stalled past a watermark. severity=error: timed out and pause transmission was disabled; drops may have occurred while pause TX was off. Ethtool tx_pause_storm_{warning,error}_events.",
			[]string{"device", "port", "netdev", "severity"},
			nil,
		),
		netdevVPortRDMABytesDesc: prometheus.NewDesc(
			"rdma_netdev_vport_rdma_bytes_total",
			"RDMA octets steered to or from this netdev's function vport. Ethtool {rx,tx}_vport_rdma_{unicast,multicast}_bytes. Not a physical-port *_phy total and not a sum of other function vports. Distinct from sysfs port_rcv_data and rdma_qp_{rx,tx}_bytes_total.",
			[]string{"device", "port", "netdev", "direction", "traffic"},
			nil,
		),
		netdevVPortRDMAPacketsDesc: prometheus.NewDesc(
			"rdma_netdev_vport_rdma_packets_total",
			"RDMA packets steered to or from this netdev's function vport. Ethtool {rx,tx}_vport_rdma_{unicast,multicast}_packets. Not a physical-port *_phy total and not a sum of other function vports. Distinct from sysfs port packet counters and rdma_qp_{rx,tx}_packets_total.",
			[]string{"device", "port", "netdev", "direction", "traffic"},
			nil,
		),
		optionalCounterEnabledDesc: prometheus.NewDesc(
			"rdma_optional_counter_enabled",
			"Whether an optional RDMA hardware counter is enabled on the port. 1 means currently enabled; 0 means supported but disabled. The exporter never enables counters.",
			[]string{"device", "port", "counter"},
			nil,
		),
		qpCounterModeDesc: prometheus.NewDesc(
			"rdma_qp_counter_mode",
			"QP statistic counter bind mode on the port. One series per mode; value 1 is the current mode.",
			[]string{"device", "port", "mode"},
			nil,
		),
		qpAutoMaskDesc: prometheus.NewDesc(
			"rdma_qp_auto_mask",
			"Whether auto-mode grouping includes this criterion. type and pid are independent bits.",
			[]string{"device", "port", "criteria"},
			nil,
		),
		qpScrapeStatusDesc: prometheus.NewDesc(
			"rdma_qp_scrape_status",
			"Result of the last QP counter dump for this port. overflow means the receive budget was exceeded and totals were omitted.",
			[]string{"device", "port", "result"},
			nil,
		),
		qpValueDescs: make(map[string]*prometheus.Desc, len(qpCounterValueNames)),
		scrapeErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "rdma_scrape_errors_total",
			Help: "Total number of errors encountered while scraping RDMA sysfs.",
		}),
		rocePFCScrapeErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "rdma_roce_pfc_scrape_errors_total",
			Help: "Total number of errors encountered while scraping RoCEv2 PFC ethtool stats.",
		}),
		netDevHWScrapeErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "rdma_netdev_scrape_errors_total",
			Help: "Total number of errors encountered while scraping netdev ethtool hardware stats.",
		}),
		optionalCounterScrapeErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "rdma_optional_counter_scrape_errors_total",
			Help: "Total number of errors encountered while scraping optional RDMA counters via netlink.",
		}),
		qpScrapeErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "rdma_qp_scrape_errors_total",
			Help: "Total number of errors encountered while scraping QP counters via netlink, including dump overflow.",
		}),
		portStatMetrics:        make(map[string]metricEntry),
		portStatLookup:         make(map[string]string),
		portHwMetrics:          make(map[string]metricEntry),
		portHwStatLookup:       make(map[string]string),
		portOptionalMetrics:    make(map[string]metricEntry),
		portOptionalStatLookup: make(map[string]string),
	}

	for _, name := range qpCounterValueNames {
		c.qpValueDescs[name] = prometheus.NewDesc(
			qpCounterMetricName(name),
			qpCounterHelp,
			[]string{"device", "port", "qp_type"},
			nil,
		)
	}

	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}

	c.storeContext(context.Background())

	return c
}

func (c *RdmaCollector) storeContext(ctx context.Context) {
	c.ctxValue.Store(&ctx)
}

// WithNetDevStatsProvider configures a provider used to fetch netdev statistics
// for RoCEv2 PFC-related metrics. PFC collection is enabled by default when a
// provider is configured; use WithRoCEPFCMetrics(false) to disable it.
func WithNetDevStatsProvider(provider NetDevStatsProvider) Option {
	return func(c *RdmaCollector) {
		c.netDevStatsProvider = provider
		c.collectRoCEPFC = true
	}
}

// WithRoCEPFCMetrics enables or disables RoCEv2 PFC metric emission.
func WithRoCEPFCMetrics(enabled bool) Option {
	return func(c *RdmaCollector) {
		c.collectRoCEPFC = enabled
	}
}

// WithNetDevHWMetrics enables ethtool hardware counters (buffer, PCIe, PHY/FEC, IEEE 802.3x global pause, pause storm, vport RDMA).
func WithNetDevHWMetrics(enabled bool) Option {
	return func(c *RdmaCollector) {
		c.collectNetDevHW = enabled
	}
}

// WithNetDevPhysPortName overrides how the collector reads sysfs phys_port_name for a netdev.
func WithNetDevPhysPortName(fn func(string) string) Option {
	return func(c *RdmaCollector) {
		c.physPortName = fn
	}
}

// WithOptionalCounterProvider enables optional RDMA hardware counters via netlink.
func WithOptionalCounterProvider(provider OptionalCounterProvider) Option {
	return func(c *RdmaCollector) {
		c.optionalCounterProvider = provider
	}
}

// WithQPCounterProvider enables live bound QP counter sets via netlink GET/DUMP.
func WithQPCounterProvider(provider QPCounterProvider) Option {
	return func(c *RdmaCollector) {
		c.qpCounterProvider = provider
	}
}

// SetContext updates the context used by the next Collect invocation.
func (c *RdmaCollector) SetContext(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	c.storeContext(ctx)
}

// ResetContext resets the collector back to the background context.
func (c *RdmaCollector) ResetContext() {
	c.storeContext(context.Background())
}

// Describe implements prometheus.Collector.
func (c *RdmaCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.portInfoDesc
	ch <- c.lifespanMillisecondsDesc
	ch <- c.rocePFCPauseFramesDesc
	ch <- c.rocePFCPauseDurationDesc
	ch <- c.rocePFCPauseTransitionsDesc
	ch <- c.netdevPrioBufDiscardDesc
	ch <- c.netdevPrioCongDiscardDesc
	ch <- c.netdevPrioDiscardsDesc
	ch <- c.netdevPrioECNMarkedDesc
	ch <- c.netdevDevOutOfBufferDesc
	ch <- c.netdevRxOutOfBufferDesc
	ch <- c.netdevRxDiscardsPhyDesc
	ch <- c.pcieOutboundStalledPercentDesc
	ch <- c.pcieOutboundStalledSecondsDesc
	ch <- c.pcieOutboundOverflowDesc
	ch <- c.pcieSignalIntegrityDesc
	ch <- c.phyRxCorrectedBitsDesc
	ch <- c.phyRxPCSSymbolErrDesc
	ch <- c.phyRxBitsDesc
	ch <- c.phyRxErrLaneDesc
	ch <- c.phyRxCRCErrorsDesc
	ch <- c.phyLinkDownEventsDesc
	ch <- c.netdevGlobalPauseFramesDesc
	ch <- c.netdevGlobalPauseDurationDesc
	ch <- c.netdevGlobalPauseTransitionsDesc
	ch <- c.netdevPauseStormEventsDesc
	ch <- c.netdevVPortRDMABytesDesc
	ch <- c.netdevVPortRDMAPacketsDesc
	c.scrapeErrors.Describe(ch)
	c.rocePFCScrapeErrors.Describe(ch)
	c.netDevHWScrapeErrors.Describe(ch)
	if c.optionalCounterProvider != nil {
		ch <- c.optionalCounterEnabledDesc
		c.optionalCounterScrapeErrors.Describe(ch)
	}
	if c.qpCounterProvider != nil {
		ch <- c.qpCounterModeDesc
		ch <- c.qpAutoMaskDesc
		ch <- c.qpScrapeStatusDesc
		c.qpScrapeErrors.Describe(ch)
		for _, name := range qpCounterValueNames {
			ch <- c.qpValueDescs[name]
		}
	}

	c.collectMu.Lock()
	statDescs := make([]*prometheus.Desc, 0, len(c.portStatMetrics))
	for _, entry := range c.portStatMetrics {
		statDescs = append(statDescs, entry.desc)
	}
	hwDescs := make([]*prometheus.Desc, 0, len(c.portHwMetrics))
	for _, entry := range c.portHwMetrics {
		hwDescs = append(hwDescs, entry.desc)
	}
	optionalDescs := make([]*prometheus.Desc, 0, len(c.portOptionalMetrics))
	for _, entry := range c.portOptionalMetrics {
		optionalDescs = append(optionalDescs, entry.desc)
	}
	c.collectMu.Unlock()

	for _, desc := range statDescs {
		ch <- desc
	}
	for _, desc := range hwDescs {
		ch <- desc
	}
	for _, desc := range optionalDescs {
		ch <- desc
	}
}

// Collect implements prometheus.Collector.
func (c *RdmaCollector) Collect(ch chan<- prometheus.Metric) {
	c.collectMu.Lock()
	defer c.collectMu.Unlock()

	ctx := context.Background()
	if stored := c.ctxValue.Load(); stored != nil {
		ctx = *stored
	}

	devices, err := c.provider.Devices(ctx)
	if err != nil {
		if ctx.Err() != nil {
			c.logger.Warn("rdma scrape aborted by context", "err", ctx.Err())
		} else {
			c.logger.Warn("rdma scrape failed", "err", err)
		}
		c.scrapeErrors.Inc()
		c.scrapeErrors.Collect(ch)
		return
	}

	netDevStatsCache := make(map[string]netDevStatsCacheEntry)
	netDevHWEmitted := make(map[string]struct{})
	netdevOwners := ethernetNetDevOwners(devices)
	optionalReady := false
	if c.optionalCounterProvider != nil {
		if err := c.optionalCounterProvider.Prepare(ctx); err != nil {
			if ctx.Err() != nil {
				c.logger.Warn("optional rdma counter scrape aborted by context", "err", ctx.Err())
			} else {
				c.logger.Warn("optional rdma counter dump failed", "err", err)
			}
			c.optionalCounterScrapeErrors.Inc()
		} else {
			optionalReady = true
		}
	}

	for _, device := range devices {
		deviceStart := time.Now()
		portIDStrings := make([]string, len(device.Ports))
		for i, port := range device.Ports {
			portID := strconv.Itoa(port.ID)
			portIDStrings[i] = portID

			if len(port.Stats) > 0 {
				names := sortedKeys(port.Stats)
				for _, name := range names {
					value := float64(port.Stats[name])
					desc := c.statMetricDesc(name)
					ch <- prometheus.MustNewConstMetric(
						desc,
						prometheus.CounterValue,
						value,
						device.Name,
						portID,
					)
				}
			}

			if len(port.HwStats) > 0 {
				names := sortedKeys(port.HwStats)
				for _, name := range names {
					value := float64(port.HwStats[name])
					if name == "lifespan" {
						ch <- prometheus.MustNewConstMetric(
							c.lifespanMillisecondsDesc,
							prometheus.GaugeValue,
							value,
							device.Name,
							portID,
						)
						continue
					}
					desc := c.hwMetricDesc(name)
					ch <- prometheus.MustNewConstMetric(
						desc,
						prometheus.CounterValue,
						value,
						device.Name,
						portID,
					)
				}
			}

			if optionalReady {
				c.collectOptionalCounters(ctx, ch, device.Name, port.ID, portID, port.HwStats)
			}

			attr := port.Attributes
			c.collectNetDevMetrics(ctx, ch, device.Name, portID, attr, device.IsVF, device.PCIAddr, netdevOwners, netDevStatsCache, netDevHWEmitted)

			ch <- prometheus.MustNewConstMetric(
				c.portInfoDesc,
				prometheus.GaugeValue,
				1,
				device.Name,
				portID,
				attr.LinkLayer,
				attr.State,
				attr.PhysState,
				attr.LinkWidth,
				attr.LinkSpeed,
				device.PCIAddr,
				strconv.FormatBool(device.IsVF),
				device.PFDevice,
			)
		}
		c.logger.Debug("rdma device scraped",
			"device", device.Name,
			"ports", portIDStrings,
			"duration", time.Since(deviceStart))
	}

	if c.qpCounterProvider != nil {
		c.collectQPCounters(ctx, ch, devices)
	}

	c.scrapeErrors.Collect(ch)
	c.rocePFCScrapeErrors.Collect(ch)
	c.netDevHWScrapeErrors.Collect(ch)
	if c.optionalCounterProvider != nil {
		c.optionalCounterScrapeErrors.Collect(ch)
	}
	if c.qpCounterProvider != nil {
		c.qpScrapeErrors.Collect(ch)
	}
}

func sortedKeys[V any](m map[string]V) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func (c *RdmaCollector) collectOptionalCounters(
	ctx context.Context,
	ch chan<- prometheus.Metric,
	deviceName string,
	portID int,
	portLabel string,
	hwStats map[string]uint64,
) {
	counters, err := c.optionalCounterProvider.Counters(ctx, deviceName, portID)
	if err != nil {
		if ctx.Err() != nil {
			c.logger.Warn("optional rdma counter scrape aborted by context", "device", deviceName, "port", portLabel, "err", ctx.Err())
			return
		}
		c.logger.Warn("optional rdma counter scrape failed", "device", deviceName, "port", portLabel, "err", err)
		c.optionalCounterScrapeErrors.Inc()
		return
	}

	for _, counter := range counters {
		if counter.Name == "" {
			continue
		}
		enabled := 0.0
		if counter.Enabled {
			enabled = 1
		}
		ch <- prometheus.MustNewConstMetric(
			c.optionalCounterEnabledDesc,
			prometheus.GaugeValue,
			enabled,
			deviceName,
			portLabel,
			counter.Name,
		)
		if counter.Enabled && !counter.HasValue {
			c.optionalCounterScrapeErrors.Inc()
			continue
		}
		if !counter.Enabled || !counter.HasValue {
			continue
		}
		if _, ok := optionalCounterValueNames[counter.Name]; !ok {
			continue
		}
		if _, exists := hwStats[counter.Name]; exists {
			continue
		}
		ch <- prometheus.MustNewConstMetric(
			c.optionalMetricDesc(counter.Name),
			prometheus.CounterValue,
			float64(counter.Value),
			deviceName,
			portLabel,
		)
	}
}

func (c *RdmaCollector) collectQPCounters(ctx context.Context, ch chan<- prometheus.Metric, devices []rdma.Device) {
	if err := c.qpCounterProvider.Prepare(ctx); err != nil {
		if ctx.Err() != nil {
			c.logger.Warn("qp counter scrape aborted by context", "err", ctx.Err())
		} else {
			c.logger.Warn("qp counter dump failed", "err", err)
		}
		c.qpScrapeErrors.Inc()
		return
	}
	for _, device := range devices {
		for _, port := range device.Ports {
			c.collectQPPort(ctx, ch, device.Name, port.ID)
		}
	}
}

func (c *RdmaCollector) collectQPPort(ctx context.Context, ch chan<- prometheus.Metric, deviceName string, portID int) {
	portLabel := strconv.Itoa(portID)
	mode, err := c.qpCounterProvider.QPMode(ctx, deviceName, portID)
	if err != nil {
		if ctx.Err() != nil {
			c.logger.Warn("qp counter scrape aborted by context", "device", deviceName, "port", portLabel, "err", ctx.Err())
			return
		}
		c.logger.Warn("qp counter mode scrape failed", "device", deviceName, "port", portLabel, "err", err)
		if err != rdmanl.ErrQPUnsupported {
			c.qpScrapeErrors.Inc()
		}
		c.emitQPScrapeStatus(ch, deviceName, portLabel, "error")
		return
	}
	c.emitQPModeMetrics(ch, deviceName, portLabel, mode)

	sets, err := c.qpCounterProvider.QPSets(ctx, deviceName, portID)
	if err != nil {
		if ctx.Err() != nil {
			c.logger.Warn("qp counter scrape aborted by context", "device", deviceName, "port", portLabel, "err", ctx.Err())
			return
		}
		result := "error"
		if errors.Is(err, rdmanl.ErrDumpOverflow) {
			result = "overflow"
		}
		c.logger.Warn("qp counter scrape failed", "device", deviceName, "port", portLabel, "err", err)
		if err != rdmanl.ErrQPUnsupported {
			c.qpScrapeErrors.Inc()
		}
		c.emitQPScrapeStatus(ch, deviceName, portLabel, result)
		return
	}

	c.emitQPScrapeStatus(ch, deviceName, portLabel, "ok")
	c.emitQPTotals(ch, deviceName, portLabel, sets)
}

func (c *RdmaCollector) emitQPTotals(ch chan<- prometheus.Metric, deviceName, portLabel string, sets []rdmanl.QPSet) {
	totals := make(map[string]map[string]uint64)
	for _, set := range sets {
		if !set.AutoType() {
			continue
		}
		byName := totals[set.QPType]
		if byName == nil {
			byName = make(map[string]uint64)
			totals[set.QPType] = byName
		}
		for _, name := range qpCounterValueNames {
			value, ok := set.Stats[name]
			if !ok {
				continue
			}
			byName[name] += value
		}
	}
	for _, qpType := range sortedKeys(totals) {
		for _, name := range qpCounterValueNames {
			value, ok := totals[qpType][name]
			if !ok {
				continue
			}
			ch <- prometheus.MustNewConstMetric(
				c.qpValueDescs[name],
				prometheus.CounterValue,
				float64(value),
				deviceName,
				portLabel,
				qpType,
			)
		}
	}
}

func (c *RdmaCollector) emitQPModeMetrics(ch chan<- prometheus.Metric, deviceName, portLabel string, mode rdmanl.QPMode) {
	current := mode.Mode
	if current != "none" && current != "auto" && current != "manual" {
		current = "none"
	}
	for _, name := range []string{"none", "auto", "manual"} {
		value := 0.0
		if name == current {
			value = 1
		}
		ch <- prometheus.MustNewConstMetric(
			c.qpCounterModeDesc,
			prometheus.GaugeValue,
			value,
			deviceName,
			portLabel,
			name,
		)
	}
	for _, criteria := range []string{"type", "pid"} {
		value := 0.0
		if criteria == "type" && mode.MaskType {
			value = 1
		}
		if criteria == "pid" && mode.MaskPID {
			value = 1
		}
		ch <- prometheus.MustNewConstMetric(
			c.qpAutoMaskDesc,
			prometheus.GaugeValue,
			value,
			deviceName,
			portLabel,
			criteria,
		)
	}
}

func (c *RdmaCollector) emitQPScrapeStatus(ch chan<- prometheus.Metric, deviceName, portLabel, result string) {
	for _, name := range []string{"ok", "overflow", "error"} {
		value := 0.0
		if name == result {
			value = 1
		}
		ch <- prometheus.MustNewConstMetric(
			c.qpScrapeStatusDesc,
			prometheus.GaugeValue,
			value,
			deviceName,
			portLabel,
			name,
		)
	}
}

func (c *RdmaCollector) collectNetDevMetrics(
	ctx context.Context,
	ch chan<- prometheus.Metric,
	deviceName, portID string,
	attr rdma.PortAttributes,
	isVF bool,
	pciAddr string,
	netdevOwners map[string]int,
	cache map[string]netDevStatsCacheEntry,
	hwEmitted map[string]struct{},
) {
	if c.netDevStatsProvider == nil {
		return
	}
	if !c.collectRoCEPFC && !c.collectNetDevHW {
		return
	}
	// Ethtool collection is skipped for IB devices flagged as PCI VFs.
	// Remaining Ethernet ports are not guaranteed to be PFs (SF and guest VF
	// without physfn still pass). vPort RDMA has extra omit gates.
	if isVF {
		c.logger.Debug("skipping netdev collection for VF device", "device", deviceName, "port", portID)
		return
	}
	if attr.LinkLayer != "Ethernet" || attr.NetDev == "" {
		return
	}

	stats, err := c.readNetDevStatsWithCache(ctx, attr.NetDev, cache)
	if err != nil {
		if ctx.Err() != nil {
			c.logger.Warn("netdev scrape aborted by context", "device", deviceName, "port", portID, "netdev", attr.NetDev, "err", ctx.Err())
			return
		}
		c.logger.Warn("netdev scrape failed", "device", deviceName, "port", portID, "netdev", attr.NetDev, "err", err)
		return
	}

	if c.collectRoCEPFC {
		c.emitRoCEPFCMetrics(ch, deviceName, portID, attr.NetDev, stats)
	}
	if c.collectNetDevHW {
		if _, ok := hwEmitted[attr.NetDev]; ok {
			return
		}
		hwEmitted[attr.NetDev] = struct{}{}
		skipVPort := !isPCIFunctionBDF(pciAddr) || netdevOwners[attr.NetDev] != 1 || isRepresentorPhysPortName(c.lookupPhysPortName(attr.NetDev))
		if skipVPort {
			c.logger.Debug("omitting vport RDMA metrics",
				"device", deviceName,
				"port", portID,
				"netdev", attr.NetDev,
				"pci_addr", pciAddr,
			)
		}
		c.emitNetDevHWMetrics(ch, deviceName, portID, attr.NetDev, stats, skipVPort)
	}
}

func (c *RdmaCollector) emitRoCEPFCMetrics(
	ch chan<- prometheus.Metric,
	deviceName, portID, netDev string,
	stats map[string]uint64,
) {
	names := sortedKeys(stats)
	for _, name := range names {
		direction, priority, kind, ok := parseRoCEPFCMetricName(name)
		if !ok {
			continue
		}
		desc := c.rocePFCPauseFramesDesc
		switch kind {
		case rocePFCMetricKindDuration:
			desc = c.rocePFCPauseDurationDesc
		case rocePFCMetricKindTransitions:
			desc = c.rocePFCPauseTransitionsDesc
		}

		ch <- prometheus.MustNewConstMetric(
			desc,
			prometheus.CounterValue,
			float64(stats[name]),
			deviceName,
			portID,
			netDev,
			direction,
			priority,
		)
	}
}

func (c *RdmaCollector) readNetDevStatsWithCache(
	ctx context.Context,
	netDev string,
	cache map[string]netDevStatsCacheEntry,
) (map[string]uint64, error) {
	if entry, ok := cache[netDev]; ok {
		return entry.stats, entry.err
	}

	stats, err := c.netDevStatsProvider.Stats(ctx, netDev)
	if err != nil {
		if c.collectRoCEPFC {
			c.rocePFCScrapeErrors.Inc()
		}
		if c.collectNetDevHW {
			c.netDevHWScrapeErrors.Inc()
		}
	}
	cache[netDev] = netDevStatsCacheEntry{
		stats: stats,
		err:   err,
	}
	return stats, err
}

func parseRoCEPFCMetricName(name string) (direction, priority string, kind rocePFCMetricKind, ok bool) {
	matches := rocePFCStatPattern.FindStringSubmatch(name)
	if matches == nil {
		return "", "", rocePFCMetricKindFrames, false
	}

	direction = matches[1]
	priority = matches[2]
	switch matches[3] {
	case "":
		return direction, priority, rocePFCMetricKindFrames, true
	case "duration":
		return direction, priority, rocePFCMetricKindDuration, true
	case "transition":
		return direction, priority, rocePFCMetricKindTransitions, true
	default:
		return "", "", rocePFCMetricKindFrames, false
	}
}

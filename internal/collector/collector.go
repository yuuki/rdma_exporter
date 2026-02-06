package collector

import (
	"context"
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
)

// Provider defines the subset of the rdma.Provider interface required by the collector.
type Provider interface {
	Devices(ctx context.Context) ([]rdma.Device, error)
}

// NetDevStatsProvider fetches ethtool-like statistics for a network device.
type NetDevStatsProvider interface {
	Stats(ctx context.Context, netDev string) (map[string]uint64, error)
}

// Option configures collector behavior.
type Option func(*RdmaCollector)

// RdmaCollector implements prometheus.Collector for RDMA device metrics.
type RdmaCollector struct {
	provider Provider
	logger   *slog.Logger

	portInfoDesc *prometheus.Desc

	portStatMetrics  map[string]metricEntry
	portStatLookup   map[string]string
	portHwMetrics    map[string]metricEntry
	portHwStatLookup map[string]string

	rocePFCPauseFramesDesc      *prometheus.Desc
	rocePFCPauseDurationDesc    *prometheus.Desc
	rocePFCPauseTransitionsDesc *prometheus.Desc

	scrapeErrors        prometheus.Counter
	rocePFCScrapeErrors prometheus.Counter

	netDevStatsProvider NetDevStatsProvider

	collectMu sync.Mutex
	ctxValue  atomic.Value // stores contextHolder
}

type contextHolder struct {
	ctx context.Context
}

type metricEntry struct {
	desc       *prometheus.Desc
	metricName string
	stat       string
	docName    string
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
			Help:    "The maximum period in ms which defines the aging of the counter reads. Two consecutive reads within this period might return the same values.",
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
)

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
		desc:       desc,
		metricName: metricName,
		stat:       stat,
		docName:    docName,
	}
	lookup[stat] = metricName

	return desc
}

func buildMetricName(docName string, existing map[string]metricEntry) string {
	base := sanitizeStatName(docName)
	metricName := fmt.Sprintf("rdma_%s_total", base)

	if entry, ok := existing[metricName]; ok && entry.docName != docName {
		metricName = fmt.Sprintf("rdma_%s_%x_total", base, fnv32(docName))
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

func fnv32(s string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return h.Sum32()
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
			[]string{"device", "port", "link_layer", "state", "phys_state", "link_width", "link_speed"},
			nil,
		),
		rocePFCPauseFramesDesc: prometheus.NewDesc(
			"rdma_roce_pfc_pause_frames_total",
			"RoCEv2 PFC pause frame counter sourced from ethtool stats.",
			[]string{"device", "port", "netdev", "direction", "priority"},
			nil,
		),
		rocePFCPauseDurationDesc: prometheus.NewDesc(
			"rdma_roce_pfc_pause_duration_total",
			"RoCEv2 PFC pause duration counter sourced from ethtool stats.",
			[]string{"device", "port", "netdev", "direction", "priority"},
			nil,
		),
		rocePFCPauseTransitionsDesc: prometheus.NewDesc(
			"rdma_roce_pfc_pause_transitions_total",
			"RoCEv2 PFC pause transition counter sourced from ethtool stats.",
			[]string{"device", "port", "netdev", "direction", "priority"},
			nil,
		),
		scrapeErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "rdma_scrape_errors_total",
			Help: "Total number of errors encountered while scraping RDMA sysfs.",
		}),
		rocePFCScrapeErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "rdma_roce_pfc_scrape_errors_total",
			Help: "Total number of errors encountered while scraping RoCEv2 PFC ethtool stats.",
		}),
		portStatMetrics:  make(map[string]metricEntry),
		portStatLookup:   make(map[string]string),
		portHwMetrics:    make(map[string]metricEntry),
		portHwStatLookup: make(map[string]string),
	}

	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}

	c.ctxValue.Store(contextHolder{ctx: context.Background()})

	return c
}

// WithNetDevStatsProvider configures a provider used to fetch netdev statistics
// for RoCEv2 PFC-related metrics.
func WithNetDevStatsProvider(provider NetDevStatsProvider) Option {
	return func(c *RdmaCollector) {
		c.netDevStatsProvider = provider
	}
}

// SetContext updates the context used by the next Collect invocation.
func (c *RdmaCollector) SetContext(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	c.ctxValue.Store(contextHolder{ctx: ctx})
}

// ResetContext resets the collector back to the background context.
func (c *RdmaCollector) ResetContext() {
	c.ctxValue.Store(contextHolder{ctx: context.Background()})
}

// Describe implements prometheus.Collector.
func (c *RdmaCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.portInfoDesc
	ch <- c.rocePFCPauseFramesDesc
	ch <- c.rocePFCPauseDurationDesc
	ch <- c.rocePFCPauseTransitionsDesc
	c.scrapeErrors.Describe(ch)
	c.rocePFCScrapeErrors.Describe(ch)

	c.collectMu.Lock()
	statDescs := make([]*prometheus.Desc, 0, len(c.portStatMetrics))
	for _, entry := range c.portStatMetrics {
		statDescs = append(statDescs, entry.desc)
	}
	hwDescs := make([]*prometheus.Desc, 0, len(c.portHwMetrics))
	for _, entry := range c.portHwMetrics {
		hwDescs = append(hwDescs, entry.desc)
	}
	c.collectMu.Unlock()

	for _, desc := range statDescs {
		ch <- desc
	}
	for _, desc := range hwDescs {
		ch <- desc
	}
}

// Collect implements prometheus.Collector.
func (c *RdmaCollector) Collect(ch chan<- prometheus.Metric) {
	c.collectMu.Lock()
	defer c.collectMu.Unlock()

	holder, _ := c.ctxValue.Load().(contextHolder)
	ctx := holder.ctx
	if ctx == nil {
		ctx = context.Background()
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

			attr := port.Attributes
			c.collectRoCEPFCMetrics(ctx, ch, device.Name, portID, attr, netDevStatsCache)

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
			)
		}
		c.logger.Debug("rdma device scraped",
			"device", device.Name,
			"ports", portIDStrings,
			"duration", time.Since(deviceStart))
	}

	c.scrapeErrors.Collect(ch)
	c.rocePFCScrapeErrors.Collect(ch)
}

// ScrapeErrors returns the scrape error counter collector for external registration.
func (c *RdmaCollector) ScrapeErrors() prometheus.Counter {
	return c.scrapeErrors
}

func sortedKeys(m map[string]uint64) []string {
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

func (c *RdmaCollector) collectRoCEPFCMetrics(
	ctx context.Context,
	ch chan<- prometheus.Metric,
	deviceName, portID string,
	attr rdma.PortAttributes,
	cache map[string]netDevStatsCacheEntry,
) {
	if c.netDevStatsProvider == nil {
		return
	}
	if attr.LinkLayer != "Ethernet" || attr.NetDev == "" {
		return
	}

	stats, err := c.readNetDevStatsWithCache(ctx, attr.NetDev, cache)
	if err != nil {
		if ctx.Err() != nil {
			c.logger.Warn("roce pfc scrape aborted by context", "device", deviceName, "port", portID, "netdev", attr.NetDev, "err", ctx.Err())
			return
		}
		c.logger.Warn("roce pfc scrape failed", "device", deviceName, "port", portID, "netdev", attr.NetDev, "err", err)
		return
	}

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
			attr.NetDev,
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
		c.rocePFCScrapeErrors.Inc()
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

package collector

import (
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"
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

// RdmaCollector implements prometheus.Collector for RDMA device metrics.
type RdmaCollector struct {
	provider Provider
	logger   *slog.Logger

	portInfoDesc *prometheus.Desc

	portStatMetrics  map[string]metricEntry
	portStatLookup   map[string]string
	portHwMetrics    map[string]metricEntry
	portHwStatLookup map[string]string

	scrapeErrors prometheus.Counter

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
	metricSpecs = map[string]metricSpec{
		"port_rcv_data": {
			DocName: "port_rcv_data",
			Help:    "The total number of data octets, divided by 4 (counting in double words, 32 bits), received on all VLs from the port.",
		},
		"port_rcv_packets": {
			DocName: "port_rcv_packets",
			Help:    "Total number of packets (may include packets containing errors).",
		},
		"multicast_rcv_packets": {
			DocName: "port_multicast_rcv_packets",
			Help:    "Total number of multicast packets, including multicast packets containing errors.",
		},
		"port_multicast_rcv_packets": {
			DocName: "port_multicast_rcv_packets",
			Help:    "Total number of multicast packets, including multicast packets containing errors.",
		},
		"unicast_rcv_packets": {
			DocName: "port_unicast_rcv_packets",
			Help:    "Total number of unicast packets, including unicast packets containing errors.",
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
		"multicast_xmit_packets": {
			DocName: "port_multicast_xmit_packets",
			Help:    "Total number of multicast packets transmitted on all VLs from the port (may include multicast packets with errors).",
		},
		"port_multicast_xmit_packets": {
			DocName: "port_multicast_xmit_packets",
			Help:    "Total number of multicast packets transmitted on all VLs from the port (may include multicast packets with errors).",
		},
		"unicast_xmit_packets": {
			DocName: "port_unicast_xmit_packets",
			Help:    "Total number of unicast packets transmitted on all VLs from the port (may include unicast packets with errors).",
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
		"symbol_errors": {
			DocName: "symbol_error",
			Help:    "Total number of minor link errors detected on one or more physical lanes.",
		},
		"vl15_dropped": {
			DocName: "VL15_dropped",
			Help:    "Number of incoming VL15 packets dropped due to resource limitations.",
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
			Help:    "The number of received packets where the request had already been executed.",
		},
		"implied_nak_seq_err": {
			DocName: "implied_nak_seq_err",
			Help:    "Number of implied NAK sequence errors detected.",
		},
		"local_ack_timeout_err": {
			DocName: "local_ack_timeout_err",
			Help:    "Number of local ACK timeout errors observed.",
		},
		"np_cnp_sent": {
			DocName: "np_cnp_sent",
			Help:    "Number of notification point congestion notification packets transmitted.",
		},
		"np_ecn_marked_roce_packets": {
			DocName: "np_ecn_marked_roce_packets",
			Help:    "Number of RoCE packets transmitted with ECN marking by the NP.",
		},
		"out_of_buffer": {
			DocName: "out_of_buffer",
			Help:    "Count of requests dropped because the responder ran out of receive buffers.",
		},
		"out_of_sequence": {
			DocName: "out_of_sequence",
			Help:    "Requests received out of sequence on the port.",
		},
		"packet_seq_err": {
			DocName: "packet_seq_err",
			Help:    "Packet sequence errors detected on the port.",
		},
		"req_cqe_error": {
			DocName: "req_cqe_error",
			Help:    "Completion queue entries with error for requester operations.",
		},
		"req_cqe_flush_error": {
			DocName: "req_cqe_flush_error",
			Help:    "Requester completion queue entries flushed due to QP error.",
		},
		"req_remote_access_errors": {
			DocName: "req_remote_access_errors",
			Help:    "Remote access errors reported for requester operations.",
		},
		"req_remote_invalid_request": {
			DocName: "req_remote_invalid_request",
			Help:    "Remote invalid request errors reported for requester operations.",
		},
		"resp_cqe_error": {
			DocName: "resp_cqe_error",
			Help:    "Completion queue entries with error for responder operations.",
		},
		"resp_cqe_flush_error": {
			DocName: "resp_cqe_flush_error",
			Help:    "Responder completion queue entries flushed due to QP error.",
		},
		"resp_local_length_error": {
			DocName: "resp_local_length_error",
			Help:    "Local length errors reported for responder operations.",
		},
		"resp_remote_access_errors": {
			DocName: "resp_remote_access_errors",
			Help:    "Remote access errors reported for responder operations.",
		},
		"rnr_nak_retry_err": {
			DocName: "rnr_nak_retry_err",
			Help:    "Count of times RNR NAK retries were exhausted.",
		},
		"roce_adp_retrans": {
			DocName: "roce_adp_retrans",
			Help:    "Number of adaptive retransmissions observed for RoCE.",
		},
		"roce_adp_retrans_to": {
			DocName: "roce_adp_retrans_to",
			Help:    "Adaptive retransmissions triggered by timeout for RoCE.",
		},
		"roce_slow_restart": {
			DocName: "roce_slow_restart",
			Help:    "Count of RoCE slow restart events.",
		},
		"roce_slow_restart_cnps": {
			DocName: "roce_slow_restart_cnps",
			Help:    "Number of CNPs that triggered RoCE slow restart.",
		},
		"roce_slow_restart_trans": {
			DocName: "roce_slow_restart_trans",
			Help:    "Number of retransmissions during RoCE slow restart.",
		},
		"rp_cnp_handled": {
			DocName: "rp_cnp_handled",
			Help:    "Number of congestion notification packets handled by the responder.",
		},
		"rp_cnp_ignored": {
			DocName: "rp_cnp_ignored",
			Help:    "Number of congestion notification packets ignored by the responder.",
		},
		"rx_atomic_requests": {
			DocName: "rx_atomic_requests",
			Help:    "Number of incoming Atomic requests processed.",
		},
		"rx_dct_connect": {
			DocName: "rx_dct_connect",
			Help:    "Number of DCT connections established.",
		},
		"rx_icrc_encapsulated": {
			DocName: "rx_icrc_encapsulated",
			Help:    "Packets received with ICRC encapsulation.",
		},
		"rx_read_requests": {
			DocName: "rx_read_requests",
			Help:    "Number of incoming RDMA read requests processed.",
		},
		"rx_write_requests": {
			DocName: "rx_write_requests",
			Help:    "Number of incoming RDMA write requests processed.",
		},
	}

	metricHelpByDocName = buildMetricHelpByDocName()
)

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
func New(provider Provider, logger *slog.Logger) *RdmaCollector {
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
		scrapeErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "rdma_scrape_errors_total",
			Help: "Total number of errors encountered while scraping RDMA sysfs.",
		}),
		portStatMetrics:  make(map[string]metricEntry),
		portStatLookup:   make(map[string]string),
		portHwMetrics:    make(map[string]metricEntry),
		portHwStatLookup: make(map[string]string),
	}
	c.ctxValue.Store(contextHolder{ctx: context.Background()})

	return c
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
	c.scrapeErrors.Describe(ch)

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

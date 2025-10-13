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
}

func (c *RdmaCollector) hwMetricDesc(stat string) *prometheus.Desc {
	return c.metricDesc(stat, "rdma_port_hw_", "RDMA port hardware counter sourced from sysfs hw_counters.", c.portHwMetrics, c.portHwStatLookup)
}

func (c *RdmaCollector) statMetricDesc(stat string) *prometheus.Desc {
	return c.metricDesc(stat, "rdma_port_", "RDMA port counter sourced from sysfs counters.", c.portStatMetrics, c.portStatLookup)
}

func (c *RdmaCollector) metricDesc(stat, prefix, help string, entries map[string]metricEntry, lookup map[string]string) *prometheus.Desc {
	if metricName, ok := lookup[stat]; ok {
		if entry, exists := entries[metricName]; exists {
			return entry.desc
		}
	}

	metricName := buildMetricName(prefix, stat, entries)
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
	}
	lookup[stat] = metricName

	return desc
}

func buildMetricName(prefix, stat string, existing map[string]metricEntry) string {
	base := sanitizeStatName(stat)
	metricName := fmt.Sprintf("%s%s_total", prefix, base)

	if entry, ok := existing[metricName]; ok && entry.stat != stat {
		metricName = fmt.Sprintf("%s%s_%x_total", prefix, base, fnv32(stat))
	}

	return metricName
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

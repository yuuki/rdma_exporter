package collector

import (
	"context"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

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

	portStatDesc   *prometheus.Desc
	portHwStatDesc *prometheus.Desc
	portInfoDesc   *prometheus.Desc

	scrapeErrors prometheus.Counter

	collectMu sync.Mutex
	ctxValue  atomic.Value // stores contextHolder
}

type contextHolder struct {
	ctx context.Context
}

// New creates a new RDMA collector with the provided provider and logger.
func New(provider Provider, logger *slog.Logger) *RdmaCollector {
	if logger == nil {
		logger = slog.Default()
	}

	c := &RdmaCollector{
		provider: provider,
		logger:   logger,
		portStatDesc: prometheus.NewDesc(
			"rdma_port_stat_total",
			"RDMA port counter sourced from sysfs counters.",
			[]string{"device", "port", "stat"},
			nil,
		),
		portHwStatDesc: prometheus.NewDesc(
			"rdma_port_hw_stat_total",
			"RDMA port hardware counter sourced from sysfs hw_counters.",
			[]string{"device", "port", "stat"},
			nil,
		),
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
	ch <- c.portStatDesc
	ch <- c.portHwStatDesc
	ch <- c.portInfoDesc
	c.scrapeErrors.Describe(ch)
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
					ch <- prometheus.MustNewConstMetric(
						c.portStatDesc,
						prometheus.CounterValue,
						value,
						device.Name,
						portID,
						name,
					)
				}
			}

			if len(port.HwStats) > 0 {
				names := sortedKeys(port.HwStats)
				for _, name := range names {
					value := float64(port.HwStats[name])
					ch <- prometheus.MustNewConstMetric(
						c.portHwStatDesc,
						prometheus.CounterValue,
						value,
						device.Name,
						portID,
						name,
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

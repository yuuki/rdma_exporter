package main

import (
	"log/slog"
	"strconv"
	"time"

	"github.com/Mellanox/rdmamap"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	namespace = "rdma"
)

// RDMACollector implements the prometheus.Collector interface
type RDMACollector struct {
	logger *slog.Logger

	// Metrics
	up             *prometheus.Desc
	scrapeDuration *prometheus.Desc
	portStat       *prometheus.Desc
}

// NewRDMACollector creates a new RDMA collector
func NewRDMACollector(logger *slog.Logger) *RDMACollector {
	return &RDMACollector{
		logger: logger,
		up: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "", "up"),
			"Was the last scrape of RDMA statistics successful.",
			nil,
			nil,
		),
		scrapeDuration: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "", "scrape_duration_seconds"),
			"Duration of the scrape in seconds.",
			nil,
			nil,
		),
		portStat: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "port", "stat_total"),
			"RDMA port statistics.",
			[]string{"device", "port", "stat"},
			nil,
		),
	}
}

// Describe implements prometheus.Collector
func (c *RDMACollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.up
	ch <- c.scrapeDuration
	ch <- c.portStat
}

// Collect implements prometheus.Collector
func (c *RDMACollector) Collect(ch chan<- prometheus.Metric) {
	start := time.Now()
	success := 1.0

	if err := c.collectRDMAStats(ch); err != nil {
		c.logger.Error("Failed to collect RDMA statistics", "error", err)
		success = 0.0
	}

	duration := time.Since(start).Seconds()

	ch <- prometheus.MustNewConstMetric(
		c.up,
		prometheus.GaugeValue,
		success,
	)
	ch <- prometheus.MustNewConstMetric(
		c.scrapeDuration,
		prometheus.GaugeValue,
		duration,
	)
}

// collectRDMAStats collects statistics from all RDMA devices
func (c *RDMACollector) collectRDMAStats(ch chan<- prometheus.Metric) error {
	devices := rdmamap.GetRdmaDeviceList()
	if len(devices) == 0 {
		c.logger.Warn("No RDMA devices found")
		return nil
	}

	c.logger.Debug("Found RDMA devices", "count", len(devices))

	for _, device := range devices {
		if err := c.collectDeviceStats(ch, device); err != nil {
			c.logger.Error("Failed to collect device statistics",
				"device", device,
				"error", err)
			// Continue with other devices even if one fails
			continue
		}
	}

	return nil
}

// collectDeviceStats collects statistics for a specific RDMA device
func (c *RDMACollector) collectDeviceStats(ch chan<- prometheus.Metric, device string) error {
	stats, err := rdmamap.GetRdmaSysfsAllPortsStats(device)
	if err != nil {
		return err
	}

	for _, portStats := range stats.PortStats {
		portStr := strconv.Itoa(portStats.Port)
		c.logger.Debug("Collecting port statistics",
			"device", device,
			"port", portStr)

		// Collect hardware counters
		for _, stat := range portStats.HwStats {
			c.collectStat(ch, device, portStr, stat)
		}

		// Collect standard counters
		for _, stat := range portStats.Stats {
			c.collectStat(ch, device, portStr, stat)
		}
	}

	return nil
}

// collectStat emits a single statistic as a Prometheus metric
func (c *RDMACollector) collectStat(ch chan<- prometheus.Metric, device, port string, stat rdmamap.RdmaStatEntry) {
	// Convert uint64 to float64
	value := float64(stat.Value)

	ch <- prometheus.MustNewConstMetric(
		c.portStat,
		prometheus.CounterValue,
		value,
		device,
		port,
		stat.Name,
	)
}

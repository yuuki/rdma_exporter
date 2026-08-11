package mlxlink

import (
	"log/slog"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type pcieEyeSnapshotSource interface {
	PCIeEyeSnapshots() *pcieEyeSnapshotSet
}

// PCIeEyeCollector exports the independently cached root PCIe Eye telemetry.
type PCIeEyeCollector struct {
	source     pcieEyeSnapshotSource
	staleAfter time.Duration
	now        func() time.Time
	logger     *slog.Logger

	descs       []*prometheus.Desc
	fom         *prometheus.Desc
	up          *prometheus.Desc
	duration    *prometheus.Desc
	lastSuccess *prometheus.Desc
}

// NewPCIeEyeCollector returns a collector for the poller's root PCIe Eye cache.
func NewPCIeEyeCollector(source *Poller, staleAfter time.Duration, logger *slog.Logger) *PCIeEyeCollector {
	return newPCIeEyeCollector(source, staleAfter, logger, time.Now)
}

func newPCIeEyeCollector(
	source pcieEyeSnapshotSource,
	staleAfter time.Duration,
	logger *slog.Logger,
	now func() time.Time,
) *PCIeEyeCollector {
	if logger == nil {
		logger = slog.Default()
	}
	c := &PCIeEyeCollector{source: source, staleAfter: staleAfter, now: now, logger: logger}
	base := []string{"device", "pci_addr"}
	c.fom = c.newDesc("mlxlink_pcie_eye_fom",
		"Vendor-defined root PCIe Eye figure-of-merit score reported by mlxlink.",
		append(base, "lane", "stage"))
	c.up = c.newDesc("mlxlink_pcie_eye_collector_up",
		"Whether the most recent root PCIe Eye collection for this device succeeded.", base)
	c.duration = c.newDesc("mlxlink_pcie_eye_collection_duration_seconds",
		"Duration of the latest root PCIe Eye collection attempt for this device in seconds.", base)
	c.lastSuccess = c.newDesc("mlxlink_pcie_eye_collection_last_success_timestamp_seconds",
		"Unix timestamp of the most recent successful root PCIe Eye collection for this device.", base)
	return c
}

func (c *PCIeEyeCollector) newDesc(name, help string, labels []string) *prometheus.Desc {
	desc := prometheus.NewDesc(name, help, labels, nil)
	c.descs = append(c.descs, desc)
	return desc
}

// Describe implements prometheus.Collector.
func (c *PCIeEyeCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range c.descs {
		ch <- desc
	}
}

// Collect implements prometheus.Collector without executing mlxlink.
func (c *PCIeEyeCollector) Collect(ch chan<- prometheus.Metric) {
	set := c.source.PCIeEyeSnapshots()
	if set == nil {
		return
	}
	now := c.now()
	for _, snapshot := range set.devices {
		labels := []string{snapshot.Target.Device, snapshot.Target.PCIAddr}
		up := 0.0
		if snapshot.LastError == "" && !snapshot.LastSuccess.IsZero() {
			up = 1
		}
		ch <- prometheus.MustNewConstMetric(c.up, prometheus.GaugeValue, up, labels...)
		ch <- prometheus.MustNewConstMetric(c.duration, prometheus.GaugeValue,
			snapshot.LastDuration.Seconds(), labels...)
		if !snapshot.LastSuccess.IsZero() {
			ch <- prometheus.MustNewConstMetric(c.lastSuccess, prometheus.GaugeValue,
				float64(snapshot.LastSuccess.UnixNano())/1e9, labels...)
		}
		if snapshot.LastSuccess.IsZero() || now.Sub(snapshot.LastSuccess) > c.staleAfter {
			c.logger.Debug("mlxlink PCIe Eye snapshot is stale, exporting self monitoring only",
				"device", snapshot.Target.Device, "pci_addr", snapshot.Target.PCIAddr,
				"last_success", snapshot.LastSuccess, "stale_after", c.staleAfter.String())
			continue
		}
		c.collectFOM(ch, snapshot.Data.InitialFOM, labels, "initial")
		c.collectFOM(ch, snapshot.Data.LastFOM, labels, "last")
	}
}

func (c *PCIeEyeCollector) collectFOM(ch chan<- prometheus.Metric, lanes []LaneValue, labels []string, stage string) {
	for _, lane := range lanes {
		values := []string{strconv.Itoa(lane.Lane), stage}
		ch <- prometheus.MustNewConstMetric(c.fom, prometheus.GaugeValue, lane.Value,
			concatLabels(labels, values)...)
	}
}

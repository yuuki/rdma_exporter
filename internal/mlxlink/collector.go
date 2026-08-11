package mlxlink

import (
	"log/slog"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// snapshotSource is the consumer side view of the poller: the collector only
// ever reads the published cache, never sysfs or mlxlink itself.
type snapshotSource interface {
	Snapshots() *snapshotSet
}

// CollectorOption customises a Collector at construction time.
type CollectorOption func(*Collector)

// WithNow replaces the clock used to decide staleness. Tests use it to pin the
// moment a scrape happens.
func WithNow(now func() time.Time) CollectorOption {
	return func(c *Collector) { c.now = now }
}

// Collector exports the cached mlxlink data. Collect never blocks on I/O, so a
// scrape costs the same whether or not a poll is in flight.
type Collector struct {
	source     snapshotSource
	staleAfter time.Duration
	now        func() time.Time
	logger     *slog.Logger

	descs []*prometheus.Desc

	linkInfo *prometheus.Desc

	effectivePhysicalErrors *prometheus.Desc
	rawPhysicalErrorsLane   *prometheus.Desc
	linkDown                *prometheus.Desc
	linkErrorRecovery       *prometheus.Desc

	effectiveBER   *prometheus.Desc
	rawBER         *prometheus.Desc
	rawBERLane     *prometheus.Desc
	rxFECCodewords *prometheus.Desc
	serDesTXFIR    *prometheus.Desc
	serDesTXDrive  *prometheus.Desc
	eyeFOM         *prometheus.Desc
	eyeGrade       *prometheus.Desc

	moduleTemperature  *prometheus.Desc
	moduleVoltage      *prometheus.Desc
	moduleBiasCurrent  *prometheus.Desc
	moduleRxPower      *prometheus.Desc
	moduleTxPower      *prometheus.Desc
	moduleFWFault      *prometheus.Desc
	datapathFWFault    *prometheus.Desc
	txFault            *prometheus.Desc
	txLOS              *prometheus.Desc
	rxLOS              *prometheus.Desc
	txCDRLossOfLock    *prometheus.Desc
	rxCDRLossOfLock    *prometheus.Desc
	datapathActive     *prometheus.Desc
	moduleInfo         *prometheus.Desc
	up                 *prometheus.Desc
	collectionDuration *prometheus.Desc
	lastSuccess        *prometheus.Desc
}

// NewCollector returns a collector serving the poller's cache. Snapshots older
// than staleAfter stop being exported. A nil logger falls back to slog.Default.
func NewCollector(source *Poller, staleAfter time.Duration, logger *slog.Logger, opts ...CollectorOption) *Collector {
	return newCollector(source, staleAfter, logger, opts...)
}

func newCollector(source snapshotSource, staleAfter time.Duration, logger *slog.Logger, opts ...CollectorOption) *Collector {
	if logger == nil {
		logger = slog.Default()
	}

	c := &Collector{
		source:     source,
		staleAfter: staleAfter,
		now:        time.Now,
		logger:     logger,
	}

	base := []string{"device", "port", "pci_addr"}
	lane := []string{"device", "port", "pci_addr", "lane"}
	// mlxlink counters are readable by any tool that can also reset them, so
	// every counter carries the same caveat.
	const resettable = " mlxlink counters can be cleared by other tooling, so this counter may reset."

	c.linkInfo = c.newDesc("mlxlink_link_info",
		"Physical link attributes reported by mlxlink, exported as labels with a constant value of 1.",
		append(base, "state", "physical_state", "speed", "width", "fec", "auto_negotiation"))

	c.effectivePhysicalErrors = c.newDesc("mlxlink_effective_physical_errors_total",
		"Effective physical errors reported by mlxlink."+resettable, base)
	c.rawPhysicalErrorsLane = c.newDesc("mlxlink_raw_physical_errors_total",
		"Raw physical errors per lane reported by mlxlink."+resettable, lane)
	c.linkDown = c.newDesc("mlxlink_link_down_total",
		"Link down events reported by mlxlink."+resettable, base)
	c.linkErrorRecovery = c.newDesc("mlxlink_link_error_recovery_total",
		"Link error recovery events reported by mlxlink."+resettable, base)

	c.effectiveBER = c.newDesc("mlxlink_effective_physical_ber",
		"Effective physical bit error ratio reported by mlxlink.", base)
	c.rawBER = c.newDesc("mlxlink_raw_physical_ber",
		"Raw physical bit error ratio reported by mlxlink.", base)
	c.rawBERLane = c.newDesc("mlxlink_raw_physical_ber_lane",
		"Raw physical bit error ratio per lane reported by mlxlink.", lane)
	c.rxFECCodewords = c.newDesc("mlxlink_rx_fec_codewords_total",
		"Received FEC codewords in the reported corrected-error range."+resettable,
		append(base, "bin", "error_count_min", "error_count_max"))
	c.serDesTXFIR = c.newDesc("mlxlink_serdes_tx_fir_coefficient",
		"Vendor-defined transmitter FIR tuning code reported by mlxlink.", append(lane, "tap"))
	c.serDesTXDrive = c.newDesc("mlxlink_serdes_tx_drive_amplitude",
		"Vendor-defined transmitter drive amplitude tuning code reported by mlxlink.", lane)
	c.eyeFOM = c.newDesc("mlxlink_eye_fom",
		"Vendor-defined network Eye figure-of-merit score reported by mlxlink.", append(lane, "stage"))
	c.eyeGrade = c.newDesc("mlxlink_eye_grade",
		"Vendor-defined network Eye grade reported by mlxlink.", append(lane, "position"))

	c.moduleTemperature = c.newDesc("mlxlink_module_temperature_celsius",
		"Optical module temperature in degrees Celsius.", base)
	c.moduleVoltage = c.newDesc("mlxlink_module_voltage_volts",
		"Optical module supply voltage in volts.", base)
	c.moduleBiasCurrent = c.newDesc("mlxlink_module_bias_current_amperes",
		"Optical module laser bias current per lane in amperes.", lane)
	c.moduleRxPower = c.newDesc("mlxlink_module_rx_power_dbm",
		"Optical module received power per lane in dBm.", lane)
	c.moduleTxPower = c.newDesc("mlxlink_module_tx_power_dbm",
		"Optical module transmitted power per lane in dBm.", lane)

	c.moduleFWFault = c.newDesc("mlxlink_module_fw_fault",
		"Optical module firmware fault, 1 when faulted.", base)
	c.datapathFWFault = c.newDesc("mlxlink_datapath_fw_fault",
		"Optical module datapath firmware fault, 1 when faulted.", base)
	c.txFault = c.newDesc("mlxlink_tx_fault",
		"Transmitter fault per lane, 1 when faulted.", lane)
	c.txLOS = c.newDesc("mlxlink_tx_los",
		"Transmitter loss of signal per lane, 1 when signal is lost.", lane)
	c.rxLOS = c.newDesc("mlxlink_rx_los",
		"Receiver loss of signal per lane, 1 when signal is lost.", lane)
	c.txCDRLossOfLock = c.newDesc("mlxlink_tx_cdr_loss_of_lock",
		"Transmitter clock and data recovery loss of lock per lane, 1 when unlocked.", lane)
	c.rxCDRLossOfLock = c.newDesc("mlxlink_rx_cdr_loss_of_lock",
		"Receiver clock and data recovery loss of lock per lane, 1 when unlocked.", lane)
	c.datapathActive = c.newDesc("mlxlink_datapath_active",
		"Optical module datapath state per lane, 1 when active.", lane)

	c.moduleInfo = c.newDesc("mlxlink_module_info",
		"Optical module inventory reported by mlxlink, exported as labels with a constant value of 1.",
		append(base, "identifier", "vendor", "part_number", "serial_number", "revision",
			"firmware_version", "active_host_compliance", "active_media_compliance", "cable_type"))

	c.up = c.newDesc("mlxlink_collector_up",
		"Whether the most recent mlxlink poll for this device succeeded.", base)
	c.collectionDuration = c.newDesc("mlxlink_collection_duration_seconds",
		"Duration of the latest mlxlink collection attempt, including all fallback invocations, for this device in seconds.", base)
	c.lastSuccess = c.newDesc("mlxlink_collection_last_success_timestamp_seconds",
		"Unix timestamp of the most recent successful mlxlink collection for this device.", base)

	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	return c
}

// newDesc registers the descriptor so Describe cannot fall out of sync with the
// metrics Collect emits.
func (c *Collector) newDesc(name, help string, labels []string) *prometheus.Desc {
	desc := prometheus.NewDesc(name, help, labels, nil)
	c.descs = append(c.descs, desc)
	return desc
}

// Describe implements prometheus.Collector.
func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range c.descs {
		ch <- desc
	}
}

// Collect implements prometheus.Collector. It reads the immutable snapshot set
// published by the poller; no locks are held and no commands are executed.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	set := c.source.Snapshots()
	if set == nil {
		return
	}

	now := c.now()
	for _, snapshot := range set.devices {
		c.collectDevice(ch, snapshot, now)
	}
}

func (c *Collector) collectDevice(ch chan<- prometheus.Metric, snapshot DeviceSnapshot, now time.Time) {
	labels := []string{snapshot.Target.Device, snapshot.Target.Port, snapshot.Target.PCIAddr}

	up := 0.0
	if snapshot.LastError == "" && !snapshot.LastSuccess.IsZero() {
		up = 1
	}
	ch <- prometheus.MustNewConstMetric(c.up, prometheus.GaugeValue, up, labels...)
	ch <- prometheus.MustNewConstMetric(c.collectionDuration, prometheus.GaugeValue,
		snapshot.LastDuration.Seconds(), labels...)
	// A device that never succeeded has no timestamp to report; emitting the
	// zero time would look like a successful collection in 1970.
	if !snapshot.LastSuccess.IsZero() {
		ch <- prometheus.MustNewConstMetric(c.lastSuccess, prometheus.GaugeValue,
			float64(snapshot.LastSuccess.UnixNano())/1e9, labels...)
	}

	if c.isStale(snapshot, now) {
		c.logger.Debug("mlxlink snapshot is stale, exporting self monitoring only",
			"device", snapshot.Target.Device, "port", snapshot.Target.Port,
			"last_success", snapshot.LastSuccess, "stale_after", c.staleAfter.String())
		return
	}

	c.collectLink(ch, snapshot.Data.Link, labels)
	c.collectCounters(ch, snapshot.Data.Counters, labels)
	c.collectModule(ch, snapshot.Data.Module, labels)
	c.collectFECHistogram(ch, snapshot.Data.FECHistogram, labels)
	c.collectSerDesTX(ch, snapshot.Data.SerDesTX, labels)
	c.collectEye(ch, snapshot.Data.Eye, labels)
}

// isStale reports whether the cached data is too old to publish. Data that was
// never collected is stale by definition.
func (c *Collector) isStale(snapshot DeviceSnapshot, now time.Time) bool {
	if snapshot.LastSuccess.IsZero() {
		return true
	}
	return now.Sub(snapshot.LastSuccess) > c.staleAfter
}

func (c *Collector) collectLink(ch chan<- prometheus.Metric, link LinkInfo, labels []string) {
	values := []string{link.State, link.PhysicalState, link.Speed, link.Width, link.FEC, link.AutoNegotiation}
	if !anyNonEmpty(values) {
		return
	}
	ch <- prometheus.MustNewConstMetric(c.linkInfo, prometheus.GaugeValue, 1,
		concatLabels(labels, values)...)
}

func (c *Collector) collectCounters(ch chan<- prometheus.Metric, counters Counters, labels []string) {
	sendValue(ch, c.effectivePhysicalErrors, prometheus.CounterValue, counters.EffectivePhysicalErrors, labels)
	sendValue(ch, c.linkDown, prometheus.CounterValue, counters.LinkDown, labels)
	sendValue(ch, c.linkErrorRecovery, prometheus.CounterValue, counters.LinkErrorRecovery, labels)
	sendValue(ch, c.effectiveBER, prometheus.GaugeValue, counters.EffectiveBER, labels)
	sendValue(ch, c.rawBER, prometheus.GaugeValue, counters.RawBER, labels)

	sendLanes(ch, c.rawPhysicalErrorsLane, prometheus.CounterValue, counters.RawPhysicalErrorsLane, labels)
	sendLanes(ch, c.rawBERLane, prometheus.GaugeValue, counters.RawBERLane, labels)
}

func (c *Collector) collectModule(ch chan<- prometheus.Metric, module Module, labels []string) {
	sendValue(ch, c.moduleTemperature, prometheus.GaugeValue, module.TemperatureCelsius, labels)
	sendValue(ch, c.moduleVoltage, prometheus.GaugeValue, module.VoltageVolts, labels)
	sendValue(ch, c.moduleFWFault, prometheus.GaugeValue, module.ModuleFWFault, labels)
	sendValue(ch, c.datapathFWFault, prometheus.GaugeValue, module.DatapathFWFault, labels)

	sendLanes(ch, c.moduleBiasCurrent, prometheus.GaugeValue, module.BiasCurrentAmperes, labels)
	sendLanes(ch, c.moduleRxPower, prometheus.GaugeValue, module.RxPowerDBm, labels)
	sendLanes(ch, c.moduleTxPower, prometheus.GaugeValue, module.TxPowerDBm, labels)
	sendLanes(ch, c.txFault, prometheus.GaugeValue, module.TxFault, labels)
	sendLanes(ch, c.txLOS, prometheus.GaugeValue, module.TxLOS, labels)
	sendLanes(ch, c.rxLOS, prometheus.GaugeValue, module.RxLOS, labels)
	sendLanes(ch, c.txCDRLossOfLock, prometheus.GaugeValue, module.TxCDRLOL, labels)
	sendLanes(ch, c.rxCDRLossOfLock, prometheus.GaugeValue, module.RxCDRLOL, labels)
	sendLanes(ch, c.datapathActive, prometheus.GaugeValue, module.DatapathActive, labels)

	info := module.Info
	values := []string{
		info.Identifier, info.Vendor, info.PartNumber, info.SerialNumber, info.Revision,
		info.FirmwareVersion, info.ActiveHostCompliance, info.ActiveMediaCompliance, info.CableType,
	}
	if !anyNonEmpty(values) {
		return
	}
	ch <- prometheus.MustNewConstMetric(c.moduleInfo, prometheus.GaugeValue, 1,
		concatLabels(labels, values)...)
}

func (c *Collector) collectFECHistogram(ch chan<- prometheus.Metric, bins []FECHistogramBin, labels []string) {
	for _, bin := range bins {
		values := []string{
			strconv.Itoa(bin.Bin),
			strconv.FormatUint(bin.ErrorCountMin, 10),
			strconv.FormatUint(bin.ErrorCountMax, 10),
		}
		ch <- prometheus.MustNewConstMetric(c.rxFECCodewords, prometheus.CounterValue,
			float64(bin.Occurrences), concatLabels(labels, values)...)
	}
}

func (c *Collector) collectSerDesTX(ch chan<- prometheus.Metric, serdes SerDesTX, labels []string) {
	for _, coefficient := range serdes.FIRCoefficients {
		values := []string{strconv.Itoa(coefficient.Lane), coefficient.Tap}
		ch <- prometheus.MustNewConstMetric(c.serDesTXFIR, prometheus.GaugeValue,
			coefficient.Value, concatLabels(labels, values)...)
	}
	sendLanes(ch, c.serDesTXDrive, prometheus.GaugeValue, serdes.DriveAmplitude, labels)
}

func (c *Collector) collectEye(ch chan<- prometheus.Metric, eye Eye, labels []string) {
	sendLanesWithLabel(ch, c.eyeFOM, eye.InitialFOM, labels, "initial")
	sendLanesWithLabel(ch, c.eyeFOM, eye.LastFOM, labels, "last")
	sendLanesWithLabel(ch, c.eyeGrade, eye.UpperGrade, labels, "upper")
	sendLanesWithLabel(ch, c.eyeGrade, eye.MidGrade, labels, "mid")
	sendLanesWithLabel(ch, c.eyeGrade, eye.LowerGrade, labels, "lower")
}

// sendValue drops samples the decoder could not read: a missing field must not
// become a zero in the time series.
func sendValue(ch chan<- prometheus.Metric, desc *prometheus.Desc, valueType prometheus.ValueType, value Value, labels []string) {
	if !value.Valid {
		return
	}
	ch <- prometheus.MustNewConstMetric(desc, valueType, value.Float, labels...)
}

func sendLanes(ch chan<- prometheus.Metric, desc *prometheus.Desc, valueType prometheus.ValueType, lanes []LaneValue, labels []string) {
	for _, lane := range lanes {
		ch <- prometheus.MustNewConstMetric(desc, valueType, lane.Value,
			concatLabels(labels, []string{strconv.Itoa(lane.Lane)})...)
	}
}

func sendLanesWithLabel(ch chan<- prometheus.Metric, desc *prometheus.Desc, lanes []LaneValue, labels []string, extra string) {
	for _, lane := range lanes {
		values := []string{strconv.Itoa(lane.Lane), extra}
		ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, lane.Value,
			concatLabels(labels, values)...)
	}
}

// concatLabels returns a fresh slice: label values handed to a metric must not
// share a backing array with the next metric's.
func concatLabels(base, extra []string) []string {
	labels := make([]string, 0, len(base)+len(extra))
	labels = append(labels, base...)
	return append(labels, extra...)
}

func anyNonEmpty(values []string) bool {
	for _, value := range values {
		if value != "" {
			return true
		}
	}
	return false
}

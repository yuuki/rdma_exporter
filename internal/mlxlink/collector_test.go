package mlxlink

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// collectorNow is the moment every collector test pretends a scrape happens.
var (
	collectorSuccess = time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	collectorNow     = collectorSuccess.Add(10 * time.Second)
	collectorTarget  = Target{Device: "mlx5_0", Port: "1", PCIAddr: "0000:1a:00.0", NetDev: "ens1f0np0"}
)

const collectorStaleAfter = 150 * time.Second

// dataMetricNames are the families that must disappear once data is stale.
var dataMetricNames = []string{
	"mlxlink_link_info",
	"mlxlink_rx_fec_codewords_total",
	"mlxlink_serdes_tx_fir_coefficient",
	"mlxlink_serdes_tx_drive_amplitude",
	"mlxlink_eye_fom",
	"mlxlink_eye_grade",
	"mlxlink_effective_physical_errors_total",
	"mlxlink_raw_physical_errors_total",
	"mlxlink_link_down_total",
	"mlxlink_link_error_recovery_total",
	"mlxlink_effective_physical_ber",
	"mlxlink_raw_physical_ber",
	"mlxlink_raw_physical_ber_lane",
	"mlxlink_module_temperature_celsius",
	"mlxlink_module_voltage_volts",
	"mlxlink_module_bias_current_amperes",
	"mlxlink_module_rx_power_dbm",
	"mlxlink_module_tx_power_dbm",
	"mlxlink_module_fw_fault",
	"mlxlink_datapath_fw_fault",
	"mlxlink_tx_fault",
	"mlxlink_tx_los",
	"mlxlink_rx_los",
	"mlxlink_tx_cdr_loss_of_lock",
	"mlxlink_rx_cdr_loss_of_lock",
	"mlxlink_datapath_active",
	"mlxlink_module_info",
}

func TestCollector_ExportsNetworkEye(t *testing.T) {
	t.Parallel()

	data := PortData{Eye: Eye{
		InitialFOM: []LaneValue{{Lane: 0, Value: 100}, {Lane: 3, Value: 98}},
		LastFOM:    []LaneValue{{Lane: 0, Value: 107}, {Lane: 3, Value: 101}},
		UpperGrade: []LaneValue{{Lane: 0, Value: 108}, {Lane: 3, Value: 100}},
		MidGrade:   []LaneValue{{Lane: 0, Value: 124}, {Lane: 3, Value: 114}},
		LowerGrade: []LaneValue{{Lane: 0, Value: 106}, {Lane: 3, Value: 95}},
	}}
	set := newSnapshotSet([]DeviceSnapshot{{
		Target:       collectorTarget,
		Data:         data,
		LastSuccess:  collectorSuccess,
		LastDuration: 830 * time.Millisecond,
	}})
	collector := newTestCollector(t, set, collectorNow)

	expected := `
# HELP mlxlink_eye_fom Vendor-defined network Eye figure-of-merit score reported by mlxlink.
# TYPE mlxlink_eye_fom gauge
mlxlink_eye_fom{device="mlx5_0",lane="0",pci_addr="0000:1a:00.0",port="1",stage="initial"} 100
mlxlink_eye_fom{device="mlx5_0",lane="0",pci_addr="0000:1a:00.0",port="1",stage="last"} 107
mlxlink_eye_fom{device="mlx5_0",lane="3",pci_addr="0000:1a:00.0",port="1",stage="initial"} 98
mlxlink_eye_fom{device="mlx5_0",lane="3",pci_addr="0000:1a:00.0",port="1",stage="last"} 101
# HELP mlxlink_eye_grade Vendor-defined network Eye grade reported by mlxlink.
# TYPE mlxlink_eye_grade gauge
mlxlink_eye_grade{device="mlx5_0",lane="0",pci_addr="0000:1a:00.0",port="1",position="lower"} 106
mlxlink_eye_grade{device="mlx5_0",lane="0",pci_addr="0000:1a:00.0",port="1",position="mid"} 124
mlxlink_eye_grade{device="mlx5_0",lane="0",pci_addr="0000:1a:00.0",port="1",position="upper"} 108
mlxlink_eye_grade{device="mlx5_0",lane="3",pci_addr="0000:1a:00.0",port="1",position="lower"} 95
mlxlink_eye_grade{device="mlx5_0",lane="3",pci_addr="0000:1a:00.0",port="1",position="mid"} 114
mlxlink_eye_grade{device="mlx5_0",lane="3",pci_addr="0000:1a:00.0",port="1",position="upper"} 100
`
	if err := testutil.CollectAndCompare(collector, strings.NewReader(expected),
		"mlxlink_eye_fom", "mlxlink_eye_grade"); err != nil {
		t.Fatalf("unexpected Eye metrics: %v", err)
	}
}

func TestCollector_ExportsFECHistogramAndSerDesTX(t *testing.T) {
	t.Parallel()

	data := PortData{
		FECHistogram: []FECHistogramBin{
			{Bin: 0, ErrorCountMin: 0, ErrorCountMax: 0, Occurrences: 1_274_099_439},
			{Bin: 8, ErrorCountMin: 15, ErrorCountMax: 16, Occurrences: 27},
		},
		SerDesTX: SerDesTX{
			FIRCoefficients: []SerDesFIRCoefficient{
				{Lane: 0, Tap: "pre3", Value: -3},
				{Lane: 0, Tap: "main", Value: 19},
				{Lane: 3, Tap: "post1", Value: -7},
			},
			DriveAmplitude: []LaneValue{
				{Lane: 0, Value: 4},
				{Lane: 3, Value: 7},
			},
		},
	}
	set := newSnapshotSet([]DeviceSnapshot{{
		Target:       collectorTarget,
		Data:         data,
		LastSuccess:  collectorSuccess,
		LastDuration: 700 * time.Millisecond,
	}})
	collector := newTestCollector(t, set, collectorNow)

	expected := `
# HELP mlxlink_rx_fec_codewords_total Received FEC codewords in the reported corrected-error range. mlxlink counters can be cleared by other tooling, so this counter may reset.
# TYPE mlxlink_rx_fec_codewords_total counter
mlxlink_rx_fec_codewords_total{bin="0",device="mlx5_0",error_count_max="0",error_count_min="0",pci_addr="0000:1a:00.0",port="1"} 1.274099439e+09
mlxlink_rx_fec_codewords_total{bin="8",device="mlx5_0",error_count_max="16",error_count_min="15",pci_addr="0000:1a:00.0",port="1"} 27
# HELP mlxlink_serdes_tx_drive_amplitude Vendor-defined transmitter drive amplitude tuning code reported by mlxlink.
# TYPE mlxlink_serdes_tx_drive_amplitude gauge
mlxlink_serdes_tx_drive_amplitude{device="mlx5_0",lane="0",pci_addr="0000:1a:00.0",port="1"} 4
mlxlink_serdes_tx_drive_amplitude{device="mlx5_0",lane="3",pci_addr="0000:1a:00.0",port="1"} 7
# HELP mlxlink_serdes_tx_fir_coefficient Vendor-defined transmitter FIR tuning code reported by mlxlink.
# TYPE mlxlink_serdes_tx_fir_coefficient gauge
mlxlink_serdes_tx_fir_coefficient{device="mlx5_0",lane="0",pci_addr="0000:1a:00.0",port="1",tap="main"} 19
mlxlink_serdes_tx_fir_coefficient{device="mlx5_0",lane="0",pci_addr="0000:1a:00.0",port="1",tap="pre3"} -3
mlxlink_serdes_tx_fir_coefficient{device="mlx5_0",lane="3",pci_addr="0000:1a:00.0",port="1",tap="post1"} -7
`
	if err := testutil.CollectAndCompare(collector, strings.NewReader(expected),
		"mlxlink_rx_fec_codewords_total",
		"mlxlink_serdes_tx_drive_amplitude",
		"mlxlink_serdes_tx_fir_coefficient",
	); err != nil {
		t.Fatalf("unexpected optional metrics: %v", err)
	}
}

type fakeSnapshotSource struct{ set *snapshotSet }

func (f fakeSnapshotSource) Snapshots() *snapshotSet { return f.set }

func newTestCollector(t *testing.T, set *snapshotSet, now time.Time) *Collector {
	t.Helper()

	return newCollector(fakeSnapshotSource{set: set}, collectorStaleAfter, newDiscardLogger(),
		WithNow(func() time.Time { return now }))
}

// fullPortData fills every field so the exposition test covers all families.
//
// Every family carries values no other family produces: analog families differ
// in magnitude or sign, the two scalar fault flags differ from each other, and
// each per lane flag family has its own bit pattern. Crossing the wiring of any
// two families therefore changes the expected exposition.
func fullPortData(lanes int) PortData {
	laneValues := func(first, step float64) []LaneValue {
		values := make([]LaneValue, 0, lanes)
		for i := range lanes {
			values = append(values, LaneValue{Lane: i, Value: first + step*float64(i)})
		}
		return values
	}
	flags := func(pattern ...float64) []LaneValue {
		values := make([]LaneValue, 0, lanes)
		for i := range lanes {
			values = append(values, LaneValue{Lane: i, Value: pattern[i%len(pattern)]})
		}
		return values
	}
	coefficients := make([]SerDesFIRCoefficient, 0, lanes*5)
	for lane := range lanes {
		for _, coefficient := range []struct {
			tap   string
			value float64
		}{
			{tap: "pre3", value: -3},
			{tap: "pre2", value: 0},
			{tap: "pre1", value: -9},
			{tap: "main", value: 43},
			{tap: "post1", value: -14},
		} {
			coefficients = append(coefficients, SerDesFIRCoefficient{
				Lane:  lane,
				Tap:   coefficient.tap,
				Value: coefficient.value + float64(lane),
			})
		}
	}

	return PortData{
		Link: LinkInfo{
			State:           "Active",
			PhysicalState:   "LinkUp",
			Speed:           "NDR",
			Width:           "4x",
			FEC:             "Standard RS-FEC - RS(544,514)",
			AutoNegotiation: "Force",
		},
		Counters: Counters{
			EffectivePhysicalErrors: Value{Float: 3, Valid: true},
			LinkDown:                Value{Float: 7, Valid: true},
			LinkErrorRecovery:       Value{Float: 2, Valid: true},
			EffectiveBER:            Value{Float: 1e-255, Valid: true},
			RawBER:                  Value{Float: 2.5e-09, Valid: true},
			RawPhysicalErrorsLane:   laneValues(4, 1),
			RawBERLane:              laneValues(1.5e-09, 1e-10),
		},
		FECHistogram: []FECHistogramBin{
			{Bin: 0, ErrorCountMin: 0, ErrorCountMax: 0, Occurrences: 22_858_119_037_881},
			{Bin: 8, ErrorCountMin: 15, ErrorCountMax: 16, Occurrences: 27},
		},
		SerDesTX: SerDesTX{
			FIRCoefficients: coefficients,
			DriveAmplitude:  laneValues(4, 1),
		},
		Module: Module{
			TemperatureCelsius: Value{Float: 45, Valid: true},
			VoltageVolts:       Value{Float: 3.3, Valid: true},
			BiasCurrentAmperes: laneValues(0.007, 0.0005),
			RxPowerDBm:         laneValues(-1.5, -0.25),
			TxPowerDBm:         laneValues(0.5, 0.125),
			ModuleFWFault:      Value{Float: 1, Valid: true},
			DatapathFWFault:    Value{Float: 0, Valid: true},
			TxFault:            flags(1, 0, 0),
			TxLOS:              flags(0, 1, 0),
			RxLOS:              flags(0, 0, 1),
			TxCDRLOL:           flags(1, 1, 0),
			RxCDRLOL:           flags(1, 0, 1),
			DatapathActive:     flags(0, 1, 1),
			Info: ModuleInfo{
				Identifier:            "QSFP-DD",
				Vendor:                "Mellanox",
				PartNumber:            "MMA4Z00-NS",
				SerialNumber:          "MT0000X00000",
				Revision:              "A1",
				FirmwareVersion:       "2.4.0",
				ActiveHostCompliance:  "400GAUI-4 C2M",
				ActiveMediaCompliance: "400G SR4",
				CableType:             "Optical Module",
			},
		},
	}
}

func TestCollector_ExportsAllFamilies(t *testing.T) {
	t.Parallel()

	set := newSnapshotSet([]DeviceSnapshot{{
		Target:       collectorTarget,
		Data:         fullPortData(3),
		LastSuccess:  collectorSuccess,
		LastDuration: 700 * time.Millisecond,
	}})
	collector := newTestCollector(t, set, collectorNow)

	if err := testutil.CollectAndCompare(collector, strings.NewReader(expositionAllFamilies)); err != nil {
		t.Fatalf("unexpected metrics: %v", err)
	}
}

func TestCollector_LintsClean(t *testing.T) {
	t.Parallel()

	set := newSnapshotSet([]DeviceSnapshot{{
		Target:       collectorTarget,
		Data:         fullPortData(3),
		LastSuccess:  collectorSuccess,
		LastDuration: 700 * time.Millisecond,
	}})
	collector := newTestCollector(t, set, collectorNow)

	problems, err := testutil.CollectAndLint(collector)
	if err != nil {
		t.Fatalf("CollectAndLint returned error: %v", err)
	}
	for _, problem := range problems {
		t.Errorf("lint problem for %s: %s", problem.Metric, problem.Text)
	}

	described := make(chan *prometheus.Desc, len(collector.descs)+1)
	collector.Describe(described)
	close(described)
	if got, want := len(described), len(collector.descs); got != want {
		t.Fatalf("expected Describe to emit %d descriptors, got %d", want, got)
	}
}

func TestCollector_OmitsInvalidValues(t *testing.T) {
	t.Parallel()

	// An empty PortData is what the fixture-gated decoder returns today: no
	// value is valid, so no data series may appear.
	set := newSnapshotSet([]DeviceSnapshot{{
		Target:       collectorTarget,
		LastSuccess:  collectorSuccess,
		LastDuration: 700 * time.Millisecond,
	}})
	collector := newTestCollector(t, set, collectorNow)

	if got := testutil.CollectAndCount(collector, dataMetricNames...); got != 0 {
		t.Fatalf("expected no data series for an empty PortData, got %d", got)
	}
	if got := testutil.CollectAndCount(collector); got != 3 {
		t.Fatalf("expected only the three self monitoring series, got %d", got)
	}
}

func TestCollector_OmitsInvalidValuesWithPartialData(t *testing.T) {
	t.Parallel()

	data := PortData{
		Link: LinkInfo{State: "Active"},
		Counters: Counters{
			LinkDown:              Value{Float: 4, Valid: true},
			EffectiveBER:          Value{},
			RawPhysicalErrorsLane: nil,
			RawBERLane:            []LaneValue{{Lane: 1, Value: 3e-09}},
		},
	}
	set := newSnapshotSet([]DeviceSnapshot{{
		Target:       collectorTarget,
		Data:         data,
		LastSuccess:  collectorSuccess,
		LastDuration: 700 * time.Millisecond,
	}})
	collector := newTestCollector(t, set, collectorNow)

	// A single non-empty label is enough to publish link_info; the rest of the
	// labels stay empty rather than suppressing the series.
	expected := `
# HELP mlxlink_link_down_total Link down events reported by mlxlink. mlxlink counters can be cleared by other tooling, so this counter may reset.
# TYPE mlxlink_link_down_total counter
mlxlink_link_down_total{device="mlx5_0",pci_addr="0000:1a:00.0",port="1"} 4
# HELP mlxlink_link_info Physical link attributes reported by mlxlink, exported as labels with a constant value of 1.
# TYPE mlxlink_link_info gauge
mlxlink_link_info{auto_negotiation="",device="mlx5_0",fec="",pci_addr="0000:1a:00.0",physical_state="",port="1",speed="",state="Active",width=""} 1
# HELP mlxlink_raw_physical_ber_lane Raw physical bit error ratio per lane reported by mlxlink.
# TYPE mlxlink_raw_physical_ber_lane gauge
mlxlink_raw_physical_ber_lane{device="mlx5_0",lane="1",pci_addr="0000:1a:00.0",port="1"} 3e-09
`
	if err := testutil.CollectAndCompare(collector, strings.NewReader(expected),
		"mlxlink_link_info", "mlxlink_link_down_total", "mlxlink_raw_physical_ber_lane"); err != nil {
		t.Fatalf("unexpected metrics: %v", err)
	}

	// Invalid values, empty lane slices and an all empty module inventory
	// produce no series at all.
	absent := []string{
		"mlxlink_effective_physical_ber",
		"mlxlink_effective_physical_errors_total",
		"mlxlink_link_error_recovery_total",
		"mlxlink_raw_physical_ber",
		"mlxlink_raw_physical_errors_total",
		"mlxlink_module_info",
		"mlxlink_module_temperature_celsius",
	}
	if got := testutil.CollectAndCount(collector, absent...); got != 0 {
		t.Fatalf("expected missing values to be dropped, got %d series", got)
	}
}

func TestCollector_StaleSuppressesData(t *testing.T) {
	t.Parallel()

	set := newSnapshotSet([]DeviceSnapshot{{
		Target:       collectorTarget,
		Data:         fullPortData(3),
		LastSuccess:  collectorSuccess,
		LastDuration: 700 * time.Millisecond,
	}})
	// One second past the staleness horizon.
	collector := newTestCollector(t, set, collectorSuccess.Add(collectorStaleAfter+time.Second))

	// The last poll succeeded, so staleness must suppress the data without
	// touching the self monitoring values: up stays 1 and the timestamp keeps
	// pointing at that success.
	expected := `
# HELP mlxlink_collection_duration_seconds Duration of the latest mlxlink collection attempt, including all fallback invocations, for this device in seconds.
# TYPE mlxlink_collection_duration_seconds gauge
mlxlink_collection_duration_seconds{device="mlx5_0",pci_addr="0000:1a:00.0",port="1"} 0.7
# HELP mlxlink_collection_last_success_timestamp_seconds Unix timestamp of the most recent successful mlxlink collection for this device.
# TYPE mlxlink_collection_last_success_timestamp_seconds gauge
mlxlink_collection_last_success_timestamp_seconds{device="mlx5_0",pci_addr="0000:1a:00.0",port="1"} 1.7859312e+09
# HELP mlxlink_collector_up Whether the most recent mlxlink poll for this device succeeded.
# TYPE mlxlink_collector_up gauge
mlxlink_collector_up{device="mlx5_0",pci_addr="0000:1a:00.0",port="1"} 1
`
	if err := testutil.CollectAndCompare(collector, strings.NewReader(expected)); err != nil {
		t.Fatalf("unexpected metrics: %v", err)
	}
	if got := testutil.CollectAndCount(collector, dataMetricNames...); got != 0 {
		t.Fatalf("expected stale data to be suppressed, got %d series", got)
	}

	// Exactly at the horizon the data is still fresh enough.
	fresh := newTestCollector(t, set, collectorSuccess.Add(collectorStaleAfter))
	if got := testutil.CollectAndCount(fresh, "mlxlink_link_info"); got != 1 {
		t.Fatalf("expected data at the staleness boundary, got %d series", got)
	}
}

func TestCollector_NeverSucceededDevice(t *testing.T) {
	t.Parallel()

	set := newSnapshotSet([]DeviceSnapshot{{
		Target:       collectorTarget,
		LastError:    ReasonPermissionDenied,
		LastDuration: 20 * time.Millisecond,
	}})
	collector := newTestCollector(t, set, collectorNow)

	expected := `
# HELP mlxlink_collection_duration_seconds Duration of the latest mlxlink collection attempt, including all fallback invocations, for this device in seconds.
# TYPE mlxlink_collection_duration_seconds gauge
mlxlink_collection_duration_seconds{device="mlx5_0",pci_addr="0000:1a:00.0",port="1"} 0.02
# HELP mlxlink_collection_last_success_timestamp_seconds Unix timestamp of the most recent successful mlxlink collection for this device.
# TYPE mlxlink_collection_last_success_timestamp_seconds gauge
# HELP mlxlink_collector_up Whether the most recent mlxlink poll for this device succeeded.
# TYPE mlxlink_collector_up gauge
mlxlink_collector_up{device="mlx5_0",pci_addr="0000:1a:00.0",port="1"} 0
`
	if err := testutil.CollectAndCompare(collector, strings.NewReader(expected)); err != nil {
		t.Fatalf("unexpected metrics: %v", err)
	}
}

func TestCollector_FailedDeviceKeepsLastData(t *testing.T) {
	t.Parallel()

	set := newSnapshotSet([]DeviceSnapshot{{
		Target:       collectorTarget,
		Data:         fullPortData(3),
		LastSuccess:  collectorSuccess,
		LastError:    ReasonTimeout,
		LastDuration: 3 * time.Second,
	}})
	collector := newTestCollector(t, set, collectorNow)

	if got := testutil.ToFloat64(constMetric(t, collector, "mlxlink_collector_up")); got != 0 {
		t.Fatalf("expected up=0 after a failed poll, got %v", got)
	}
	if got := testutil.CollectAndCount(collector, "mlxlink_link_info"); got != 1 {
		t.Fatalf("expected the last successful data to stay published, got %d series", got)
	}
	if got := testutil.CollectAndCount(collector, "mlxlink_module_rx_power_dbm"); got != 3 {
		t.Fatalf("expected every lane to stay published, got %d series", got)
	}
}

func TestCollector_EmptySnapshots(t *testing.T) {
	t.Parallel()

	collector := newTestCollector(t, nil, collectorNow)

	if got := testutil.CollectAndCount(collector); got != 0 {
		t.Fatalf("expected no series before the first sweep, got %d", got)
	}
}

// constMetric gathers a single-series family so its value can be asserted.
func constMetric(t *testing.T, collector *Collector, name string) prometheus.Collector {
	t.Helper()

	registry := prometheus.NewPedanticRegistry()
	registry.MustRegister(collector)

	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		if len(family.GetMetric()) != 1 {
			t.Fatalf("expected exactly one %s series, got %d", name, len(family.GetMetric()))
		}
		value := family.GetMetric()[0].GetGauge().GetValue()
		gauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: "proxy", Help: "proxy."})
		gauge.Set(value)
		return gauge
	}
	t.Fatalf("metric %s not found", name)
	return nil
}

func BenchmarkMlxlinkCollectorCollect(b *testing.B) {
	const (
		devices = 8
		lanes   = 8
	)

	snapshots := make([]DeviceSnapshot, 0, devices)
	for i := range devices {
		snapshots = append(snapshots, DeviceSnapshot{
			Target: Target{
				Device:  "mlx5_" + strconv.Itoa(i),
				Port:    "1",
				PCIAddr: "0000:1a:0" + strconv.Itoa(i) + ".0",
			},
			Data:         fullPortData(lanes),
			LastSuccess:  collectorSuccess,
			LastDuration: 700 * time.Millisecond,
		})
	}
	collector := newCollector(fakeSnapshotSource{set: newSnapshotSet(snapshots)},
		collectorStaleAfter, newDiscardLogger(), WithNow(func() time.Time { return collectorNow }))

	// Large enough that Collect never blocks on the unread channel.
	ch := make(chan prometheus.Metric, 4096)

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		collector.Collect(ch)
		for len(ch) > 0 {
			<-ch
		}
	}
}

// expositionAllFamilies is the complete output for one device with every
// field populated across three lanes.
const expositionAllFamilies = `
# HELP mlxlink_collection_duration_seconds Duration of the latest mlxlink collection attempt, including all fallback invocations, for this device in seconds.
# TYPE mlxlink_collection_duration_seconds gauge
mlxlink_collection_duration_seconds{device="mlx5_0",pci_addr="0000:1a:00.0",port="1"} 0.7
# HELP mlxlink_collection_last_success_timestamp_seconds Unix timestamp of the most recent successful mlxlink collection for this device.
# TYPE mlxlink_collection_last_success_timestamp_seconds gauge
mlxlink_collection_last_success_timestamp_seconds{device="mlx5_0",pci_addr="0000:1a:00.0",port="1"} 1.7859312e+09
# HELP mlxlink_collector_up Whether the most recent mlxlink poll for this device succeeded.
# TYPE mlxlink_collector_up gauge
mlxlink_collector_up{device="mlx5_0",pci_addr="0000:1a:00.0",port="1"} 1
# HELP mlxlink_datapath_active Optical module datapath state per lane, 1 when active.
# TYPE mlxlink_datapath_active gauge
mlxlink_datapath_active{device="mlx5_0",lane="0",pci_addr="0000:1a:00.0",port="1"} 0
mlxlink_datapath_active{device="mlx5_0",lane="1",pci_addr="0000:1a:00.0",port="1"} 1
mlxlink_datapath_active{device="mlx5_0",lane="2",pci_addr="0000:1a:00.0",port="1"} 1
# HELP mlxlink_datapath_fw_fault Optical module datapath firmware fault, 1 when faulted.
# TYPE mlxlink_datapath_fw_fault gauge
mlxlink_datapath_fw_fault{device="mlx5_0",pci_addr="0000:1a:00.0",port="1"} 0
# HELP mlxlink_effective_physical_ber Effective physical bit error ratio reported by mlxlink.
# TYPE mlxlink_effective_physical_ber gauge
mlxlink_effective_physical_ber{device="mlx5_0",pci_addr="0000:1a:00.0",port="1"} 1e-255
# HELP mlxlink_effective_physical_errors_total Effective physical errors reported by mlxlink. mlxlink counters can be cleared by other tooling, so this counter may reset.
# TYPE mlxlink_effective_physical_errors_total counter
mlxlink_effective_physical_errors_total{device="mlx5_0",pci_addr="0000:1a:00.0",port="1"} 3
# HELP mlxlink_link_down_total Link down events reported by mlxlink. mlxlink counters can be cleared by other tooling, so this counter may reset.
# TYPE mlxlink_link_down_total counter
mlxlink_link_down_total{device="mlx5_0",pci_addr="0000:1a:00.0",port="1"} 7
# HELP mlxlink_link_error_recovery_total Link error recovery events reported by mlxlink. mlxlink counters can be cleared by other tooling, so this counter may reset.
# TYPE mlxlink_link_error_recovery_total counter
mlxlink_link_error_recovery_total{device="mlx5_0",pci_addr="0000:1a:00.0",port="1"} 2
# HELP mlxlink_link_info Physical link attributes reported by mlxlink, exported as labels with a constant value of 1.
# TYPE mlxlink_link_info gauge
mlxlink_link_info{auto_negotiation="Force",device="mlx5_0",fec="Standard RS-FEC - RS(544,514)",pci_addr="0000:1a:00.0",physical_state="LinkUp",port="1",speed="NDR",state="Active",width="4x"} 1
# HELP mlxlink_module_bias_current_amperes Optical module laser bias current per lane in amperes.
# TYPE mlxlink_module_bias_current_amperes gauge
mlxlink_module_bias_current_amperes{device="mlx5_0",lane="0",pci_addr="0000:1a:00.0",port="1"} 0.007
mlxlink_module_bias_current_amperes{device="mlx5_0",lane="1",pci_addr="0000:1a:00.0",port="1"} 0.0075
mlxlink_module_bias_current_amperes{device="mlx5_0",lane="2",pci_addr="0000:1a:00.0",port="1"} 0.008
# HELP mlxlink_module_fw_fault Optical module firmware fault, 1 when faulted.
# TYPE mlxlink_module_fw_fault gauge
mlxlink_module_fw_fault{device="mlx5_0",pci_addr="0000:1a:00.0",port="1"} 1
# HELP mlxlink_module_info Optical module inventory reported by mlxlink, exported as labels with a constant value of 1.
# TYPE mlxlink_module_info gauge
mlxlink_module_info{active_host_compliance="400GAUI-4 C2M",active_media_compliance="400G SR4",cable_type="Optical Module",device="mlx5_0",firmware_version="2.4.0",identifier="QSFP-DD",part_number="MMA4Z00-NS",pci_addr="0000:1a:00.0",port="1",revision="A1",serial_number="MT0000X00000",vendor="Mellanox"} 1
# HELP mlxlink_module_rx_power_dbm Optical module received power per lane in dBm.
# TYPE mlxlink_module_rx_power_dbm gauge
mlxlink_module_rx_power_dbm{device="mlx5_0",lane="0",pci_addr="0000:1a:00.0",port="1"} -1.5
mlxlink_module_rx_power_dbm{device="mlx5_0",lane="1",pci_addr="0000:1a:00.0",port="1"} -1.75
mlxlink_module_rx_power_dbm{device="mlx5_0",lane="2",pci_addr="0000:1a:00.0",port="1"} -2
# HELP mlxlink_module_temperature_celsius Optical module temperature in degrees Celsius.
# TYPE mlxlink_module_temperature_celsius gauge
mlxlink_module_temperature_celsius{device="mlx5_0",pci_addr="0000:1a:00.0",port="1"} 45
# HELP mlxlink_module_tx_power_dbm Optical module transmitted power per lane in dBm.
# TYPE mlxlink_module_tx_power_dbm gauge
mlxlink_module_tx_power_dbm{device="mlx5_0",lane="0",pci_addr="0000:1a:00.0",port="1"} 0.5
mlxlink_module_tx_power_dbm{device="mlx5_0",lane="1",pci_addr="0000:1a:00.0",port="1"} 0.625
mlxlink_module_tx_power_dbm{device="mlx5_0",lane="2",pci_addr="0000:1a:00.0",port="1"} 0.75
# HELP mlxlink_module_voltage_volts Optical module supply voltage in volts.
# TYPE mlxlink_module_voltage_volts gauge
mlxlink_module_voltage_volts{device="mlx5_0",pci_addr="0000:1a:00.0",port="1"} 3.3
# HELP mlxlink_raw_physical_ber Raw physical bit error ratio reported by mlxlink.
# TYPE mlxlink_raw_physical_ber gauge
mlxlink_raw_physical_ber{device="mlx5_0",pci_addr="0000:1a:00.0",port="1"} 2.5e-09
# HELP mlxlink_raw_physical_ber_lane Raw physical bit error ratio per lane reported by mlxlink.
# TYPE mlxlink_raw_physical_ber_lane gauge
mlxlink_raw_physical_ber_lane{device="mlx5_0",lane="0",pci_addr="0000:1a:00.0",port="1"} 1.5e-09
mlxlink_raw_physical_ber_lane{device="mlx5_0",lane="1",pci_addr="0000:1a:00.0",port="1"} 1.6e-09
mlxlink_raw_physical_ber_lane{device="mlx5_0",lane="2",pci_addr="0000:1a:00.0",port="1"} 1.7e-09
# HELP mlxlink_raw_physical_errors_total Raw physical errors per lane reported by mlxlink. mlxlink counters can be cleared by other tooling, so this counter may reset.
# TYPE mlxlink_raw_physical_errors_total counter
mlxlink_raw_physical_errors_total{device="mlx5_0",lane="0",pci_addr="0000:1a:00.0",port="1"} 4
mlxlink_raw_physical_errors_total{device="mlx5_0",lane="1",pci_addr="0000:1a:00.0",port="1"} 5
mlxlink_raw_physical_errors_total{device="mlx5_0",lane="2",pci_addr="0000:1a:00.0",port="1"} 6
# HELP mlxlink_rx_cdr_loss_of_lock Receiver clock and data recovery loss of lock per lane, 1 when unlocked.
# TYPE mlxlink_rx_cdr_loss_of_lock gauge
mlxlink_rx_cdr_loss_of_lock{device="mlx5_0",lane="0",pci_addr="0000:1a:00.0",port="1"} 1
mlxlink_rx_cdr_loss_of_lock{device="mlx5_0",lane="1",pci_addr="0000:1a:00.0",port="1"} 0
mlxlink_rx_cdr_loss_of_lock{device="mlx5_0",lane="2",pci_addr="0000:1a:00.0",port="1"} 1
# HELP mlxlink_rx_fec_codewords_total Received FEC codewords in the reported corrected-error range. mlxlink counters can be cleared by other tooling, so this counter may reset.
# TYPE mlxlink_rx_fec_codewords_total counter
mlxlink_rx_fec_codewords_total{bin="0",device="mlx5_0",error_count_max="0",error_count_min="0",pci_addr="0000:1a:00.0",port="1"} 2.2858119037881e+13
mlxlink_rx_fec_codewords_total{bin="8",device="mlx5_0",error_count_max="16",error_count_min="15",pci_addr="0000:1a:00.0",port="1"} 27
# HELP mlxlink_rx_los Receiver loss of signal per lane, 1 when signal is lost.
# TYPE mlxlink_rx_los gauge
mlxlink_rx_los{device="mlx5_0",lane="0",pci_addr="0000:1a:00.0",port="1"} 0
mlxlink_rx_los{device="mlx5_0",lane="1",pci_addr="0000:1a:00.0",port="1"} 0
mlxlink_rx_los{device="mlx5_0",lane="2",pci_addr="0000:1a:00.0",port="1"} 1
# HELP mlxlink_serdes_tx_drive_amplitude Vendor-defined transmitter drive amplitude tuning code reported by mlxlink.
# TYPE mlxlink_serdes_tx_drive_amplitude gauge
mlxlink_serdes_tx_drive_amplitude{device="mlx5_0",lane="0",pci_addr="0000:1a:00.0",port="1"} 4
mlxlink_serdes_tx_drive_amplitude{device="mlx5_0",lane="1",pci_addr="0000:1a:00.0",port="1"} 5
mlxlink_serdes_tx_drive_amplitude{device="mlx5_0",lane="2",pci_addr="0000:1a:00.0",port="1"} 6
# HELP mlxlink_serdes_tx_fir_coefficient Vendor-defined transmitter FIR tuning code reported by mlxlink.
# TYPE mlxlink_serdes_tx_fir_coefficient gauge
mlxlink_serdes_tx_fir_coefficient{device="mlx5_0",lane="0",pci_addr="0000:1a:00.0",port="1",tap="main"} 43
mlxlink_serdes_tx_fir_coefficient{device="mlx5_0",lane="0",pci_addr="0000:1a:00.0",port="1",tap="post1"} -14
mlxlink_serdes_tx_fir_coefficient{device="mlx5_0",lane="0",pci_addr="0000:1a:00.0",port="1",tap="pre1"} -9
mlxlink_serdes_tx_fir_coefficient{device="mlx5_0",lane="0",pci_addr="0000:1a:00.0",port="1",tap="pre2"} 0
mlxlink_serdes_tx_fir_coefficient{device="mlx5_0",lane="0",pci_addr="0000:1a:00.0",port="1",tap="pre3"} -3
mlxlink_serdes_tx_fir_coefficient{device="mlx5_0",lane="1",pci_addr="0000:1a:00.0",port="1",tap="main"} 44
mlxlink_serdes_tx_fir_coefficient{device="mlx5_0",lane="1",pci_addr="0000:1a:00.0",port="1",tap="post1"} -13
mlxlink_serdes_tx_fir_coefficient{device="mlx5_0",lane="1",pci_addr="0000:1a:00.0",port="1",tap="pre1"} -8
mlxlink_serdes_tx_fir_coefficient{device="mlx5_0",lane="1",pci_addr="0000:1a:00.0",port="1",tap="pre2"} 1
mlxlink_serdes_tx_fir_coefficient{device="mlx5_0",lane="1",pci_addr="0000:1a:00.0",port="1",tap="pre3"} -2
mlxlink_serdes_tx_fir_coefficient{device="mlx5_0",lane="2",pci_addr="0000:1a:00.0",port="1",tap="main"} 45
mlxlink_serdes_tx_fir_coefficient{device="mlx5_0",lane="2",pci_addr="0000:1a:00.0",port="1",tap="post1"} -12
mlxlink_serdes_tx_fir_coefficient{device="mlx5_0",lane="2",pci_addr="0000:1a:00.0",port="1",tap="pre1"} -7
mlxlink_serdes_tx_fir_coefficient{device="mlx5_0",lane="2",pci_addr="0000:1a:00.0",port="1",tap="pre2"} 2
mlxlink_serdes_tx_fir_coefficient{device="mlx5_0",lane="2",pci_addr="0000:1a:00.0",port="1",tap="pre3"} -1
# HELP mlxlink_tx_cdr_loss_of_lock Transmitter clock and data recovery loss of lock per lane, 1 when unlocked.
# TYPE mlxlink_tx_cdr_loss_of_lock gauge
mlxlink_tx_cdr_loss_of_lock{device="mlx5_0",lane="0",pci_addr="0000:1a:00.0",port="1"} 1
mlxlink_tx_cdr_loss_of_lock{device="mlx5_0",lane="1",pci_addr="0000:1a:00.0",port="1"} 1
mlxlink_tx_cdr_loss_of_lock{device="mlx5_0",lane="2",pci_addr="0000:1a:00.0",port="1"} 0
# HELP mlxlink_tx_fault Transmitter fault per lane, 1 when faulted.
# TYPE mlxlink_tx_fault gauge
mlxlink_tx_fault{device="mlx5_0",lane="0",pci_addr="0000:1a:00.0",port="1"} 1
mlxlink_tx_fault{device="mlx5_0",lane="1",pci_addr="0000:1a:00.0",port="1"} 0
mlxlink_tx_fault{device="mlx5_0",lane="2",pci_addr="0000:1a:00.0",port="1"} 0
# HELP mlxlink_tx_los Transmitter loss of signal per lane, 1 when signal is lost.
# TYPE mlxlink_tx_los gauge
mlxlink_tx_los{device="mlx5_0",lane="0",pci_addr="0000:1a:00.0",port="1"} 0
mlxlink_tx_los{device="mlx5_0",lane="1",pci_addr="0000:1a:00.0",port="1"} 1
mlxlink_tx_los{device="mlx5_0",lane="2",pci_addr="0000:1a:00.0",port="1"} 0
`

func TestCollectorWithPoller_ExportsRealOptionalCapture(t *testing.T) {
	t.Parallel()

	clk := newFakeClock(1)
	runner := newFakeRunner(mlxlinkFixture(t, "mft-4.34.1-400g-fec-serdes.json"))
	poller := newTestPoller(t, newFakeDiscoverer([]Target{collectorTarget}), runner, clk)

	poller.sweep(context.Background())

	collector := newCollector(poller, collectorStaleAfter, newDiscardLogger(), WithNow(clk.Now))
	tests := []struct {
		name string
		want int
	}{
		{name: "mlxlink_rx_fec_codewords_total", want: 16},
		{name: "mlxlink_serdes_tx_fir_coefficient", want: 20},
		{name: "mlxlink_serdes_tx_drive_amplitude", want: 4},
	}
	for _, tt := range tests {
		if got := testutil.CollectAndCount(collector, tt.name); got != tt.want {
			t.Errorf("expected %d %s series from real capture, got %d", tt.want, tt.name, got)
		}
	}
}

func TestCollectorWithPoller_ExportsRealEyeCapture(t *testing.T) {
	t.Parallel()

	clk := newFakeClock(1)
	runner := newFakeRunner(minimalMlxlinkJSON)
	runner.setEyeResult(mlxlinkFixture(t, "mft-4.34.1-400g-eye.json"), nil)
	poller := newPoller(newFakeDiscoverer([]Target{collectorTarget}), runner, testPollInterval,
		newDiscardLogger(), withClock(clk), WithShowEye(true))

	poller.sweep(context.Background())
	collector := newCollector(poller, collectorStaleAfter, newDiscardLogger(), WithNow(clk.Now))
	for _, tt := range []struct {
		name string
		want int
	}{
		{name: "mlxlink_eye_fom", want: 8},
		{name: "mlxlink_eye_grade", want: 12},
		{name: "mlxlink_rx_fec_codewords_total", want: 16},
		{name: "mlxlink_serdes_tx_fir_coefficient", want: 20},
	} {
		if got := testutil.CollectAndCount(collector, tt.name); got != tt.want {
			t.Errorf("expected %d %s series from real capture, got %d", tt.want, tt.name, got)
		}
	}
}

func TestPCIeEyeCollectorWithPoller_ExportsRealCapture(t *testing.T) {
	t.Parallel()

	clk := newFakeClock(1)
	runner := newFakeRunner(minimalMlxlinkJSON)
	runner.setPCIeEyeResult(mlxlinkFixture(t, "mft-4.34.1-pcie-eye.json"), nil)
	poller := newPoller(newFakeDiscoverer([]Target{collectorTarget}), runner, testPollInterval,
		newDiscardLogger(), withClock(clk), WithShowPCIeEye(true))

	poller.sweep(context.Background())
	collector := newPCIeEyeCollector(poller, collectorStaleAfter, newDiscardLogger(), clk.Now)
	if got := testutil.CollectAndCount(collector, "mlxlink_pcie_eye_fom"); got != 32 {
		t.Fatalf("expected 32 PCIe Eye FOM series from real capture, got %d", got)
	}
}

// TestCollectorWithPoller_ExportsRealCapture runs the whole path a scrape takes:
// the runner returns the captured mlxlink response, the poller decodes and
// publishes it, and the collector turns it into metrics. The values below are
// the fixture's, converted where the decoder converts.
func TestCollectorWithPoller_ExportsRealCapture(t *testing.T) {
	t.Parallel()

	clk := newFakeClock(1)
	runner := newFakeRunner(mlxlinkFixture(t, "mft-4.34.1-400g-dr4.json"))
	poller := newTestPoller(t, newFakeDiscoverer([]Target{collectorTarget}), runner, clk)

	poller.sweep(context.Background())

	collector := newCollector(poller, collectorStaleAfter, newDiscardLogger(), WithNow(clk.Now))

	if err := testutil.CollectAndCompare(collector, strings.NewReader(expositionRealCapture),
		"mlxlink_collector_up",
		"mlxlink_datapath_active",
		"mlxlink_effective_physical_ber",
		"mlxlink_link_info",
		"mlxlink_module_bias_current_amperes",
		"mlxlink_module_info",
		"mlxlink_module_rx_power_dbm",
		"mlxlink_module_temperature_celsius",
		"mlxlink_module_voltage_volts",
		"mlxlink_raw_physical_ber_lane",
		"mlxlink_raw_physical_errors_total",
	); err != nil {
		t.Fatalf("unexpected metrics: %v", err)
	}
}

// expositionRealCapture is the metric output for the captured MFT 4.34.1
// response, decoded through the poller.
const expositionRealCapture = `
# HELP mlxlink_collector_up Whether the most recent mlxlink poll for this device succeeded.
# TYPE mlxlink_collector_up gauge
mlxlink_collector_up{device="mlx5_0",pci_addr="0000:1a:00.0",port="1"} 1
# HELP mlxlink_datapath_active Optical module datapath state per lane, 1 when active.
# TYPE mlxlink_datapath_active gauge
mlxlink_datapath_active{device="mlx5_0",lane="0",pci_addr="0000:1a:00.0",port="1"} 1
mlxlink_datapath_active{device="mlx5_0",lane="1",pci_addr="0000:1a:00.0",port="1"} 1
mlxlink_datapath_active{device="mlx5_0",lane="2",pci_addr="0000:1a:00.0",port="1"} 1
mlxlink_datapath_active{device="mlx5_0",lane="3",pci_addr="0000:1a:00.0",port="1"} 1
# HELP mlxlink_effective_physical_ber Effective physical bit error ratio reported by mlxlink.
# TYPE mlxlink_effective_physical_ber gauge
mlxlink_effective_physical_ber{device="mlx5_0",pci_addr="0000:1a:00.0",port="1"} 1.5e-254
# HELP mlxlink_link_info Physical link attributes reported by mlxlink, exported as labels with a constant value of 1.
# TYPE mlxlink_link_info gauge
mlxlink_link_info{auto_negotiation="ON",device="mlx5_0",fec="Standard_RS-FEC - (544,514)",pci_addr="0000:1a:00.0",physical_state="ETH_AN_FSM_ENABLE",port="1",speed="400G",state="Active",width="4x"} 1
# HELP mlxlink_module_bias_current_amperes Optical module laser bias current per lane in amperes.
# TYPE mlxlink_module_bias_current_amperes gauge
mlxlink_module_bias_current_amperes{device="mlx5_0",lane="0",pci_addr="0000:1a:00.0",port="1"} 0.265504
mlxlink_module_bias_current_amperes{device="mlx5_0",lane="1",pci_addr="0000:1a:00.0",port="1"} 0.265504
mlxlink_module_bias_current_amperes{device="mlx5_0",lane="2",pci_addr="0000:1a:00.0",port="1"} 0.248416
mlxlink_module_bias_current_amperes{device="mlx5_0",lane="3",pci_addr="0000:1a:00.0",port="1"} 0.248416
# HELP mlxlink_module_info Optical module inventory reported by mlxlink, exported as labels with a constant value of 1.
# TYPE mlxlink_module_info gauge
mlxlink_module_info{active_host_compliance="IB NDR",active_media_compliance="400GBASE-DR4",cable_type="Optical Module (separated)",device="mlx5_0",firmware_version="40.242.17",identifier="OSFP",part_number="OSFP-400G-DR4",pci_addr="0000:1a:00.0",port="1",revision="1A",serial_number="S00XXX000000",vendor="EXAMPLE"} 1
# HELP mlxlink_module_rx_power_dbm Optical module received power per lane in dBm.
# TYPE mlxlink_module_rx_power_dbm gauge
mlxlink_module_rx_power_dbm{device="mlx5_0",lane="0",pci_addr="0000:1a:00.0",port="1"} 3.583
mlxlink_module_rx_power_dbm{device="mlx5_0",lane="1",pci_addr="0000:1a:00.0",port="1"} 3.253
mlxlink_module_rx_power_dbm{device="mlx5_0",lane="2",pci_addr="0000:1a:00.0",port="1"} 3.233
mlxlink_module_rx_power_dbm{device="mlx5_0",lane="3",pci_addr="0000:1a:00.0",port="1"} 2.658
# HELP mlxlink_module_temperature_celsius Optical module temperature in degrees Celsius.
# TYPE mlxlink_module_temperature_celsius gauge
mlxlink_module_temperature_celsius{device="mlx5_0",pci_addr="0000:1a:00.0",port="1"} 61
# HELP mlxlink_module_voltage_volts Optical module supply voltage in volts.
# TYPE mlxlink_module_voltage_volts gauge
mlxlink_module_voltage_volts{device="mlx5_0",pci_addr="0000:1a:00.0",port="1"} 3.2355
# HELP mlxlink_raw_physical_ber_lane Raw physical bit error ratio per lane reported by mlxlink.
# TYPE mlxlink_raw_physical_ber_lane gauge
mlxlink_raw_physical_ber_lane{device="mlx5_0",lane="0",pci_addr="0000:1a:00.0",port="1"} 1e-10
mlxlink_raw_physical_ber_lane{device="mlx5_0",lane="1",pci_addr="0000:1a:00.0",port="1"} 1e-09
mlxlink_raw_physical_ber_lane{device="mlx5_0",lane="2",pci_addr="0000:1a:00.0",port="1"} 4e-10
mlxlink_raw_physical_ber_lane{device="mlx5_0",lane="3",pci_addr="0000:1a:00.0",port="1"} 5e-10
# HELP mlxlink_raw_physical_errors_total Raw physical errors per lane reported by mlxlink. mlxlink counters can be cleared by other tooling, so this counter may reset.
# TYPE mlxlink_raw_physical_errors_total counter
mlxlink_raw_physical_errors_total{device="mlx5_0",lane="0",pci_addr="0000:1a:00.0",port="1"} 3.017647e+06
mlxlink_raw_physical_errors_total{device="mlx5_0",lane="1",pci_addr="0000:1a:00.0",port="1"} 1.5132549e+07
mlxlink_raw_physical_errors_total{device="mlx5_0",lane="2",pci_addr="0000:1a:00.0",port="1"} 7.368641e+06
mlxlink_raw_physical_errors_total{device="mlx5_0",lane="3",pci_addr="0000:1a:00.0",port="1"} 9.233545e+06
`

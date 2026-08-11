package mlxlink

import (
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

type fakePCIeEyeSnapshotSource struct{ set *pcieEyeSnapshotSet }

func (f fakePCIeEyeSnapshotSource) PCIeEyeSnapshots() *pcieEyeSnapshotSet { return f.set }

func newTestPCIeEyeCollector(set *pcieEyeSnapshotSet, now time.Time) *PCIeEyeCollector {
	return newPCIeEyeCollector(fakePCIeEyeSnapshotSource{set: set}, collectorStaleAfter,
		newDiscardLogger(), func() time.Time { return now })
}

func TestPCIeEyeCollector_ExportsMetricsAndSelfMonitoring(t *testing.T) {
	t.Parallel()

	set := newPCIeEyeSnapshotSet([]PCIeEyeSnapshot{{
		Target: collectorTarget,
		Data: PCIeEye{
			InitialFOM: []LaneValue{{Lane: 0, Value: 145}, {Lane: 15, Value: 135}},
			LastFOM:    []LaneValue{{Lane: 0, Value: 134}, {Lane: 15, Value: 131}},
		},
		LastSuccess:  collectorSuccess,
		LastDuration: 330 * time.Millisecond,
	}})
	collector := newTestPCIeEyeCollector(set, collectorNow)

	expected := `
# HELP mlxlink_pcie_eye_collection_duration_seconds Duration of the latest root PCIe Eye collection attempt for this device in seconds.
# TYPE mlxlink_pcie_eye_collection_duration_seconds gauge
mlxlink_pcie_eye_collection_duration_seconds{device="mlx5_0",pci_addr="0000:1a:00.0"} 0.33
# HELP mlxlink_pcie_eye_collection_last_success_timestamp_seconds Unix timestamp of the most recent successful root PCIe Eye collection for this device.
# TYPE mlxlink_pcie_eye_collection_last_success_timestamp_seconds gauge
mlxlink_pcie_eye_collection_last_success_timestamp_seconds{device="mlx5_0",pci_addr="0000:1a:00.0"} 1.7859312e+09
# HELP mlxlink_pcie_eye_collector_up Whether the most recent root PCIe Eye collection for this device succeeded.
# TYPE mlxlink_pcie_eye_collector_up gauge
mlxlink_pcie_eye_collector_up{device="mlx5_0",pci_addr="0000:1a:00.0"} 1
# HELP mlxlink_pcie_eye_fom Vendor-defined root PCIe Eye figure-of-merit score reported by mlxlink.
# TYPE mlxlink_pcie_eye_fom gauge
mlxlink_pcie_eye_fom{device="mlx5_0",lane="0",pci_addr="0000:1a:00.0",stage="initial"} 145
mlxlink_pcie_eye_fom{device="mlx5_0",lane="0",pci_addr="0000:1a:00.0",stage="last"} 134
mlxlink_pcie_eye_fom{device="mlx5_0",lane="15",pci_addr="0000:1a:00.0",stage="initial"} 135
mlxlink_pcie_eye_fom{device="mlx5_0",lane="15",pci_addr="0000:1a:00.0",stage="last"} 131
`
	if err := testutil.CollectAndCompare(collector, strings.NewReader(expected)); err != nil {
		t.Fatalf("unexpected PCIe Eye metrics: %v", err)
	}
}

func TestPCIeEyeCollector_FailureRetainsFreshData(t *testing.T) {
	t.Parallel()

	set := newPCIeEyeSnapshotSet([]PCIeEyeSnapshot{{
		Target:       collectorTarget,
		Data:         PCIeEye{InitialFOM: []LaneValue{{Lane: 0, Value: 145}}},
		LastSuccess:  collectorSuccess,
		LastError:    ReasonTimeout,
		LastDuration: 3 * time.Second,
	}})
	collector := newTestPCIeEyeCollector(set, collectorNow)

	if got := testutil.CollectAndCount(collector, "mlxlink_pcie_eye_fom"); got != 1 {
		t.Fatalf("expected fresh previous Eye data after failure, got %d series", got)
	}
	if got := testutil.ToFloat64(pcieEyeConstMetric(t, collector, "mlxlink_pcie_eye_collector_up")); got != 0 {
		t.Fatalf("expected PCIe Eye up=0 after failure, got %v", got)
	}
}

func TestPCIeEyeCollector_StaleSuppressesData(t *testing.T) {
	t.Parallel()

	set := newPCIeEyeSnapshotSet([]PCIeEyeSnapshot{{
		Target:       collectorTarget,
		Data:         PCIeEye{InitialFOM: []LaneValue{{Lane: 0, Value: 145}}},
		LastSuccess:  collectorSuccess,
		LastDuration: 330 * time.Millisecond,
	}})
	collector := newTestPCIeEyeCollector(set, collectorSuccess.Add(collectorStaleAfter+time.Second))

	if got := testutil.CollectAndCount(collector, "mlxlink_pcie_eye_fom"); got != 0 {
		t.Fatalf("expected stale PCIe Eye data to be suppressed, got %d series", got)
	}
	if got := testutil.CollectAndCount(collector); got != 3 {
		t.Fatalf("expected only PCIe Eye self-monitoring series, got %d", got)
	}
}

func TestPCIeEyeCollector_LintsAndRegistersPedantically(t *testing.T) {
	t.Parallel()

	set := newPCIeEyeSnapshotSet([]PCIeEyeSnapshot{{
		Target:       collectorTarget,
		Data:         PCIeEye{InitialFOM: []LaneValue{{Lane: 0, Value: 145}}},
		LastSuccess:  collectorSuccess,
		LastDuration: 330 * time.Millisecond,
	}})
	collector := newTestPCIeEyeCollector(set, collectorNow)

	problems, err := testutil.CollectAndLint(collector)
	if err != nil {
		t.Fatalf("CollectAndLint returned error: %v", err)
	}
	for _, problem := range problems {
		t.Errorf("lint problem for %s: %s", problem.Metric, problem.Text)
	}
	registry := prometheus.NewPedanticRegistry()
	registry.MustRegister(collector)
	if _, err := registry.Gather(); err != nil {
		t.Fatalf("pedantic gather failed: %v", err)
	}
}

func pcieEyeConstMetric(t *testing.T, collector *PCIeEyeCollector, name string) prometheus.Collector {
	t.Helper()

	registry := prometheus.NewPedanticRegistry()
	registry.MustRegister(collector)
	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, family := range families {
		if family.GetName() == name {
			gauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: "pcie_eye_proxy", Help: "proxy."})
			gauge.Set(family.GetMetric()[0].GetGauge().GetValue())
			return gauge
		}
	}
	t.Fatalf("metric %s not found", name)
	return nil
}

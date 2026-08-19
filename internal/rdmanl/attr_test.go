package rdmanl

import (
	"testing"
)

func TestParseDevices_NameAndIndex(t *testing.T) {
	t.Parallel()

	payload := concat(
		putU32(attrDevIndex, 3),
		putString(attrDevName, "mlx5_0"),
	)

	got, err := parseDevices(payload)
	if err != nil {
		t.Fatalf("parseDevices: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d devices, want 1", len(got))
	}
	if got[0].Name != "mlx5_0" || got[0].Index != 3 {
		t.Fatalf("got %+v, want name=mlx5_0 index=3", got[0])
	}
}

func TestParseHWCounters_StatusMarksOptionalByDynamicAttr(t *testing.T) {
	t.Parallel()

	// Kernel STAT_GET_STATUS: DYNAMIC is present only for IB_STAT_FLAG_OPTIONAL.
	// Value 1 means enabled (!is_disabled). Static counters have no DYNAMIC attr.
	payload := nest(attrStatHWCounters, concat(
		nest(attrStatHWCounterEntry, concat(
			putString(attrStatHWCounterEntryName, "np_cnp_sent"),
			putU32(attrStatHWCounterIndex, 0),
		)),
		nest(attrStatHWCounterEntry, concat(
			putString(attrStatHWCounterEntryName, "cc_rx_ce_pkts"),
			putU32(attrStatHWCounterIndex, 12),
			putU8(attrStatHWCounterDynamic, 1),
		)),
		nest(attrStatHWCounterEntry, concat(
			putString(attrStatHWCounterEntryName, "cc_rx_cnp_pkts"),
			putU32(attrStatHWCounterIndex, 13),
			putU8(attrStatHWCounterDynamic, 0),
		)),
	))

	got, err := parseHWCounters(payload)
	if err != nil {
		t.Fatalf("parseHWCounters: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d counters, want 3", len(got))
	}

	if got[0].Name != "np_cnp_sent" || got[0].Optional || got[0].Enabled {
		t.Fatalf("static counter: %+v", got[0])
	}
	if got[1].Name != "cc_rx_ce_pkts" || !got[1].Optional || !got[1].Enabled || got[1].Index != 12 {
		t.Fatalf("enabled optional: %+v", got[1])
	}
	if got[2].Name != "cc_rx_cnp_pkts" || !got[2].Optional || got[2].Enabled {
		t.Fatalf("disabled optional: %+v", got[2])
	}
}

func TestParseHWCounters_Values(t *testing.T) {
	t.Parallel()

	payload := nest(attrStatHWCounters, concat(
		nest(attrStatHWCounterEntry, concat(
			putString(attrStatHWCounterEntryName, "cc_rx_ce_pkts"),
			putU64(attrStatHWCounterEntryValue, 42),
		)),
		nest(attrStatHWCounterEntry, concat(
			putString(attrStatHWCounterEntryName, "np_cnp_sent"),
			putU64(attrStatHWCounterEntryValue, 7),
		)),
	))

	got, err := parseHWCounters(payload)
	if err != nil {
		t.Fatalf("parseHWCounters: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d counters, want 2", len(got))
	}
	if !got[0].HasValue || got[0].Value != 42 || got[0].Name != "cc_rx_ce_pkts" {
		t.Fatalf("first value: %+v", got[0])
	}
	if !got[1].HasValue || got[1].Value != 7 {
		t.Fatalf("second value: %+v", got[1])
	}
}

func TestMergeOptionalCounters_DropsStaticAndFillsEnabledValues(t *testing.T) {
	t.Parallel()

	status := []hwCounter{
		{Name: "np_cnp_sent", Index: 0},
		{Name: "cc_rx_ce_pkts", Index: 12, Optional: true, Enabled: true},
		{Name: "cc_rx_cnp_pkts", Index: 13, Optional: true, Enabled: false},
		{Name: "cc_tx_cnp_pkts", Index: 14, Optional: true, Enabled: true},
	}
	values := []hwCounter{
		{Name: "np_cnp_sent", Value: 99, HasValue: true},
		{Name: "cc_rx_ce_pkts", Value: 11, HasValue: true},
		{Name: "cc_tx_cnp_pkts", Value: 22, HasValue: true},
	}

	got := mergeOptionalCounters(status, values)
	want := []OptionalCounter{
		{Name: "cc_rx_ce_pkts", Enabled: true, Value: 11, HasValue: true},
		{Name: "cc_rx_cnp_pkts", Enabled: false},
		{Name: "cc_tx_cnp_pkts", Enabled: true, Value: 22, HasValue: true},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("index %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestParseDevices_EmptyPayload(t *testing.T) {
	t.Parallel()

	got, err := parseDevices(nil)
	if err != nil {
		t.Fatalf("parseDevices: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d devices, want 0", len(got))
	}
}

func TestParseHWCounters_TruncatedAttr(t *testing.T) {
	t.Parallel()

	if _, err := parseHWCounters([]byte{0x08, 0x00}); err == nil {
		t.Fatal("expected error for truncated attribute")
	}
}

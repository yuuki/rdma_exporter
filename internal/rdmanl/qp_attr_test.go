package rdmanl

import (
	"testing"
)

func TestParseQPMode_AutoType(t *testing.T) {
	t.Parallel()

	payload := concat(
		putU32(attrDevIndex, 3),
		putString(attrDevName, "mlx5_0"),
		putU32(attrPortIndex, 1),
		putU32(attrStatMode, counterModeAuto),
		putU32(attrStatAutoModeMask, counterMaskQPType),
	)
	got, err := parseQPMode(payload)
	if err != nil {
		t.Fatalf("parseQPMode: %v", err)
	}
	if got.Mode != "auto" || !got.MaskType || got.MaskPID {
		t.Fatalf("got %+v", got)
	}
}

func TestParseQPDump_TwoEntriesInOneMessageSkipsLQPN(t *testing.T) {
	t.Parallel()

	payload := concat(
		putU32(attrDevIndex, 3),
		putString(attrDevName, "mlx5_0"),
		nest(attrStatCounter, concat(
			qpCounterEntry(4, counterModeAuto, uint8ptr(ibQPTCRC), nil, map[string]uint64{
				"out_of_buffer":        9,
				"rx_write_requests":    3,
				"port_xmit_data":       100, // not in collector allowlist; parser still returns it
			}),
			qpCounterEntry(5, counterModeManual, nil, nil, map[string]uint64{
				"out_of_buffer": 7,
			}),
		)),
	)

	got, err := parseQPDump([][]byte{payload})
	if err != nil {
		t.Fatalf("parseQPDump: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d sets, want 2", len(got))
	}
	if got[0].Mode != "auto" || got[0].QPType != "RC" || got[0].HasPID {
		t.Fatalf("auto type set: %+v", got[0])
	}
	if got[0].Stats["out_of_buffer"] != 9 || got[0].Stats["rx_write_requests"] != 3 {
		t.Fatalf("auto stats: %+v", got[0].Stats)
	}
	if !got[0].AutoType() {
		t.Fatal("expected AutoType on first set")
	}
	if got[1].Mode != "manual" || got[1].QPType != "" || got[1].AutoType() {
		t.Fatalf("manual set should not be AutoType: %+v", got[1])
	}
}

func TestParseQPDump_SplitAcrossMessages(t *testing.T) {
	t.Parallel()

	msg1 := concat(
		putU32(attrDevIndex, 3),
		putString(attrDevName, "mlx5_0"),
		nest(attrStatCounter, qpCounterEntry(1, counterModeAuto, uint8ptr(ibQPTCRC), nil, map[string]uint64{
			"out_of_buffer": 1,
		})),
	)
	msg2 := concat(
		putU32(attrDevIndex, 3),
		putString(attrDevName, "mlx5_0"),
		nest(attrStatCounter, qpCounterEntry(2, counterModeAuto, uint8ptr(ibQPTCUD), nil, map[string]uint64{
			"out_of_buffer": 2,
		})),
	)

	got, err := parseQPDump([][]byte{msg1, msg2})
	if err != nil {
		t.Fatalf("parseQPDump: %v", err)
	}
	if len(got) != 2 || got[0].QPType != "RC" || got[1].QPType != "UD" {
		t.Fatalf("got %+v", got)
	}
}

func TestParseQPDump_PidSetIsNotAutoType(t *testing.T) {
	t.Parallel()

	pid := uint32(30489)
	payload := concat(
		putU32(attrDevIndex, 3),
		putString(attrDevName, "mlx5_0"),
		nest(attrStatCounter, qpCounterEntry(8, counterModeAuto, nil, &pid, map[string]uint64{
			"out_of_buffer": 4,
		})),
	)
	got, err := parseQPDump([][]byte{payload})
	if err != nil {
		t.Fatalf("parseQPDump: %v", err)
	}
	if len(got) != 1 || !got[0].HasPID || got[0].AutoType() {
		t.Fatalf("pid set should not AutoType: %+v", got[0])
	}
}

func TestParseQPDump_TypeAndPidIsNotAutoType(t *testing.T) {
	t.Parallel()

	pid := uint32(1)
	payload := concat(
		putU32(attrDevIndex, 3),
		putString(attrDevName, "mlx5_0"),
		nest(attrStatCounter, qpCounterEntry(8, counterModeAuto, uint8ptr(ibQPTCRC), &pid, map[string]uint64{
			"out_of_buffer": 4,
		})),
	)
	got, err := parseQPDump([][]byte{payload})
	if err != nil {
		t.Fatalf("parseQPDump: %v", err)
	}
	if len(got) != 1 || got[0].AutoType() {
		t.Fatalf("type+pid set should not AutoType: %+v", got[0])
	}
}

func TestEncodeQPModeQueryUsesManualSentinel(t *testing.T) {
	t.Parallel()

	payload := encodeQPModeQuery(3, 1)
	attrs, err := parseAttrs(payload)
	if err != nil {
		t.Fatalf("parseAttrs: %v", err)
	}
	var res, mode, index, port uint32
	for _, a := range attrs {
		switch a.Type {
		case attrDevIndex:
			index = binaryUint32(a.Data)
		case attrPortIndex:
			port = binaryUint32(a.Data)
		case attrStatRes:
			res = binaryUint32(a.Data)
		case attrStatMode:
			mode = binaryUint32(a.Data)
		}
	}
	if index != 3 || port != 1 || res != attrResQP || mode != counterModeManual {
		t.Fatalf("index=%d port=%d res=%d mode=%d", index, port, res, mode)
	}
}

func qpCounterEntry(id uint32, mode uint32, qpType *uint8, pid *uint32, stats map[string]uint64) []byte {
	inner := concat(
		putU32(attrPortIndex, 1),
		putU32(attrStatCounterID, id),
		putU32(attrStatMode, mode),
		nest(attrResQP, nest(attrResQPEntry, putU32(attrResLQPN, 178))), // parser must skip LQPN list
	)
	if qpType != nil {
		inner = concat(inner, putU8(attrResType, *qpType))
	}
	if pid != nil {
		inner = concat(inner, putU32(attrResPID, *pid))
	}
	var hw []byte
	for name, v := range stats {
		hw = concat(hw, nest(attrStatHWCounterEntry, concat(
			putString(attrStatHWCounterEntryName, name),
			putU64(attrStatHWCounterEntryValue, v),
		)))
	}
	inner = concat(inner, nest(attrStatHWCounters, hw))
	return nest(attrStatCounterEntry, inner)
}

func uint8ptr(v uint8) *uint8 { return &v }

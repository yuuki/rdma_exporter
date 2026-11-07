package rdmanl

import (
	"bytes"
	"context"
	"encoding/binary"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// encodeNlMsgs frames msgs the way the kernel does, with zeroed padding.
func encodeNlMsgs(msgs []nlMsg) []byte {
	var buf []byte
	for _, m := range msgs {
		total := nlmsgHdrLen + len(m.data)
		b := make([]byte, nlaAlign(total))
		binary.LittleEndian.PutUint32(b[0:4], uint32(total))
		binary.LittleEndian.PutUint16(b[4:6], m.typ)
		binary.LittleEndian.PutUint16(b[6:8], m.flags)
		binary.LittleEndian.PutUint32(b[8:12], m.seq)
		copy(b[nlmsgHdrLen:], m.data)
		buf = append(buf, b...)
	}
	return buf
}

// FuzzParseNlMsgs checks the framing of a received datagram: no header or
// payload reaches past the buffer, payloads are copies that outlive it, and
// re-framing what was parsed parses back to the same messages.
func FuzzParseNlMsgs(f *testing.F) {
	f.Add(encodeNlMsgs([]nlMsg{
		{typ: nlType(cmdStatGetStatus), seq: 7, data: []byte{1, 2, 3, 4}},
		{typ: nlmsgError, seq: 7, data: make([]byte, 4)},
	}))
	f.Add(encodeNlMsgs([]nlMsg{{typ: nlType(cmdStatGet), flags: nlmFDumpIntr, seq: 1, data: []byte("x")}, {typ: nlmsgDone, seq: 1}}))
	f.Add(encodeNlMsgs([]nlMsg{{typ: nlmsgOverrun}}))
	f.Add([]byte{1, 2, 3})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0})

	f.Fuzz(func(t *testing.T, buf []byte) {
		msgs, err := parseNlMsgs(buf)
		if err != nil {
			return
		}
		framed := 0
		for _, m := range msgs {
			framed += nlmsgHdrLen + len(m.data)
		}
		if framed > len(buf) {
			t.Fatalf("%d messages frame %d bytes of a %d-byte datagram", len(msgs), framed, len(buf))
		}

		want := make([][]byte, len(msgs))
		for i, m := range msgs {
			want[i] = bytes.Clone(m.data)
		}
		for i := range buf {
			buf[i] ^= 0xff
		}
		for i, m := range msgs {
			if !bytes.Equal(m.data, want[i]) {
				t.Fatalf("message %d payload aliases the receive buffer", i)
			}
		}

		again, err := parseNlMsgs(encodeNlMsgs(msgs))
		if err != nil {
			t.Fatalf("re-framed messages do not parse: %v", err)
		}
		if !slices.EqualFunc(again, msgs, func(a, b nlMsg) bool {
			return a.typ == b.typ && a.flags == b.flags && a.seq == b.seq && bytes.Equal(a.data, b.data)
		}) {
			t.Fatalf("round trip changed the messages:\n got %+v\nwant %+v", again, msgs)
		}
	})
}

// FuzzParseAttrs checks the attribute decoder that every reply goes through,
// at any nesting depth: no attribute reaches past its parent, and encoding
// what was parsed decodes back to the same attributes.
func FuzzParseAttrs(f *testing.F) {
	f.Add(concat(putU32(attrDevIndex, 3), putString(attrDevName, "mlx5_0")))
	f.Add(nest(attrStatHWCounters, nest(attrStatHWCounterEntry, concat(
		putString(attrStatHWCounterEntryName, "cc_rx_ce_pkts"),
		putU64(attrStatHWCounterEntryValue, 11),
		putU8(attrStatHWCounterDynamic, 1),
	))))
	f.Add(nest(attrStatCounter, qpCounterEntry(4, counterModeAuto, uint8ptr(ibQPTCRC), nil, map[string]uint64{"out_of_buffer": 9})))
	f.Add([]byte{8, 0, 1, 0, 1, 2})
	f.Add([]byte{3, 0, 1, 0})

	f.Fuzz(func(t *testing.T, data []byte) {
		checkAttrs(t, data, 0)
	})
}

func checkAttrs(t *testing.T, data []byte, depth int) {
	t.Helper()
	attrs, err := parseAttrs(data)
	if err != nil {
		return
	}
	var encoded []byte
	payload := 0
	for _, a := range attrs {
		if a.Type > nlaTypeMask {
			t.Fatalf("attribute type %#x keeps flag bits", a.Type)
		}
		payload += nlaHeaderLen + len(a.Data)
		typ := a.Type
		if a.Nested {
			typ |= nlaFNested
		}
		encoded = append(encoded, putBytes(typ, a.Data)...)
	}
	if payload > len(data) {
		t.Fatalf("%d attributes span %d bytes of %d", len(attrs), payload, len(data))
	}

	again, err := parseAttrs(encoded)
	if err != nil {
		t.Fatalf("re-encoded attributes do not parse: %v", err)
	}
	if !slices.EqualFunc(again, attrs, func(a, b nlAttr) bool {
		return a.Type == b.Type && a.Nested == b.Nested && bytes.Equal(a.Data, b.Data)
	}) {
		t.Fatalf("round trip changed the attributes:\n got %+v\nwant %+v", again, attrs)
	}

	// The decoders parse a payload as nested attributes whether or not the
	// kernel set NLA_F_NESTED, so every payload must be safe to descend into.
	if depth < 4 {
		for _, a := range attrs {
			checkAttrs(t, a.Data, depth+1)
		}
	}
}

// FuzzProviderReplies drives the Provider with arbitrary netlink replies to
// each of its requests. Errors are fine; the results that do come back must
// hold what the collector relies on.
func FuzzProviderReplies(f *testing.F) {
	device := concat(putU32(attrDevIndex, 3), putString(attrDevName, "mlx5_0"))
	status := nest(attrStatHWCounters, concat(
		nest(attrStatHWCounterEntry, concat(putString(attrStatHWCounterEntryName, "np_cnp_sent"), putU32(attrStatHWCounterIndex, 0))),
		nest(attrStatHWCounterEntry, concat(putString(attrStatHWCounterEntryName, "cc_rx_ce_pkts"), putU32(attrStatHWCounterIndex, 12), putU8(attrStatHWCounterDynamic, 1))),
		nest(attrStatHWCounterEntry, concat(putString(attrStatHWCounterEntryName, "cc_tx_cnp_pkts"), putU32(attrStatHWCounterIndex, 14), putU8(attrStatHWCounterDynamic, 0))),
	))
	values := nest(attrStatHWCounters, nest(attrStatHWCounterEntry, concat(
		putString(attrStatHWCounterEntryName, "cc_rx_ce_pkts"),
		putU64(attrStatHWCounterEntryValue, 11),
	)))
	mode := concat(putU32(attrStatMode, counterModeAuto), putU32(attrStatAutoModeMask, counterMaskQPType))
	dump := concat(putU32(attrDevIndex, 3), nest(attrStatCounter, concat(
		qpCounterEntry(4, counterModeAuto, uint8ptr(ibQPTCRC), nil, map[string]uint64{"out_of_buffer": 9}),
		qpCounterEntry(5, counterModeAuto, uint8ptr(42), uint32ptr(77), map[string]uint64{"rx_write_requests": 3}),
	)))
	f.Add(device, status, values, mode, dump)
	f.Add([]byte{}, []byte{}, []byte{}, []byte{}, []byte{})
	f.Add(putU32(attrDevIndex, 1), status[:len(status)-3], values[:9], mode[:6], dump[:len(dump)/2])

	f.Fuzz(func(t *testing.T, devReply, statusReply, valueReply, modeReply, dumpReply []byte) {
		ctx := context.Background()

		devices := newProvider(&stubQueryClient{dumpMsgs: [][]byte{devReply}})
		if err := devices.Prepare(ctx); err == nil {
			for name := range devices.devices {
				if name == "" {
					t.Fatalf("device dump %x resolved an empty device name", devReply)
				}
			}
		}

		p := newProvider(&stubQueryClient{
			dumpMsgs:   [][]byte{device},
			statusMsgs: [][]byte{statusReply},
			valueMsgs:  [][]byte{valueReply},
			qpModeMsgs: [][]byte{modeReply},
			qpDumpMsgs: [][]byte{dumpReply},
		})
		if err := p.Prepare(ctx); err != nil {
			t.Fatalf("Prepare: %v", err)
		}

		if counters, err := p.Counters(ctx, "mlx5_0", 1); err == nil {
			for _, c := range counters {
				if c.Name == "" || strings.HasSuffix(c.Name, "\x00") {
					t.Fatalf("optional counter name %q", c.Name)
				}
				if c.HasValue && !c.Enabled {
					t.Fatalf("disabled optional counter %q has a value", c.Name)
				}
			}
		}

		if m, err := p.QPMode(ctx, "mlx5_0", 1); err == nil && !slices.Contains([]string{"none", "auto", "manual"}, m.Mode) {
			t.Fatalf("QP mode %q", m.Mode)
		}

		if sets, err := p.QPSets(ctx, "mlx5_0", 1); err == nil {
			for _, s := range sets {
				if s.Stats == nil {
					t.Fatalf("QP set %+v has nil stats", s)
				}
				if !slices.Contains([]string{"", "none", "auto", "manual"}, s.Mode) {
					t.Fatalf("QP set mode %q", s.Mode)
				}
				if s.QPType != "" && !isQPTypeName(s.QPType) {
					t.Fatalf("QP type %q", s.QPType)
				}
				for name := range s.Stats {
					if name == "" {
						t.Fatalf("QP set %+v has an unnamed counter", s)
					}
				}
			}
		}
	})
}

// isQPTypeName reports whether name is what qpTypeName returns for some type:
// an IB_QPT_* name or, for an unknown type, its decimal number.
func isQPTypeName(name string) bool {
	if n, err := strconv.ParseUint(name, 10, 8); err == nil {
		return qpTypeName(uint8(n)) == name
	}
	for t := range 256 {
		if qpTypeName(uint8(t)) == name {
			return true
		}
	}
	return false
}

func uint32ptr(v uint32) *uint32 { return &v }

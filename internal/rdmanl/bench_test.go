package rdmanl

import (
	"fmt"
	"testing"
)

// BenchmarkParseQPDump decodes a QP counter dump of 64 auto-type sets, each
// with an LQPN list and 20 counters, split over 16 messages.
func BenchmarkParseQPDump(b *testing.B) {
	stats := make(map[string]uint64, 20)
	for i := range 20 {
		stats[fmt.Sprintf("counter_%d", i)] = uint64(i)
	}
	msgs := make([][]byte, 16)
	for m := range msgs {
		var entries []byte
		for s := range 4 {
			entries = concat(entries, qpCounterEntry(uint32(m*4+s), counterModeAuto, uint8ptr(ibQPTCRC), nil, stats))
		}
		msgs[m] = concat(putU32(attrDevIndex, 3), nest(attrStatCounter, entries))
	}

	b.ReportAllocs()
	for b.Loop() {
		sets, err := parseQPDump(msgs)
		if err != nil || len(sets) != 64 {
			b.Fatalf("parseQPDump: %d sets, err %v", len(sets), err)
		}
	}
}

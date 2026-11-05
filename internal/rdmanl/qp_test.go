package rdmanl

import (
	"maps"
	"testing"
)

func TestParseQPSetEntry(t *testing.T) {
	t.Parallel()

	hwEntry := func(attrs ...[]byte) []byte {
		return nest(attrStatHWCounterEntry, concat(attrs...))
	}

	cases := []struct {
		name    string
		data    []byte
		want    QPSet
		wantErr string
	}{
		{
			name: "all attributes",
			data: concat(
				putU32(attrStatCounterID, 4),
				putU32(attrStatMode, counterModeAuto),
				putU8(attrResType, ibQPTCRC),
				putU32(attrResPID, 30489),
				nest(attrResQP, nest(attrResQPEntry, putU32(attrResLQPN, 178))),
				nest(attrStatHWCounters, concat(
					hwEntry(putString(attrStatHWCounterEntryName, "out_of_buffer"), putU64(attrStatHWCounterEntryValue, 9)),
					hwEntry(putString(attrStatHWCounterEntryName, "rx_write_requests"), putU64(attrStatHWCounterEntryValue, 0)),
				)),
			),
			want: QPSet{Mode: "auto", QPType: "RC", HasPID: true, Stats: map[string]uint64{"out_of_buffer": 9, "rx_write_requests": 0}},
		},
		{
			name: "empty entry",
			data: nil,
			want: QPSet{Stats: map[string]uint64{}},
		},
		{
			name: "missing mode leaves it empty",
			data: putU8(attrResType, ibQPTCUD),
			want: QPSet{QPType: "UD", Stats: map[string]uint64{}},
		},
		{
			name: "unknown mode and qp type",
			data: concat(putU32(attrStatMode, 9), putU8(attrResType, 200)),
			want: QPSet{Mode: "none", QPType: "200", Stats: map[string]uint64{}},
		},
		{
			name: "last mode wins",
			data: concat(putU32(attrStatMode, counterModeAuto), putU32(attrStatMode, counterModeManual)),
			want: QPSet{Mode: "manual", Stats: map[string]uint64{}},
		},
		{
			name: "unnamed and valueless hw counters are dropped",
			data: nest(attrStatHWCounters, concat(
				hwEntry(putU64(attrStatHWCounterEntryValue, 1)),
				hwEntry(putString(attrStatHWCounterEntryName, ""), putU64(attrStatHWCounterEntryValue, 2)),
				hwEntry(putString(attrStatHWCounterEntryName, "no_value")),
				hwEntry(putString(attrStatHWCounterEntryName, "kept"), putU64(attrStatHWCounterEntryValue, 3)),
				putU32(attrStatHWCounterIndex, 7), // not an entry: skipped
			)),
			want: QPSet{Stats: map[string]uint64{"kept": 3}},
		},
		{
			name: "hw counter tables merge and repeated names overwrite",
			data: concat(
				nest(attrStatHWCounters, hwEntry(putString(attrStatHWCounterEntryName, "a"), putU64(attrStatHWCounterEntryValue, 1))),
				nest(attrStatHWCounters, concat(
					hwEntry(putString(attrStatHWCounterEntryName, "a"), putU64(attrStatHWCounterEntryValue, 5)),
					hwEntry(putString(attrStatHWCounterEntryName, "b"), putU64(attrStatHWCounterEntryValue, 6)),
				)),
			),
			want: QPSet{Stats: map[string]uint64{"a": 5, "b": 6}},
		},
		{
			name:    "malformed attributes",
			data:    []byte{1, 2, 3},
			wantErr: "truncated netlink attribute header at offset 0",
		},
		{
			name:    "short mode",
			data:    putBytes(attrStatMode, []byte{1, 0}),
			wantErr: "short STAT_MODE on counter set",
		},
		{
			name:    "short mode after a valid one",
			data:    concat(putU32(attrStatMode, counterModeAuto), putBytes(attrStatMode, nil)),
			wantErr: "short STAT_MODE on counter set",
		},
		{
			name:    "short qp type",
			data:    putBytes(attrResType, nil),
			wantErr: "short RES_TYPE",
		},
		{
			name:    "malformed hw counter table",
			data:    nest(attrStatHWCounters, []byte{9, 0}),
			wantErr: "truncated netlink attribute header at offset 0",
		},
		{
			name:    "malformed hw counter entry",
			data:    nest(attrStatHWCounters, hwEntry(putBytes(attrStatHWCounterEntryValue, []byte{1}))),
			wantErr: "short STAT_HWCOUNTER_ENTRY_VALUE",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseQPSetEntry(tc.data)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				if got.Mode != "" || got.QPType != "" || got.HasPID || got.Stats != nil {
					t.Fatalf("got %+v with error, want zero QPSet", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseQPSetEntry: %v", err)
			}
			if got.Mode != tc.want.Mode || got.QPType != tc.want.QPType || got.HasPID != tc.want.HasPID {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
			if got.Stats == nil || !maps.Equal(got.Stats, tc.want.Stats) {
				t.Fatalf("stats = %v, want %v", got.Stats, tc.want.Stats)
			}
		})
	}
}

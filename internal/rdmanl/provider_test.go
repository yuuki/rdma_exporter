package rdmanl

import (
	"context"
	"errors"
	"syscall"
	"testing"
)

type stubQueryClient struct {
	dumpMsgs      [][]byte
	statusMsgs    [][]byte
	valueMsgs     [][]byte
	dumpErr       error
	statusErr     error
	valueErr      error
	calls         []stubCall
	statusQueries int
	valueQueries  int
}

type stubCall struct {
	typ   uint16
	flags uint16
}

func (s *stubQueryClient) Execute(_ context.Context, typ uint16, flags uint16, _ []byte) ([][]byte, error) {
	s.calls = append(s.calls, stubCall{typ: typ, flags: flags})
	switch typ {
	case nlType(cmdGet):
		return s.dumpMsgs, s.dumpErr
	case nlType(cmdStatGetStatus):
		s.statusQueries++
		return s.statusMsgs, s.statusErr
	case nlType(cmdStatGet):
		s.valueQueries++
		return s.valueMsgs, s.valueErr
	default:
		return nil, errors.New("unexpected netlink type")
	}
}

func (s *stubQueryClient) Close() error { return nil }

func TestProvider_CountersMergesStatusAndValues(t *testing.T) {
	t.Parallel()

	client := &stubQueryClient{
		dumpMsgs: [][]byte{
			concat(putU32(attrDevIndex, 3), putString(attrDevName, "mlx5_0")),
		},
		statusMsgs: [][]byte{
			nest(attrStatHWCounters, concat(
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
					putString(attrStatHWCounterEntryName, "cc_tx_cnp_pkts"),
					putU32(attrStatHWCounterIndex, 14),
					putU8(attrStatHWCounterDynamic, 0),
				)),
			)),
		},
		valueMsgs: [][]byte{
			nest(attrStatHWCounters, concat(
				nest(attrStatHWCounterEntry, concat(
					putString(attrStatHWCounterEntryName, "np_cnp_sent"),
					putU64(attrStatHWCounterEntryValue, 99),
				)),
				nest(attrStatHWCounterEntry, concat(
					putString(attrStatHWCounterEntryName, "cc_rx_ce_pkts"),
					putU64(attrStatHWCounterEntryValue, 11),
				)),
			)),
		},
	}

	p := newProvider(client)
	if err := p.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if len(client.calls) == 0 || client.calls[0].typ != nlType(cmdGet) || client.calls[0].flags != nlmFRequest|nlmFDump {
		t.Fatalf("Prepare netlink call = %+v", client.calls)
	}
	got, err := p.Counters(context.Background(), "mlx5_0", 1)
	if err != nil {
		t.Fatalf("Counters: %v", err)
	}

	want := []OptionalCounter{
		{Name: "cc_rx_ce_pkts", Enabled: true, Value: 11, HasValue: true},
		{Name: "cc_tx_cnp_pkts", Enabled: false},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("index %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
	if client.statusQueries != 1 {
		t.Fatalf("expected STAT_GET_STATUS once, got %d", client.statusQueries)
	}
	if client.valueQueries != 1 {
		t.Fatalf("expected STAT_GET once, got %d", client.valueQueries)
	}
	if client.calls[1].typ != nlType(cmdStatGetStatus) || client.calls[1].flags != nlmFRequest|nlmFAck {
		t.Fatalf("status call = %+v", client.calls[1])
	}
	if client.calls[2].typ != nlType(cmdStatGet) || client.calls[2].flags != nlmFRequest|nlmFAck {
		t.Fatalf("value call = %+v", client.calls[2])
	}
}

func TestProvider_SkipsStatGetWhenNoOptionalEnabled(t *testing.T) {
	t.Parallel()

	client := &stubQueryClient{
		dumpMsgs: [][]byte{
			concat(putU32(attrDevIndex, 1), putString(attrDevName, "mlx5_0")),
		},
		statusMsgs: [][]byte{
			nest(attrStatHWCounters, nest(attrStatHWCounterEntry, concat(
				putString(attrStatHWCounterEntryName, "cc_rx_ce_pkts"),
				putU32(attrStatHWCounterIndex, 12),
				putU8(attrStatHWCounterDynamic, 0),
			))),
		},
		valueErr: errors.New("STAT_GET should not be called"),
	}

	p := newProvider(client)
	if err := p.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	got, err := p.Counters(context.Background(), "mlx5_0", 1)
	if err != nil {
		t.Fatalf("Counters: %v", err)
	}
	if len(got) != 1 || got[0].Enabled || got[0].HasValue {
		t.Fatalf("got %+v", got)
	}
	if client.valueQueries != 0 {
		t.Fatalf("STAT_GET should be skipped, got %d calls", client.valueQueries)
	}
}

func TestProvider_UnknownDevice(t *testing.T) {
	t.Parallel()

	client := &stubQueryClient{
		dumpMsgs: [][]byte{
			concat(putU32(attrDevIndex, 1), putString(attrDevName, "mlx5_0")),
		},
	}
	p := newProvider(client)
	if err := p.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if _, err := p.Counters(context.Background(), "mlx5_1", 1); err == nil {
		t.Fatal("expected error for unknown device")
	}
}

func TestProvider_PrepareError(t *testing.T) {
	t.Parallel()

	client := &stubQueryClient{dumpErr: errors.New("boom")}
	p := newProvider(client)
	if err := p.Prepare(context.Background()); err == nil {
		t.Fatal("expected Prepare error")
	}
}

func TestProvider_CachesUnsupportedStatus(t *testing.T) {
	t.Parallel()

	client := &stubQueryClient{
		dumpMsgs: [][]byte{
			concat(putU32(attrDevIndex, 1), putString(attrDevName, "mlx5_0")),
		},
		statusErr: syscall.EOPNOTSUPP,
	}
	p := newProvider(client)
	if err := p.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if _, err := p.Counters(context.Background(), "mlx5_0", 1); !isUnsupportedNetlink(err) {
		t.Fatalf("expected unsupported error, got %v", err)
	}
	got, err := p.Counters(context.Background(), "mlx5_0", 1)
	if err != nil {
		t.Fatalf("second Counters: %v", err)
	}
	if got != nil {
		t.Fatalf("expected no counters after unsupported cache, got %+v", got)
	}
	if client.statusQueries != 1 {
		t.Fatalf("STAT_GET_STATUS should run once, got %d", client.statusQueries)
	}
}

func TestEncodePortQueryContainsDeviceAndPort(t *testing.T) {
	t.Parallel()

	payload := encodePortQuery(3, 2)
	attrs, err := parseAttrs(payload)
	if err != nil {
		t.Fatalf("parseAttrs: %v", err)
	}
	var index, port uint32
	for _, a := range attrs {
		switch a.Type {
		case attrDevIndex:
			index = binaryUint32(a.Data)
		case attrPortIndex:
			port = binaryUint32(a.Data)
		}
	}
	if index != 3 || port != 2 {
		t.Fatalf("index=%d port=%d", index, port)
	}
}

func TestProvider_ContextCanceled(t *testing.T) {
	t.Parallel()

	p := newProvider(&stubQueryClient{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.Prepare(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled, got %v", err)
	}
}

func binaryUint32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

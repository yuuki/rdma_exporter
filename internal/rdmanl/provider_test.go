package rdmanl

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"syscall"
	"testing"
)

type stubQueryClient struct {
	mu            sync.Mutex
	dumpMsgs      [][]byte
	statusMsgs    [][]byte
	valueMsgs     [][]byte
	qpModeMsgs    [][]byte
	qpDumpMsgs    [][]byte
	dumpErr       error
	statusErr     error
	valueErr      error
	qpModeErr     error
	qpDumpErr     error
	calls         []stubCall
	statusQueries int
	valueQueries  int
	qpModeQueries int
	qpDumpQueries int
}

type stubCall struct {
	typ     uint16
	flags   uint16
	payload []byte
}

func (s *stubQueryClient) Execute(_ context.Context, typ uint16, flags uint16, payload []byte) ([][]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, stubCall{typ: typ, flags: flags, payload: payload})
	switch typ {
	case nlType(cmdGet):
		return s.dumpMsgs, s.dumpErr
	case nlType(cmdStatGetStatus):
		s.statusQueries++
		return s.statusMsgs, s.statusErr
	case nlType(cmdStatGet):
		if flags&nlmFDump != 0 {
			s.qpDumpQueries++
			return s.qpDumpMsgs, s.qpDumpErr
		}
		if attrPresent(payload, attrStatMode) {
			s.qpModeQueries++
			return s.qpModeMsgs, s.qpModeErr
		}
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

func TestProviderCounters(t *testing.T) {
	t.Parallel()

	optional := func(name string, enabled bool) []byte {
		var dynamic uint8
		if enabled {
			dynamic = 1
		}
		return nest(attrStatHWCounterEntry, concat(
			putString(attrStatHWCounterEntryName, name),
			putU8(attrStatHWCounterDynamic, dynamic),
		))
	}
	static := func(name string) []byte {
		return nest(attrStatHWCounterEntry, putString(attrStatHWCounterEntryName, name))
	}
	value := func(name string, v uint64) []byte {
		return nest(attrStatHWCounterEntry, concat(
			putString(attrStatHWCounterEntryName, name),
			putU64(attrStatHWCounterEntryValue, v),
		))
	}
	table := func(entries ...[]byte) []byte {
		return nest(attrStatHWCounters, concat(entries...))
	}
	truncated := []byte{1, 0}
	enabledStatus := [][]byte{table(optional("cc_rx_ce_pkts", true))}
	// mlx5_1 has index 7, so the queries for mlx5_1 port 2 carry (7, 2).
	// Expected errors take the errno text from the errno: it differs between
	// platforms (EOPNOTSUPP reads "operation not supported on socket" on darwin).
	statusCall := stubCall{typ: nlType(cmdStatGetStatus), flags: nlmFRequest | nlmFAck, payload: encodePortQuery(7, 2)}
	valueCall := stubCall{typ: nlType(cmdStatGet), flags: nlmFRequest | nlmFAck, payload: encodePortQuery(7, 2)}

	cases := []struct {
		name            string
		unprepared      bool
		unsupported     bool
		canceled        bool
		device          string
		port            int
		statusMsgs      [][]byte
		statusErr       error
		valueMsgs       [][]byte
		valueErr        error
		want            []OptionalCounter
		wantErr         string
		wantIs          error
		wantCalls       []stubCall
		wantUnsupported bool
	}{
		{
			name:     "canceled context is checked before the port",
			canceled: true,
			device:   "mlx5_1",
			port:     0,
			wantErr:  "context canceled",
			wantIs:   context.Canceled,
		},
		{
			name:    "port zero",
			device:  "mlx5_1",
			port:    0,
			wantErr: "invalid rdma port 0",
		},
		{
			name:            "negative port is rejected before the unsupported cache",
			unsupported:     true,
			device:          "mlx5_1",
			port:            -1,
			wantErr:         "invalid rdma port -1",
			wantUnsupported: true,
		},
		{
			name:            "unsupported cache answers before the device lookup",
			unsupported:     true,
			device:          "mlx5_9",
			port:            1,
			wantUnsupported: true,
		},
		{
			name:    "unknown device",
			device:  "mlx5_9",
			port:    1,
			wantErr: "rdma device mlx5_9 not found",
		},
		{
			name:       "devices not prepared",
			unprepared: true,
			device:     "mlx5_1",
			port:       2,
			wantErr:    "rdma device mlx5_1 not found",
		},
		{
			name:            "unsupported status is cached",
			device:          "mlx5_1",
			port:            2,
			statusErr:       syscall.EOPNOTSUPP,
			wantErr:         "optional counters require Linux 5.16+ STAT_GET_STATUS: " + syscall.EOPNOTSUPP.Error(),
			wantIs:          syscall.EOPNOTSUPP,
			wantCalls:       []stubCall{statusCall},
			wantUnsupported: true,
		},
		{
			name:            "wrapped ENOTSUP status is cached",
			device:          "mlx5_1",
			port:            2,
			statusErr:       fmt.Errorf("netlink: %w", syscall.ENOTSUP),
			wantErr:         "optional counters require Linux 5.16+ STAT_GET_STATUS: netlink: " + syscall.ENOTSUP.Error(),
			wantIs:          syscall.ENOTSUP,
			wantCalls:       []stubCall{statusCall},
			wantUnsupported: true,
		},
		{
			name:      "status failure",
			device:    "mlx5_1",
			port:      2,
			statusErr: syscall.EIO,
			wantErr:   "get optional counter status for mlx5_1/2: " + syscall.EIO.Error(),
			wantIs:    syscall.EIO,
			wantCalls: []stubCall{statusCall},
		},
		{
			name:       "malformed status",
			device:     "mlx5_1",
			port:       2,
			statusMsgs: [][]byte{truncated},
			wantErr:    "truncated netlink attribute header at offset 0",
			wantCalls:  []stubCall{statusCall},
		},
		{
			name:       "malformed later status message",
			device:     "mlx5_1",
			port:       2,
			statusMsgs: append(enabledStatus, truncated),
			wantErr:    "truncated netlink attribute header at offset 0",
			wantCalls:  []stubCall{statusCall},
		},
		{
			name:      "no status messages",
			device:    "mlx5_1",
			port:      2,
			want:      []OptionalCounter{},
			wantCalls: []stubCall{statusCall},
		},
		{
			name:       "only static counters",
			device:     "mlx5_1",
			port:       2,
			statusMsgs: [][]byte{table(static("np_cnp_sent"))},
			want:       []OptionalCounter{},
			wantCalls:  []stubCall{statusCall},
		},
		{
			name:       "optional counters all disabled",
			device:     "mlx5_1",
			port:       2,
			statusMsgs: [][]byte{table(static("np_cnp_sent"), optional("cc_rx_ce_pkts", false))},
			want:       []OptionalCounter{{Name: "cc_rx_ce_pkts"}},
			wantCalls:  []stubCall{statusCall},
		},
		{
			name:   "status and values split across messages",
			device: "mlx5_1",
			port:   2,
			statusMsgs: [][]byte{
				table(static("np_cnp_sent"), optional("cc_tx_cnp_pkts", false)),
				table(optional("cc_rx_ce_pkts", true), optional("rdma_rx_bytes", true), optional("cc_rx_cnp_pkts", true)),
			},
			valueMsgs: [][]byte{
				table(value("np_cnp_sent", 99), value("cc_rx_ce_pkts", 11)),
				table(value("rdma_rx_bytes", 0), value("cc_tx_cnp_pkts", 5)),
			},
			want: []OptionalCounter{
				{Name: "cc_tx_cnp_pkts"},
				{Name: "cc_rx_ce_pkts", Enabled: true, Value: 11, HasValue: true},
				{Name: "rdma_rx_bytes", Enabled: true, HasValue: true},
				{Name: "cc_rx_cnp_pkts", Enabled: true},
			},
			wantCalls: []stubCall{statusCall, valueCall},
		},
		{
			name:       "value failure",
			device:     "mlx5_1",
			port:       2,
			statusMsgs: enabledStatus,
			valueErr:   syscall.EIO,
			wantErr:    "get optional counter values for mlx5_1/2: " + syscall.EIO.Error(),
			wantIs:     syscall.EIO,
			wantCalls:  []stubCall{statusCall, valueCall},
		},
		{
			name:       "unsupported values are not cached",
			device:     "mlx5_1",
			port:       2,
			statusMsgs: enabledStatus,
			valueErr:   syscall.EOPNOTSUPP,
			wantErr:    "get optional counter values for mlx5_1/2: " + syscall.EOPNOTSUPP.Error(),
			wantIs:     syscall.EOPNOTSUPP,
			wantCalls:  []stubCall{statusCall, valueCall},
		},
		{
			name:       "malformed later value message",
			device:     "mlx5_1",
			port:       2,
			statusMsgs: enabledStatus,
			valueMsgs:  [][]byte{table(value("cc_rx_ce_pkts", 11)), truncated},
			wantErr:    "truncated netlink attribute header at offset 0",
			wantCalls:  []stubCall{statusCall, valueCall},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client := &stubQueryClient{
				dumpMsgs: [][]byte{
					concat(putU32(attrDevIndex, 3), putString(attrDevName, "mlx5_0")),
					concat(putU32(attrDevIndex, 7), putString(attrDevName, "mlx5_1")),
				},
				statusMsgs: tc.statusMsgs,
				statusErr:  tc.statusErr,
				valueMsgs:  tc.valueMsgs,
				valueErr:   tc.valueErr,
			}
			p := newProvider(client)
			if !tc.unprepared {
				if err := p.Prepare(context.Background()); err != nil {
					t.Fatalf("Prepare: %v", err)
				}
				client.calls = nil
			}
			p.unsupported = tc.unsupported
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.canceled {
				cancel()
			}

			got, err := p.Counters(ctx, tc.device, tc.port)
			if tc.wantErr == "" && err != nil {
				t.Fatalf("Counters: %v", err)
			}
			if tc.wantErr != "" && (err == nil || err.Error() != tc.wantErr) {
				t.Fatalf("Counters error = %v, want %q", err, tc.wantErr)
			}
			if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
				t.Fatalf("Counters error %v does not wrap %v", err, tc.wantIs)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Counters = %#v, want %#v", got, tc.want)
			}
			if !reflect.DeepEqual(client.calls, tc.wantCalls) {
				t.Fatalf("netlink calls = %+v, want %+v", client.calls, tc.wantCalls)
			}
			if p.unsupported != tc.wantUnsupported {
				t.Fatalf("unsupported = %v, want %v", p.unsupported, tc.wantUnsupported)
			}
		})
	}
}

// Counters reads the device table and the unsupported flag under the
// provider lock, so it may run concurrently with Prepare and with itself.
// The race detector reports a read or write that skips the lock.
func TestProviderCountersConcurrentWithPrepare(t *testing.T) {
	t.Parallel()

	client := &stubQueryClient{
		dumpMsgs:  [][]byte{concat(putU32(attrDevIndex, 1), putString(attrDevName, "mlx5_0"))},
		statusErr: syscall.EOPNOTSUPP,
	}
	p := newProvider(client)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_ = p.Prepare(context.Background())
		}()
		go func() {
			defer wg.Done()
			<-start
			_, _ = p.Counters(context.Background(), "mlx5_0", 1)
		}()
	}
	close(start)
	wg.Wait()
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

func TestProvider_QPModeAndSets(t *testing.T) {
	t.Parallel()

	client := &stubQueryClient{
		dumpMsgs: [][]byte{
			concat(putU32(attrDevIndex, 3), putString(attrDevName, "mlx5_0")),
		},
		qpModeMsgs: [][]byte{
			concat(
				putU32(attrPortIndex, 1),
				putU32(attrStatMode, counterModeAuto),
				putU32(attrStatAutoModeMask, counterMaskQPType),
			),
		},
		qpDumpMsgs: [][]byte{
			concat(
				putU32(attrDevIndex, 3),
				putString(attrDevName, "mlx5_0"),
				nest(attrStatCounter, concat(
					qpCounterEntry(4, counterModeAuto, uint8ptr(ibQPTCRC), nil, map[string]uint64{"out_of_buffer": 9}),
					qpCounterEntry(5, counterModeManual, nil, nil, map[string]uint64{"out_of_buffer": 1}),
				)),
			),
		},
	}
	p := newProvider(client)
	if err := p.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	mode, err := p.QPMode(context.Background(), "mlx5_0", 1)
	if err != nil {
		t.Fatalf("QPMode: %v", err)
	}
	if mode.Mode != "auto" || !mode.MaskType || mode.MaskPID {
		t.Fatalf("mode %+v", mode)
	}
	if client.qpModeQueries != 1 || client.calls[1].flags != nlmFRequest|nlmFAck {
		t.Fatalf("mode call flags=%v queries=%d", client.calls[1].flags, client.qpModeQueries)
	}

	sets, err := p.QPSets(context.Background(), "mlx5_0", 1)
	if err != nil {
		t.Fatalf("QPSets: %v", err)
	}
	if len(sets) != 2 || !sets[0].AutoType() || sets[1].AutoType() {
		t.Fatalf("sets %+v", sets)
	}
	if client.qpDumpQueries != 1 || client.calls[2].flags != nlmFRequest|nlmFAck|nlmFDump {
		t.Fatalf("dump call flags=%v", client.calls[2].flags)
	}
}

func TestProvider_QPDumpOverflow(t *testing.T) {
	t.Parallel()

	client := &stubQueryClient{
		dumpMsgs: [][]byte{
			concat(putU32(attrDevIndex, 1), putString(attrDevName, "mlx5_0")),
		},
		qpDumpErr: fmt.Errorf("%w: 1048577 bytes", ErrDumpOverflow),
	}
	p := newProvider(client)
	if err := p.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	_, err := p.QPSets(context.Background(), "mlx5_0", 1)
	if !errors.Is(err, ErrDumpOverflow) {
		t.Fatalf("expected overflow, got %v", err)
	}
	if p.unsupportedQP {
		t.Fatal("dump overflow must not cache unsupportedQP")
	}
}

func TestProvider_QPUnsupportedDoesNotDisableOptional(t *testing.T) {
	t.Parallel()

	client := &stubQueryClient{
		dumpMsgs: [][]byte{
			concat(putU32(attrDevIndex, 1), putString(attrDevName, "mlx5_0")),
		},
		qpModeErr: syscall.EOPNOTSUPP,
		statusMsgs: [][]byte{
			nest(attrStatHWCounters, nest(attrStatHWCounterEntry, concat(
				putString(attrStatHWCounterEntryName, "cc_rx_ce_pkts"),
				putU32(attrStatHWCounterIndex, 12),
				putU8(attrStatHWCounterDynamic, 0),
			))),
		},
	}
	p := newProvider(client)
	if err := p.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if _, err := p.QPMode(context.Background(), "mlx5_0", 1); !isUnsupportedNetlink(err) {
		t.Fatalf("expected unsupported QP mode, got %v", err)
	}
	got, err := p.Counters(context.Background(), "mlx5_0", 1)
	if err != nil {
		t.Fatalf("optional Counters: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("optional counters disabled by QP unsupported: %+v", got)
	}
	if client.statusQueries != 1 {
		t.Fatalf("STAT_GET_STATUS queries=%d", client.statusQueries)
	}
}

func TestProvider_QPUnsupportedCacheReturnsError(t *testing.T) {
	t.Parallel()

	client := &stubQueryClient{
		dumpMsgs: [][]byte{
			concat(putU32(attrDevIndex, 1), putString(attrDevName, "mlx5_0")),
		},
		qpModeErr: syscall.EOPNOTSUPP,
	}
	p := newProvider(client)
	if err := p.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if _, err := p.QPMode(context.Background(), "mlx5_0", 1); !errors.Is(err, ErrQPUnsupported) {
		t.Fatalf("first QPMode: %v", err)
	}
	if _, err := p.QPMode(context.Background(), "mlx5_0", 1); err != ErrQPUnsupported {
		t.Fatalf("cached QPMode: %v", err)
	}
	if _, err := p.QPSets(context.Background(), "mlx5_0", 1); err != ErrQPUnsupported {
		t.Fatalf("cached QPSets: %v", err)
	}
	if client.qpModeQueries != 1 {
		t.Fatalf("mode queries=%d", client.qpModeQueries)
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

func attrPresent(payload []byte, typ uint16) bool {
	attrs, err := parseAttrs(payload)
	if err != nil {
		return false
	}
	for _, a := range attrs {
		if a.Type == typ {
			return true
		}
	}
	return false
}

//go:build linux

package rdmanl

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestReconnectLockedReplacesSocket(t *testing.T) {
	c, err := dial()
	if err != nil {
		t.Skipf("NETLINK_RDMA unavailable: %v", err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	held, err := unix.Dup(c.fd)
	if err != nil {
		t.Fatalf("dup: %v", err)
	}
	defer unix.Close(held)

	oldSA, err := unix.Getsockname(held)
	if err != nil {
		t.Fatalf("getsockname old: %v", err)
	}
	oldNL, ok := oldSA.(*unix.SockaddrNetlink)
	if !ok {
		t.Fatalf("old sockaddr type %T", oldSA)
	}

	if err := c.reconnectLocked(); err != nil {
		t.Fatalf("reconnectLocked: %v", err)
	}
	if c.fd < 0 {
		t.Fatal("fd closed after reconnect")
	}

	newSA, err := unix.Getsockname(c.fd)
	if err != nil {
		t.Fatalf("getsockname new: %v", err)
	}
	newNL, ok := newSA.(*unix.SockaddrNetlink)
	if !ok {
		t.Fatalf("new sockaddr type %T", newSA)
	}
	if newNL.Pid == oldNL.Pid {
		t.Fatalf("netlink portid reused: %d", newNL.Pid)
	}
}

// The Execute tests run against NETLINK_ROUTE: NETLINK_RDMA needs the RDMA core
// module, and Execute depends only on the netlink framing, so rtnetlink
// replies, and replies a peer socket injects, stand in for RDMA ones.

// dialRoute opens a NETLINK_ROUTE socket wrapped in an nlConn.
func dialRoute(t *testing.T) *nlConn {
	t.Helper()
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_ROUTE)
	if err != nil {
		t.Skipf("NETLINK_ROUTE unavailable: %v", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		_ = unix.Close(fd)
		t.Fatalf("bind: %v", err)
	}
	sa, err := unix.Getsockname(fd)
	if err != nil {
		_ = unix.Close(fd)
		t.Fatalf("getsockname: %v", err)
	}
	c := &nlConn{fd: fd, pid: sa.(*unix.SockaddrNetlink).Pid}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// reconnected reports whether Execute replaced c's NETLINK_ROUTE socket
// through reconnectLocked (which dials NETLINK_RDMA, or leaves fd -1 when
// that fails).
func reconnected(t *testing.T, c *nlConn) bool {
	t.Helper()
	if c.fd < 0 {
		return true
	}
	proto, err := unix.GetsockoptInt(c.fd, unix.SOL_SOCKET, unix.SO_PROTOCOL)
	if err != nil {
		t.Fatalf("SO_PROTOCOL: %v", err)
	}
	return proto != unix.NETLINK_ROUTE
}

func ifinfomsg(index int32) []byte {
	b := make([]byte, 16)
	binary.LittleEndian.PutUint32(b[4:8], uint32(index))
	return b
}

func executeTimeout(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestExecuteRouteKernelReplies(t *testing.T) {
	t.Parallel()

	lo, err := net.InterfaceByName("lo")
	if err != nil {
		t.Skipf("no loopback interface: %v", err)
	}

	cases := []struct {
		name          string
		typ           uint16
		flags         uint16
		payload       []byte
		budget        *DumpBudget
		wantMsgs      int // -1: at least one
		wantErr       error
		wantErrText   string
		wantReconnect bool
	}{
		{
			name:     "doit without ack returns the reply",
			typ:      unix.RTM_GETLINK,
			flags:    nlmFRequest,
			payload:  ifinfomsg(int32(lo.Index)),
			wantMsgs: 1,
		},
		{
			name:     "doit with ack waits for the ack",
			typ:      unix.RTM_GETLINK,
			flags:    nlmFRequest | nlmFAck,
			payload:  ifinfomsg(int32(lo.Index)),
			wantMsgs: 1,
		},
		{
			name:     "doit ignores the dump budget",
			typ:      unix.RTM_GETLINK,
			flags:    nlmFRequest | nlmFAck,
			payload:  ifinfomsg(int32(lo.Index)),
			budget:   &DumpBudget{MaxBytes: 1, MaxMsgs: 1},
			wantMsgs: 1,
		},
		{
			name:     "dump collects until done",
			typ:      unix.RTM_GETLINK,
			flags:    nlmFRequest | nlmFDump,
			payload:  ifinfomsg(0),
			wantMsgs: -1,
		},
		{
			name:          "dump over the byte budget",
			typ:           unix.RTM_GETLINK,
			flags:         nlmFRequest | nlmFDump,
			payload:       ifinfomsg(0),
			budget:        &DumpBudget{MaxBytes: 1},
			wantErr:       ErrDumpOverflow,
			wantReconnect: true,
		},
		{
			name:        "error ack",
			typ:         nlType(cmdGet), // above RTM_MAX: rtnetlink answers EOPNOTSUPP
			flags:       nlmFRequest,
			wantErr:     unix.EOPNOTSUPP,
			wantErrText: "rdma netlink: operation not supported",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			c := dialRoute(t)
			ctx := executeTimeout(t)
			if tc.budget != nil {
				ctx = withDumpBudget(ctx, *tc.budget)
			}
			got, err := c.Execute(ctx, tc.typ, tc.flags, tc.payload)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				if tc.wantErrText != "" && err.Error() != tc.wantErrText {
					t.Fatalf("err text = %q, want %q", err.Error(), tc.wantErrText)
				}
				if got != nil {
					t.Fatalf("got %d messages with error, want nil", len(got))
				}
			} else {
				if err != nil {
					t.Fatalf("Execute: %v", err)
				}
				if tc.wantMsgs >= 0 && len(got) != tc.wantMsgs {
					t.Fatalf("got %d messages, want %d", len(got), tc.wantMsgs)
				}
				if tc.wantMsgs < 0 && len(got) == 0 {
					t.Fatal("got no messages, want at least one")
				}
			}
			if r := reconnected(t, c); r != tc.wantReconnect {
				t.Fatalf("reconnected = %v, want %v", r, tc.wantReconnect)
			}
			if tc.wantReconnect {
				return
			}
			// Nothing of this request may be left for the next one to read.
			next, err := c.Execute(ctx, unix.RTM_GETLINK, nlmFRequest|nlmFAck, ifinfomsg(int32(lo.Index)))
			if err != nil || len(next) != 1 {
				t.Fatalf("follow-up Execute: %d messages, err %v", len(next), err)
			}
		})
	}
}

func TestExecuteCanceledContextSendsNothing(t *testing.T) {
	t.Parallel()

	c := dialRoute(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Execute(ctx, unix.RTM_GETLINK, nlmFRequest|nlmFDump, ifinfomsg(0)); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if got := c.seq.Load(); got != 0 {
		t.Fatalf("seq = %d, want 0 (nothing sent)", got)
	}
	if reconnected(t, c) {
		t.Fatal("canceled request replaced the socket")
	}
}

// injectedMsg is one netlink message a peer socket queues on the conn.
type injectedMsg struct {
	typ   uint16
	flags uint16
	seq   int64 // offset from the request's sequence number
	data  []byte
}

func injData(seqOff int64, data string) injectedMsg {
	return injectedMsg{typ: nlType(cmdStatGet), seq: seqOff, data: []byte(data)}
}

func injErr(seqOff int64, errno int32) injectedMsg {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], uint32(-errno))
	return injectedMsg{typ: unix.NLMSG_ERROR, seq: seqOff, data: b[:]}
}

func injDone(seqOff int64, payload []byte) injectedMsg {
	return injectedMsg{typ: unix.NLMSG_DONE, seq: seqOff, data: payload}
}

func encodeInjected(seq uint32, msgs []injectedMsg) []byte {
	var buf []byte
	for _, m := range msgs {
		total := nlmsgHdrLen + len(m.data)
		b := make([]byte, nlaAlign(total))
		binary.LittleEndian.PutUint32(b[0:4], uint32(total))
		binary.LittleEndian.PutUint16(b[4:6], m.typ)
		binary.LittleEndian.PutUint16(b[6:8], m.flags)
		binary.LittleEndian.PutUint32(b[8:12], uint32(int64(seq)+m.seq))
		copy(b[nlmsgHdrLen:], m.data)
		buf = append(buf, b...)
	}
	return buf
}

// inject queues each batch on c as one datagram, so each batch is one recv in
// Execute. Unicast between user netlink sockets needs CAP_NET_ADMIN.
func inject(t *testing.T, c *nlConn, seq uint32, batches [][]injectedMsg) {
	t.Helper()
	peer, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_ROUTE)
	if err != nil {
		t.Fatalf("peer socket: %v", err)
	}
	defer unix.Close(peer)
	if err := unix.Bind(peer, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		t.Fatalf("bind peer: %v", err)
	}
	for _, batch := range batches {
		err := unix.Sendto(peer, encodeInjected(seq, batch), 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK, Pid: c.pid})
		if errors.Is(err, unix.EPERM) {
			t.Skip("injecting netlink replies needs CAP_NET_ADMIN")
		}
		if err != nil {
			t.Fatalf("inject: %v", err)
		}
	}
}

func TestExecuteInjectedReplies(t *testing.T) {
	t.Parallel()

	// No case sets NLM_F_REQUEST, so rtnetlink does not process the request.
	// It still ACKs a request with NLM_F_ACK, behind the injected messages
	// and with the request's sequence number, so a case that ends before
	// that ACK must inject something after its end to catch a reader that
	// does not stop.
	cases := []struct {
		name          string
		flags         uint16
		budget        *DumpBudget
		batches       [][]injectedMsg
		want          []string
		wantErr       error
		wantErrText   string
		wantReconnect bool
	}{
		{
			name:  "doit returns the first own data message",
			flags: 0,
			batches: [][]injectedMsg{
				{injData(100, "foreign"), injData(0, "a"), injData(0, "b")},
			},
			want: []string{"a"},
		},
		{
			name:  "doit with ack collects data across datagrams until the ack",
			flags: nlmFAck,
			batches: [][]injectedMsg{
				{injData(0, "a")},
				{injData(-1, "stale"), injData(0, "b"), injErr(0, 0), injData(0, "late")},
			},
			want: []string{"a", "b"},
		},
		{
			name:    "doit ack without data",
			flags:   0,
			batches: [][]injectedMsg{{injErr(0, 0)}},
			want:    nil,
		},
		{
			name:  "doit ends at done",
			flags: nlmFAck,
			batches: [][]injectedMsg{
				{injData(0, "a"), injDone(0, nil)},
				{injData(0, "late")},
			},
			want: []string{"a"},
		},
		{
			name:        "doit error ack",
			flags:       nlmFAck,
			batches:     [][]injectedMsg{{injData(0, "a"), injErr(0, int32(unix.ENOENT))}},
			wantErr:     unix.ENOENT,
			wantErrText: "rdma netlink: no such file or directory",
		},
		{
			name:        "doit truncated error message",
			flags:       0,
			batches:     [][]injectedMsg{{{typ: unix.NLMSG_ERROR, data: []byte{1, 2}}}},
			wantErrText: "truncated netlink error message",
		},
		{
			name:    "doit overrun",
			flags:   0,
			batches: [][]injectedMsg{{{typ: nlmsgOverrun}}},
			wantErr: ErrDumpInterrupted,
		},
		{
			name:    "doit ignores the dump budget",
			flags:   0,
			budget:  &DumpBudget{MaxBytes: 1, MaxMsgs: 1},
			batches: [][]injectedMsg{{injData(0, "a"), injData(0, "b")}},
			want:    []string{"a"},
		},
		{
			name:  "dump collects across datagrams and skips acks until done",
			flags: nlmFDump,
			batches: [][]injectedMsg{
				{injData(0, "a"), injData(7, "foreign"), injData(0, "b")},
				{injErr(0, 0), injData(0, "c")},
				{injDone(0, make([]byte, 4)), injData(0, "after done")},
			},
			want: []string{"a", "b", "c"},
		},
		{
			name:    "dump done without payload",
			flags:   nlmFDump,
			batches: [][]injectedMsg{{injData(0, "a"), injDone(0, nil)}},
			want:    []string{"a"},
		},
		{
			name:    "dump done with a short payload",
			flags:   nlmFDump,
			batches: [][]injectedMsg{{injData(0, "a"), injDone(0, []byte{0xff, 0xff, 0xff})}},
			want:    []string{"a"},
		},
		{
			name:  "dump ignores foreign interrupted and overrun messages",
			flags: nlmFDump,
			batches: [][]injectedMsg{{
				{typ: nlType(cmdStatGet), flags: nlmFDumpIntr, seq: 1, data: []byte("x")},
				{typ: nlmsgOverrun, seq: 2},
				injData(0, "a"),
				injDone(0, nil),
			}},
			want: []string{"a"},
		},
		{
			name:          "dump done carrying an error",
			flags:         nlmFDump,
			batches:       [][]injectedMsg{{injData(0, "a"), injDone(0, errnoPayload(int32(unix.EIO)))}},
			wantErr:       unix.EIO,
			wantErrText:   "rdma netlink dump: input/output error",
			wantReconnect: true,
		},
		{
			name:          "dump error ack",
			flags:         nlmFDump | nlmFAck,
			batches:       [][]injectedMsg{{injData(0, "a"), injErr(0, int32(unix.EPERM))}},
			wantErr:       unix.EPERM,
			wantErrText:   "rdma netlink: operation not permitted",
			wantReconnect: true,
		},
		{
			name:          "dump truncated error message",
			flags:         nlmFDump,
			batches:       [][]injectedMsg{{{typ: unix.NLMSG_ERROR, data: []byte{1}}}},
			wantErrText:   "truncated netlink error message",
			wantReconnect: true,
		},
		{
			name:  "dump interrupted",
			flags: nlmFDump,
			batches: [][]injectedMsg{
				{injData(0, "a")},
				{{typ: nlType(cmdStatGet), flags: nlmFDumpIntr, data: []byte("b")}},
			},
			wantErr:       ErrDumpInterrupted,
			wantReconnect: true,
		},
		{
			name:          "dump overrun",
			flags:         nlmFDump,
			batches:       [][]injectedMsg{{injData(0, "a"), {typ: nlmsgOverrun}}},
			wantErr:       ErrDumpInterrupted,
			wantReconnect: true,
		},
		{
			name:   "dump budget counts own data messages of a datagram before reading it",
			flags:  nlmFDump,
			budget: &DumpBudget{MaxMsgs: 2},
			batches: [][]injectedMsg{
				{injData(0, "a"), injData(0, "b"), injData(0, "c"), injDone(0, nil)},
			},
			wantErr:       ErrDumpOverflow,
			wantErrText:   "rdma netlink dump exceeded receive budget: 3 messages",
			wantReconnect: true,
		},
		{
			name:   "dump budget accumulates messages across datagrams",
			flags:  nlmFDump,
			budget: &DumpBudget{MaxMsgs: 2},
			batches: [][]injectedMsg{
				{injData(0, "a")},
				{injData(0, "b")},
				{injData(0, "c"), injDone(0, nil)},
			},
			wantErr:       ErrDumpOverflow,
			wantErrText:   "rdma netlink dump exceeded receive budget: 3 messages",
			wantReconnect: true,
		},
		{
			name:   "dump budget skips control and foreign messages",
			flags:  nlmFDump,
			budget: &DumpBudget{MaxMsgs: 2},
			batches: [][]injectedMsg{
				{injData(3, "x"), injData(4, "y"), injData(0, "a"), injErr(0, 0)},
				{injData(0, "b"), injDone(0, nil)},
			},
			want: []string{"a", "b"},
		},
		{
			name:   "dump budget accumulates bytes across datagrams",
			flags:  nlmFDump,
			budget: &DumpBudget{MaxBytes: 40},
			batches: [][]injectedMsg{
				{injData(0, "a")},
				{injData(0, "b"), injDone(0, nil)},
			},
			wantErr:       ErrDumpOverflow,
			wantErrText:   "rdma netlink dump exceeded receive budget: 56 bytes",
			wantReconnect: true,
		},
		{
			name:   "dump within the budget",
			flags:  nlmFDump,
			budget: &DumpBudget{MaxBytes: 56, MaxMsgs: 2},
			batches: [][]injectedMsg{
				{injData(0, "a")},
				{injData(0, "b"), injDone(0, nil)},
			},
			want: []string{"a", "b"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			c := dialRoute(t)
			seq := c.seq.Load() + 1
			inject(t, c, seq, tc.batches)

			ctx := executeTimeout(t)
			if tc.budget != nil {
				ctx = withDumpBudget(ctx, *tc.budget)
			}
			got, err := c.Execute(ctx, nlType(cmdStatGet), tc.flags, nil)
			switch {
			case tc.wantErr != nil || tc.wantErrText != "":
				if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				if tc.wantErrText != "" && (err == nil || err.Error() != tc.wantErrText) {
					t.Fatalf("err = %v, want %q", err, tc.wantErrText)
				}
				if got != nil {
					t.Fatalf("got %q with error, want nil", got)
				}
			case err != nil:
				t.Fatalf("Execute: %v", err)
			default:
				if len(got) != len(tc.want) {
					t.Fatalf("got %q, want %q", got, tc.want)
				}
				for i := range got {
					if string(got[i]) != tc.want[i] {
						t.Fatalf("got %q, want %q", got, tc.want)
					}
				}
			}
			if r := reconnected(t, c); r != tc.wantReconnect {
				t.Fatalf("reconnected = %v, want %v", r, tc.wantReconnect)
			}
		})
	}
}

func errnoPayload(errno int32) []byte {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], uint32(-errno))
	return b[:]
}

func TestExecuteRouteLocalFailures(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		closed        bool
		smallSndbuf   bool   // netlink rejects messages larger than the send buffer
		flags         uint16 // without NLM_F_REQUEST rtnetlink stays silent
		payload       []byte
		recvTimeout   time.Duration // instead of a context deadline, which can expire before the recv
		wantErr       error
		wantErrText   string
		wantReconnect bool
	}{
		{
			name:        "closed socket fails before sending",
			closed:      true,
			flags:       nlmFDump,
			wantErr:     unix.EBADF,
			wantErrText: "set netlink recv timeout: bad file descriptor",
		},
		{
			name:        "send failure does not replace the socket",
			smallSndbuf: true,
			flags:       nlmFRequest | nlmFDump,
			payload:     make([]byte, 64<<10),
			wantErr:     unix.EMSGSIZE,
			wantErrText: "send rdma netlink: message too long",
		},
		{
			name:        "doit recv timeout",
			flags:       0,
			recvTimeout: 50 * time.Millisecond,
			wantErr:     unix.EAGAIN,
			wantErrText: "recv rdma netlink: resource temporarily unavailable",
		},
		{
			name:          "dump recv timeout replaces the socket",
			flags:         nlmFDump,
			recvTimeout:   50 * time.Millisecond,
			wantErr:       unix.EAGAIN,
			wantErrText:   "recv rdma netlink: resource temporarily unavailable",
			wantReconnect: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			c := dialRoute(t)
			if tc.closed {
				if err := c.Close(); err != nil {
					t.Fatalf("Close: %v", err)
				}
			}
			if tc.smallSndbuf {
				if err := unix.SetsockoptInt(c.fd, unix.SOL_SOCKET, unix.SO_SNDBUF, 4096); err != nil {
					t.Fatalf("SO_SNDBUF: %v", err)
				}
			}
			ctx := executeTimeout(t)
			if tc.recvTimeout > 0 {
				c.recvTimeout = tc.recvTimeout
				ctx = context.Background()
			}
			got, err := c.Execute(ctx, unix.RTM_GETLINK, tc.flags, tc.payload)
			if !errors.Is(err, tc.wantErr) || err.Error() != tc.wantErrText {
				t.Fatalf("err = %v, want %q", err, tc.wantErrText)
			}
			if got != nil {
				t.Fatalf("got %d messages with error, want nil", len(got))
			}
			if tc.closed {
				return
			}
			if r := reconnected(t, c); r != tc.wantReconnect {
				t.Fatalf("reconnected = %v, want %v", r, tc.wantReconnect)
			}
		})
	}
}

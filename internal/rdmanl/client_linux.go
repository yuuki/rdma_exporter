//go:build linux

package rdmanl

import (
	"context"
	"encoding/binary"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

const (
	netlinkReadBufSize = 65536
	defaultRecvTimeout = 5 * time.Second
)

type nlConn struct {
	fd  int
	pid uint32
	seq atomic.Uint32
	mu  sync.Mutex

	// recvTimeout caps each receive; zero means defaultRecvTimeout. Tests
	// shorten it to reach the receive timeout without a context deadline,
	// which could expire before Execute receives.
	recvTimeout time.Duration
}

// New opens a NETLINK_RDMA socket for optional counter queries.
func New() (*Provider, error) {
	conn, err := dial()
	if err != nil {
		return nil, err
	}
	return newProvider(conn), nil
}

func dial() (*nlConn, error) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_RDMA)
	if err != nil {
		return nil, fmt.Errorf("open NETLINK_RDMA socket: %w", err)
	}

	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("bind NETLINK_RDMA socket: %w", err)
	}

	sa, err := unix.Getsockname(fd)
	if err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("getsockname NETLINK_RDMA socket: %w", err)
	}
	nlsa, ok := sa.(*unix.SockaddrNetlink)
	if !ok {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("unexpected NETLINK_RDMA sockaddr type %T", sa)
	}

	return &nlConn{fd: fd, pid: nlsa.Pid}, nil
}

func (c *nlConn) Execute(ctx context.Context, typ uint16, flags uint16, payload []byte) ([][]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if err := setConnDeadline(c.fd, ctx, c.recvTimeout); err != nil {
		return nil, err
	}

	seq := c.seq.Add(1)
	if err := c.send(seq, typ, flags, payload); err != nil {
		return nil, err
	}

	replies := newReplyStream(ctx, seq, flags)
	completed := false
	if replies.dump {
		// An unfinished multipart dump leaves replies queued on the socket;
		// replace it so the next request cannot read them.
		defer func() {
			if !completed {
				_ = c.reconnectLocked()
			}
		}()
	}

	out, err := c.readReplies(ctx, replies)
	completed = err == nil
	return out, err
}

// replyStream collects the replies to one request sent on an nlConn.
type replyStream struct {
	seq        uint32
	dump       bool
	wantAck    bool
	budget     DumpBudget
	haveBudget bool
	out        [][]byte
	nBytes     int
}

func newReplyStream(ctx context.Context, seq uint32, flags uint16) *replyStream {
	budget, haveBudget := dumpBudgetFrom(ctx)
	return &replyStream{
		seq:        seq,
		dump:       flags&nlmFDump != 0,
		wantAck:    flags&nlmFAck != 0,
		budget:     budget,
		haveBudget: haveBudget,
	}
}

// readReplies receives datagrams until the request is answered and returns
// its data messages.
func (c *nlConn) readReplies(ctx context.Context, s *replyStream) ([][]byte, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		msgs, n, err := c.recv()
		if err != nil {
			return nil, err
		}
		if err := s.checkBudget(n, msgs); err != nil {
			return nil, err
		}
		done, err := s.consume(msgs)
		if err != nil {
			return nil, err
		}
		if done {
			return s.out, nil
		}
	}
}

// checkBudget adds a datagram of n bytes to the dump's totals and fails once
// they exceed the dump budget. The datagram's data messages count before they
// are read.
func (s *replyStream) checkBudget(n int, msgs []nlMsg) error {
	s.nBytes += n
	if !s.dump || !s.haveBudget {
		return nil
	}
	return s.budget.check(s.nBytes, len(s.out)+s.countData(msgs))
}

// countData counts the data messages in msgs that answer this request.
func (s *replyStream) countData(msgs []nlMsg) int {
	n := 0
	for _, msg := range msgs {
		if msg.seq == s.seq && isDataMsg(msg.typ) {
			n++
		}
	}
	return n
}

func isDataMsg(typ uint16) bool {
	return typ != unix.NLMSG_ERROR && typ != unix.NLMSG_DONE && typ != nlmsgOverrun
}

// consume handles one datagram's messages in order and reports whether the
// request is complete. Messages of other requests are ignored.
func (s *replyStream) consume(msgs []nlMsg) (bool, error) {
	for _, msg := range msgs {
		if msg.seq != s.seq {
			continue
		}
		done, err := s.handle(msg)
		if err != nil {
			return false, err
		}
		if done {
			return true, nil
		}
	}
	return false, nil
}

// handle processes one message of this request and reports whether it
// completes the request.
func (s *replyStream) handle(msg nlMsg) (bool, error) {
	if msg.typ == nlmsgOverrun || msg.flags&nlmFDumpIntr != 0 {
		return false, ErrDumpInterrupted
	}
	switch msg.typ {
	case unix.NLMSG_ERROR:
		return s.handleAck(msg.data)
	case unix.NLMSG_DONE:
		if err := dumpDoneError(msg.data); err != nil {
			return false, err
		}
		return true, nil
	default:
		s.out = append(s.out, msg.data)
		return !s.dump && !s.wantAck, nil
	}
}

// handleAck handles NLMSG_ERROR. A zero code is an ACK, which completes a
// non-dump request; a dump only completes at NLMSG_DONE.
func (s *replyStream) handleAck(data []byte) (bool, error) {
	code, err := nlErrorCode(data)
	if err != nil {
		return false, err
	}
	if code != 0 {
		return false, fmt.Errorf("rdma netlink: %w", unix.Errno(code))
	}
	return !s.dump, nil
}

// dumpDoneError returns the error NLMSG_DONE carries, if its payload has one.
func dumpDoneError(data []byte) error {
	if len(data) < 4 {
		return nil
	}
	code, err := nlErrorCode(data)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("rdma netlink dump: %w", unix.Errno(code))
	}
	return nil
}

func (c *nlConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeLocked()
}

func (c *nlConn) closeLocked() error {
	if c.fd < 0 {
		return nil
	}
	err := unix.Close(c.fd)
	c.fd = -1
	return err
}

// reconnectLocked drops an incomplete multipart dump by replacing the socket.
func (c *nlConn) reconnectLocked() error {
	_ = c.closeLocked()
	n, err := dial()
	if err != nil {
		return err
	}
	c.fd = n.fd
	c.pid = n.pid
	return nil
}

func (c *nlConn) send(seq uint32, typ uint16, flags uint16, payload []byte) error {
	total := nlmsgHdrLen + len(payload)
	buf := make([]byte, nlaAlign(total))
	binary.LittleEndian.PutUint32(buf[0:4], uint32(total))
	binary.LittleEndian.PutUint16(buf[4:6], typ)
	binary.LittleEndian.PutUint16(buf[6:8], flags)
	binary.LittleEndian.PutUint32(buf[8:12], seq)
	binary.LittleEndian.PutUint32(buf[12:16], c.pid)
	copy(buf[nlmsgHdrLen:], payload)

	if err := unix.Sendto(c.fd, buf[:total], 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return fmt.Errorf("send rdma netlink: %w", err)
	}
	return nil
}

func (c *nlConn) recv() ([]nlMsg, int, error) {
	buf := make([]byte, netlinkReadBufSize)
	n, _, err := unix.Recvfrom(c.fd, buf, 0)
	if err != nil {
		return nil, 0, fmt.Errorf("recv rdma netlink: %w", err)
	}
	msgs, err := parseNlMsgs(buf[:n])
	if err != nil {
		return nil, n, err
	}
	return msgs, n, nil
}

// setConnDeadline sets the socket's receive timeout to limit (zero means
// defaultRecvTimeout), or to the time left before ctx's deadline if shorter.
func setConnDeadline(fd int, ctx context.Context, limit time.Duration) error {
	timeout := defaultRecvTimeout
	if limit > 0 {
		timeout = limit
	}
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return context.DeadlineExceeded
		}
		if remaining < timeout {
			timeout = remaining
		}
	}
	tv := unix.NsecToTimeval(timeout.Nanoseconds())
	if err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv); err != nil {
		return fmt.Errorf("set netlink recv timeout: %w", err)
	}
	return nil
}

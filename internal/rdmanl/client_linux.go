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

	if err := setConnDeadline(c.fd, ctx); err != nil {
		return nil, err
	}

	seq := c.seq.Add(1)
	if err := c.send(seq, typ, flags, payload); err != nil {
		return nil, err
	}

	dump := flags&nlmFDump != 0
	wantAck := flags&nlmFAck != 0
	budget, haveBudget := dumpBudgetFrom(ctx)
	var out [][]byte
	nBytes := 0
	dumpDone := !dump
	if dump {
		defer func() {
			if !dumpDone {
				_ = c.reconnectLocked()
			}
		}()
	}

	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		msgs, n, err := c.recv()
		if err != nil {
			return nil, err
		}
		nBytes += n
		pendingData := 0
		for _, msg := range msgs {
			if msg.seq == seq && msg.typ != unix.NLMSG_ERROR && msg.typ != unix.NLMSG_DONE && msg.typ != nlmsgOverrun {
				pendingData++
			}
		}
		if dump && haveBudget {
			if err := budget.check(nBytes, len(out)+pendingData); err != nil {
				return nil, err
			}
		}
		for _, msg := range msgs {
			if msg.seq != seq {
				continue
			}
			if msg.typ == nlmsgOverrun {
				return nil, ErrDumpInterrupted
			}
			if msg.flags&nlmFDumpIntr != 0 {
				return nil, ErrDumpInterrupted
			}
			switch msg.typ {
			case unix.NLMSG_ERROR:
				code, err := nlErrorCode(msg.data)
				if err != nil {
					return nil, err
				}
				if code != 0 {
					return nil, fmt.Errorf("rdma netlink: %w", unix.Errno(code))
				}
				if !dump {
					dumpDone = true
					return out, nil
				}
			case unix.NLMSG_DONE:
				if len(msg.data) >= 4 {
					code, err := nlErrorCode(msg.data)
					if err != nil {
						return nil, err
					}
					if code != 0 {
						return nil, fmt.Errorf("rdma netlink dump: %w", unix.Errno(code))
					}
				}
				dumpDone = true
				return out, nil
			default:
				out = append(out, msg.data)
				if !dump && !wantAck {
					dumpDone = true
					return out, nil
				}
			}
		}
	}
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

func setConnDeadline(fd int, ctx context.Context) error {
	timeout := defaultRecvTimeout
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

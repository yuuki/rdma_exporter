//go:build linux

package rdmanl

import (
	"testing"

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

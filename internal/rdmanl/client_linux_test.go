//go:build linux

package rdmanl

import "testing"

func TestReconnectLockedReplacesSocket(t *testing.T) {
	c, err := dial()
	if err != nil {
		t.Skipf("NETLINK_RDMA unavailable: %v", err)
	}
	oldFD := c.fd
	if err := c.reconnectLocked(); err != nil {
		t.Fatalf("reconnectLocked: %v", err)
	}
	if c.fd < 0 || c.fd == oldFD {
		t.Fatalf("fd=%d old=%d", c.fd, oldFD)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

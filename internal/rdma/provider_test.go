package rdma

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestSysfsProviderDevicesFromCustomRoot(t *testing.T) {
	t.Parallel()

	root := filepath.Join("testdata", "sysfs", "basic")
	provider := NewSysfsProvider()
	provider.SetSysfsRoot(root)

	devices, err := provider.Devices(context.Background())
	if err != nil {
		t.Fatalf("Devices returned error: %v", err)
	}

	if len(devices) != 1 {
		t.Fatalf("expected 1 device, got %d", len(devices))
	}

	device := devices[0]
	if device.Name != "mlx5_0" {
		t.Fatalf("unexpected device name %q", device.Name)
	}
	if len(device.Ports) != 2 {
		t.Fatalf("expected 2 ports, got %d", len(device.Ports))
	}

	port1 := device.Ports[0]
	if port1.ID != 1 {
		t.Fatalf("expected port ID 1, got %d", port1.ID)
	}
	if got := port1.Stats["port_xmit_data"]; got != 123 {
		t.Fatalf("expected port_xmit_data=123, got %d", got)
	}
	if got := port1.HwStats["symbol_errors"]; got != 11 {
		t.Fatalf("expected symbol_errors=11, got %d", got)
	}
	if want, got := "InfiniBand", port1.Attributes.LinkLayer; got != want {
		t.Fatalf("expected link layer %q, got %q", want, got)
	}
	if want, got := "ACTIVE", port1.Attributes.State; got != want {
		t.Fatalf("expected state %q, got %q", want, got)
	}
	if want, got := "LinkUp", port1.Attributes.PhysState; got != want {
		t.Fatalf("expected phys_state %q, got %q", want, got)
	}
	if want, got := "4X", port1.Attributes.LinkWidth; got != want {
		t.Fatalf("expected link_width %q, got %q", want, got)
	}
	if want, got := "100 Gb/sec", port1.Attributes.LinkSpeed; got != want {
		t.Fatalf("expected link_speed %q, got %q", want, got)
	}

	port2 := device.Ports[1]
	if port2.ID != 2 {
		t.Fatalf("expected port ID 2, got %d", port2.ID)
	}
	if port2.Attributes.State != "DOWN" {
		t.Fatalf("expected state DOWN, got %q", port2.Attributes.State)
	}
	if port2.HwStats != nil && len(port2.HwStats) != 0 {
		t.Fatalf("expected empty hw counters, got %v", port2.HwStats)
	}
}

func TestSysfsProviderDevicesContextCanceled(t *testing.T) {
	provider := NewSysfsProvider()
	provider.SetSysfsRoot(filepath.Join("testdata", "sysfs", "basic"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := provider.Devices(ctx)
	if err == nil {
		t.Fatalf("expected context cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
}

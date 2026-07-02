package rdma

import (
	"context"
	"errors"
	"os"
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
	if want, got := "LINK_UP", port1.Attributes.PhysState; got != want {
		t.Fatalf("expected phys_state %q, got %q", want, got)
	}
	if want, got := "4X", port1.Attributes.LinkWidth; got != want {
		t.Fatalf("expected link_width %q, got %q", want, got)
	}
	if want, got := "100 Gb/sec", port1.Attributes.LinkSpeed; got != want {
		t.Fatalf("expected link_speed %q, got %q", want, got)
	}
	if want, got := "ens1f0np0", port1.Attributes.NetDev; got != want {
		t.Fatalf("expected netdev %q, got %q", want, got)
	}

	port2 := device.Ports[1]
	if port2.ID != 2 {
		t.Fatalf("expected port ID 2, got %d", port2.ID)
	}
	if port2.Attributes.State != "DOWN" {
		t.Fatalf("expected state DOWN, got %q", port2.Attributes.State)
	}
	if got := port2.Attributes.NetDev; got != "" {
		t.Fatalf("expected empty netdev, got %q", got)
	}
	if port2.HwStats != nil && len(port2.HwStats) != 0 {
		t.Fatalf("expected empty hw counters, got %v", port2.HwStats)
	}
}

func TestSysfsProviderDevicesFromSymlinkRoot(t *testing.T) {
	t.Parallel()

	root := filepath.Join("testdata", "sysfs", "symlink")
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
	if got := port1.Stats["port_xmit_data"]; got != 123 {
		t.Fatalf("expected port_xmit_data=123, got %d", got)
	}
	if got := port1.HwStats["symbol_errors"]; got != 11 {
		t.Fatalf("expected symbol_errors=11, got %d", got)
	}
	if want, got := "ens1f0np0", port1.Attributes.NetDev; got != want {
		t.Fatalf("expected netdev %q, got %q", want, got)
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

func TestSysfsProviderVFDetection(t *testing.T) {
	t.Parallel()

	root := filepath.Join("testdata", "sysfs", "vf")
	provider := NewSysfsProvider()
	provider.SetSysfsRoot(root)

	devices, err := provider.Devices(context.Background())
	if err != nil {
		t.Fatalf("Devices returned error: %v", err)
	}

	if len(devices) != 2 {
		t.Fatalf("expected 2 devices (1 PF + 1 VF), got %d", len(devices))
	}

	// devices are returned in directory-read order (alphabetical): mlx5_0, mlx5_4
	pf := devices[0]
	vf := devices[1]

	// --- PF assertions ---
	if pf.Name != "mlx5_0" {
		t.Errorf("PF name: want mlx5_0, got %q", pf.Name)
	}
	if pf.PCIAddr != "0000:1a:00.0" {
		t.Errorf("PF PCIAddr: want 0000:1a:00.0, got %q", pf.PCIAddr)
	}
	if pf.IsVF {
		t.Errorf("PF IsVF: want false, got true")
	}
	if pf.PFDevice != "" {
		t.Errorf("PF PFDevice: want empty, got %q", pf.PFDevice)
	}

	// --- VF assertions ---
	if vf.Name != "mlx5_4" {
		t.Errorf("VF name: want mlx5_4, got %q", vf.Name)
	}
	if vf.PCIAddr != "0000:1a:00.1" {
		t.Errorf("VF PCIAddr: want 0000:1a:00.1, got %q", vf.PCIAddr)
	}
	if !vf.IsVF {
		t.Errorf("VF IsVF: want true, got false")
	}
	if vf.PFDevice != "mlx5_0" {
		t.Errorf("VF PFDevice: want mlx5_0, got %q", vf.PFDevice)
	}
}

func TestSetExcludeDevices(t *testing.T) {
	t.Parallel()

	provider := NewSysfsProvider()

	devices := []string{"mlx5_0", "mlx5_1", " mlx5_2 "}
	provider.SetExcludeDevices(devices)

	tests := []struct {
		device   string
		excluded bool
	}{
		{"mlx5_0", true},
		{"mlx5_1", true},
		{" mlx5_2 ", true}, // exact match with spaces
		{"mlx5_3", false},
		{"", false},
	}

	for _, tt := range tests {
		got := provider.isExcluded(tt.device)
		if got != tt.excluded {
			t.Errorf("isExcluded(%q) = %v, want %v", tt.device, got, tt.excluded)
		}
	}
}

func TestSysfsProvider_ReadCounterDirSkipsUnreadableCounters(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeCounter(t, dir, "port_xmit_data", "123")
	writeCounter(t, dir, "port_rcv_data", "456")
	// Non-numeric contents are skipped rather than failing the whole port.
	writeCounter(t, dir, "not_a_number", "N/A")
	// A permission-denied read is one of the tolerated errors, like the EINVAL
	// that mlx5 returns for some legacy counters.
	unreadable := writeCounter(t, dir, "unreadable", "789")
	if err := os.Chmod(unreadable, 0); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	// Non-regular entries such as subdirectories are ignored.
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	provider := NewSysfsProvider()
	counters, err := provider.readCounterDir(dir)
	if err != nil {
		t.Fatalf("readCounterDir returned error: %v", err)
	}

	if got := counters["port_xmit_data"]; got != 123 {
		t.Fatalf("expected port_xmit_data=123, got %d", got)
	}
	if got := counters["port_rcv_data"]; got != 456 {
		t.Fatalf("expected port_rcv_data=456, got %d", got)
	}
	if _, ok := counters["not_a_number"]; ok {
		t.Fatalf("expected non-numeric counter to be skipped")
	}
	if _, ok := counters["subdir"]; ok {
		t.Fatalf("expected subdirectory to be ignored")
	}
	// Running as root bypasses mode bits, so the read never fails there.
	if os.Geteuid() != 0 {
		if _, ok := counters["unreadable"]; ok {
			t.Fatalf("expected unreadable counter to be skipped")
		}
	}
}

func writeCounter(t *testing.T, dir, name, contents string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write counter %s: %v", name, err)
	}
	return path
}

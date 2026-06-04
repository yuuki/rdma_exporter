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

func TestSysfsProviderExtraHwCountersPaths(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	hwDir := filepath.Join(root, "class", "infiniband", "roce_rail0", "hw_counters")
	if err := os.MkdirAll(hwDir, 0o755); err != nil {
		t.Fatalf("create hw counter dir: %v", err)
	}
	portHwDir := filepath.Join(root, "extra", "mlx5_10", "ports", "1", "hw_counters")
	if err := os.MkdirAll(portHwDir, 0o755); err != nil {
		t.Fatalf("create port hw counter dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hwDir, "np_cnp_sent"), []byte("123\n"), 0o644); err != nil {
		t.Fatalf("write np_cnp_sent: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hwDir, "rp_cnp_handled"), []byte("456\n"), 0o644); err != nil {
		t.Fatalf("write rp_cnp_handled: %v", err)
	}
	if err := os.WriteFile(filepath.Join(portHwDir, "np_ecn_marked_roce_packets"), []byte("789\n"), 0o644); err != nil {
		t.Fatalf("write np_ecn_marked_roce_packets: %v", err)
	}

	provider := NewSysfsProvider()
	provider.SetSysfsRoot(root)
	provider.SetExtraHwCountersPaths([]string{
		filepath.Join(root, "class", "infiniband", "roce_rail*", "hw_counters"),
		filepath.Join(root, "extra", "mlx5_1*", "ports", "1", "hw_counters"),
	})

	devices, err := provider.Devices(context.Background())
	if err != nil {
		t.Fatalf("Devices returned error: %v", err)
	}
	if len(devices) != 2 {
		t.Fatalf("expected 2 devices, got %d", len(devices))
	}

	device := devices[0]
	if device.Name != "roce_rail0" {
		t.Fatalf("unexpected device name %q", device.Name)
	}
	if len(device.Ports) != 1 {
		t.Fatalf("expected 1 synthetic port, got %d", len(device.Ports))
	}
	port := device.Ports[0]
	if port.ID != 0 {
		t.Fatalf("expected synthetic port ID 0, got %d", port.ID)
	}
	if got := port.HwStats["np_cnp_sent"]; got != 123 {
		t.Fatalf("expected np_cnp_sent=123, got %d", got)
	}
	if got := port.HwStats["rp_cnp_handled"]; got != 456 {
		t.Fatalf("expected rp_cnp_handled=456, got %d", got)
	}

	device = devices[1]
	if device.Name != "mlx5_10" {
		t.Fatalf("unexpected device name %q", device.Name)
	}
	if len(device.Ports) != 1 {
		t.Fatalf("expected 1 port, got %d", len(device.Ports))
	}
	port = device.Ports[0]
	if port.ID != 1 {
		t.Fatalf("expected port ID 1, got %d", port.ID)
	}
	if got := port.HwStats["np_ecn_marked_roce_packets"]; got != 789 {
		t.Fatalf("expected np_ecn_marked_roce_packets=789, got %d", got)
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

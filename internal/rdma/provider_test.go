package rdma

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
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
	if len(port2.HwStats) != 0 {
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
	if !pf.HasSRIOV {
		t.Errorf("PF HasSRIOV: want true, got false")
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
	if vf.HasSRIOV {
		t.Errorf("host VF HasSRIOV: want false, got true")
	}
}

func TestSysfsProviderHasSRIOVRequiresTotalVFsFile(t *testing.T) {
	t.Parallel()

	root := filepath.Join("testdata", "sysfs", "guest_vf")
	provider := NewSysfsProvider()
	provider.SetSysfsRoot(root)

	devices, err := provider.Devices(context.Background())
	if err != nil {
		t.Fatalf("Devices returned error: %v", err)
	}
	if len(devices) != 1 {
		t.Fatalf("expected 1 device without sriov_totalvfs, got %d", len(devices))
	}
	dev := devices[0]
	if dev.PCIAddr != "0000:00:04.0" {
		t.Errorf("PCIAddr: want 0000:00:04.0, got %q", dev.PCIAddr)
	}
	if dev.IsVF {
		t.Errorf("IsVF: want false (no physfn), got true")
	}
	if dev.HasSRIOV {
		t.Errorf("HasSRIOV: want false when sriov_totalvfs is absent, got true")
	}
}

func TestPCIHasSRIOVTotalVFsZeroStillTrue(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	devDir := filepath.Join(root, busPCIDevicesDir, "0000:1a:00.0")
	if err := os.MkdirAll(devDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(devDir, sriovTotalVFsName), []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !pciHasSRIOVTotalVFs(root, "0000:1a:00.0") {
		t.Fatal("sriov_totalvfs=0 must still set HasSRIOV")
	}
	if pciHasSRIOVTotalVFs(root, "../evil") {
		t.Fatal("non-BDF pciAddr must not be joined")
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

func TestSysfsProvider_PortWithoutCountersDir(t *testing.T) {
	t.Parallel()

	// EFA devices (rdmap*) expose only hw_counters/, no counters/ directory.
	// A missing counters/ dir must not fail the whole scrape.
	root := filepath.Join("testdata", "sysfs", "efa")
	provider := NewSysfsProvider()
	provider.SetSysfsRoot(root)

	devices, err := provider.Devices(context.Background())
	if err != nil {
		t.Fatalf("Devices returned error: %v", err)
	}
	if len(devices) != 1 {
		t.Fatalf("expected 1 device, got %d", len(devices))
	}

	port := devices[0].Ports[0]
	if len(port.Stats) != 0 {
		t.Fatalf("expected empty counters, got %v", port.Stats)
	}
	if got := port.HwStats["rx_bytes"]; got != 123 {
		t.Fatalf("expected hw_counters rx_bytes=123, got %d", got)
	}
	if got := port.HwStats["tx_bytes"]; got != 456 {
		t.Fatalf("expected hw_counters tx_bytes=456, got %d", got)
	}
}

func TestSysfsProviderDevicesScenarios(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		scenario string
		exclude  []string
		want     []Device
		wantErr  string
		wantIs   error
	}{
		{
			// A missing class directory yields nil, an empty one an empty slice.
			name:     "missing class directory",
			scenario: "does_not_exist",
			want:     nil,
		},
		{
			name:     "class directory without devices",
			scenario: "empty",
			want:     []Device{},
		},
		{
			// Directories and symlinks to directories are devices; regular
			// files, symlinks to files, dangling symlinks and excluded names
			// are skipped. Ports come in name order (os.ReadDir sorts), not
			// port number order. They skip regular files, symlinks (even to a
			// port directory), non-numeric names and numbers outside 32 bits
			// (mlx5_7's "4294967297"), and read their attributes
			// from the directory of the parsed number, so "05" gets the
			// counters of ports/05 but the attributes of ports/5. A ports
			// directory with only skipped entries yields an empty slice.
			name:     "skips entries that are not devices or ports",
			scenario: "entries",
			exclude:  []string{"mlx5_6"},
			want: []Device{
				{
					Name: "mlx5_0",
					Ports: []Port{
						{ID: 5, Stats: map[string]uint64{"port_rcv_data": 5}},
						{
							ID:      1,
							Stats:   map[string]uint64{"port_xmit_data": 7},
							HwStats: map[string]uint64{"out_of_sequence": 3},
							Attributes: PortAttributes{
								LinkLayer: "Ethernet",
								State:     "ACTIVE",
								PhysState: "LINK_UP",
								LinkWidth: "4X",
								LinkSpeed: "100 Gb/sec",
							},
						},
						{ID: 2, Attributes: PortAttributes{LinkLayer: "InfiniBand"}},
					},
				},
				{
					Name:  "mlx5_1",
					Ports: []Port{{ID: 1, Attributes: PortAttributes{LinkLayer: "Ethernet"}}},
				},
				{Name: "mlx5_5"},
				{Name: "mlx5_7", Ports: []Port{}},
			},
		},
		{
			name:     "class path is not a directory",
			scenario: "class_not_dir",
			wantErr:  "open testdata/sysfs/class_not_dir/class/infiniband: " + syscall.ENOTDIR.Error(),
			wantIs:   syscall.ENOTDIR,
		},
		{
			name:     "ports path is not a directory",
			scenario: "ports_not_dir",
			wantErr:  "collect ports for mlx5_1: open testdata/sysfs/ports_not_dir/class/infiniband/mlx5_1/ports: " + syscall.ENOTDIR.Error(),
			wantIs:   syscall.ENOTDIR,
		},
		{
			name:     "counters path is not a directory",
			scenario: "counters_not_dir",
			wantErr:  "collect ports for mlx5_0: read counters for mlx5_0 port 1: open testdata/sysfs/counters_not_dir/class/infiniband/mlx5_0/ports/1/counters: " + syscall.ENOTDIR.Error(),
			wantIs:   syscall.ENOTDIR,
		},
		{
			name:     "hw_counters path is not a directory",
			scenario: "hw_counters_not_dir",
			wantErr:  "collect ports for mlx5_0: read hw counters for mlx5_0 port 1: open testdata/sysfs/hw_counters_not_dir/class/infiniband/mlx5_0/ports/1/hw_counters: " + syscall.ENOTDIR.Error(),
			wantIs:   syscall.ENOTDIR,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			provider := NewSysfsProvider()
			provider.SetSysfsRoot(filepath.Join("testdata", "sysfs", tc.scenario))
			provider.SetExcludeDevices(tc.exclude)

			got, err := provider.Devices(context.Background())
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Devices returned error: %v", err)
				}
			} else {
				if err == nil || err.Error() != tc.wantErr || !errors.Is(err, tc.wantIs) {
					t.Fatalf("Devices error = %v, want %q wrapping %v", err, tc.wantErr, tc.wantIs)
				}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Devices = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// The scan checks the context before each class and port entry, so it stops
// even when only skipped entries are left.
func TestSysfsProviderScanStopsOnCanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	provider := NewSysfsProvider()

	cases := []struct {
		name    string
		scan    func() (any, error)
		wantErr error
	}{
		{
			name: "devices with only skipped entries",
			scan: func() (any, error) {
				return provider.devicesFromRoot(ctx, filepath.Join("testdata", "sysfs", "empty"))
			},
			wantErr: context.Canceled,
		},
		{
			name: "devices with a missing class directory",
			scan: func() (any, error) {
				return provider.devicesFromRoot(ctx, filepath.Join("testdata", "sysfs", "does_not_exist"))
			},
		},
		{
			name: "ports",
			scan: func() (any, error) {
				// The first entry, "05", is a port.
				return provider.portsFromRoot(ctx, filepath.Join("testdata", "sysfs", "entries"), "mlx5_0")
			},
			wantErr: context.Canceled,
		},
		{
			name: "ports with only skipped entries",
			scan: func() (any, error) {
				return provider.portsFromRoot(ctx, filepath.Join("testdata", "sysfs", "entries"), "mlx5_7")
			},
			wantErr: context.Canceled,
		},
		{
			name: "device without a ports directory",
			scan: func() (any, error) {
				return provider.portsFromRoot(ctx, filepath.Join("testdata", "sysfs", "entries"), "mlx5_5")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := tc.scan()
			if !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if !reflect.ValueOf(got).IsNil() {
				t.Fatalf("got %#v, want nil", got)
			}
		})
	}
}

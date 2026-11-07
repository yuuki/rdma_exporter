package rdma

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// BenchmarkSysfsProviderDevices reads a sysfs tree the size of a busy host:
// eight dual-port devices, each port with 24 counters, 40 hw_counters, its
// attributes and a netdev.
func BenchmarkSysfsProviderDevices(b *testing.B) {
	root := b.TempDir()
	write := func(path, contents string) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	for d := range 8 {
		for p := 1; p <= 2; p++ {
			port := filepath.Join(root, classInfinibandPath, fmt.Sprintf("mlx5_%d", d), portsDirName, fmt.Sprint(p))
			for i := range 24 {
				write(filepath.Join(port, countersDirName, fmt.Sprintf("counter_%d", i)), fmt.Sprintf("%d\n", i*1000))
			}
			for i := range 40 {
				write(filepath.Join(port, hwCountersDirName, fmt.Sprintf("hw_counter_%d", i)), fmt.Sprintf("%d\n", i))
			}
			write(filepath.Join(port, linkLayerFile), "Ethernet\n")
			write(filepath.Join(port, stateFile), "4: ACTIVE\n")
			write(filepath.Join(port, physStateFile), "5: LinkUp\n")
			write(filepath.Join(port, linkWidthFile), "4X\n")
			write(filepath.Join(port, rateFile), "100 Gb/sec (4X EDR)\n")
			write(filepath.Join(port, gidAttrsDirName, ndevsDirName, "0"), fmt.Sprintf("ens%df%dnp%d\n", d, p-1, p-1))
		}
	}

	provider := NewSysfsProvider()
	provider.SetSysfsRoot(root)
	b.ReportAllocs()
	for b.Loop() {
		devices, err := provider.Devices(context.Background())
		if err != nil || len(devices) != 8 {
			b.Fatalf("Devices: %d devices, err %v", len(devices), err)
		}
	}
}

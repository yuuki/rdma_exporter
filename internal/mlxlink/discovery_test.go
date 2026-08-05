package mlxlink

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func newDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func fixtureRoot(scenario string) string {
	return filepath.Join("testdata", "sysfs", scenario)
}

// newSysfsTree builds a minimal class/infiniband tree for cases that cannot be
// committed as a fixture (missing symlinks, unusual port numbering).
func newSysfsTree(t *testing.T, device string, ports ...string) string {
	t.Helper()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, classInfinibandPath, device), 0o755); err != nil {
		t.Fatalf("create device dir: %v", err)
	}
	for _, port := range ports {
		dir := filepath.Join(root, classInfinibandPath, device, portsDirName, port)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create port dir: %v", err)
		}
	}
	return root
}

func TestSysfsDiscovery_EnumeratesPhysicalFunction(t *testing.T) {
	t.Parallel()

	discovery := NewSysfsDiscovery(fixtureRoot("basic"), nil, newDiscardLogger())

	targets, err := discovery.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}

	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d (%v)", len(targets), targets)
	}
	want := Target{Device: "mlx5_0", Port: "1", PCIAddr: "0000:1a:00.0", NetDev: "ens1f0np0"}
	if targets[0] != want {
		t.Fatalf("expected target %+v, got %+v", want, targets[0])
	}
}

func TestSysfsDiscovery_SkipsVirtualFunctions(t *testing.T) {
	t.Parallel()

	discovery := NewSysfsDiscovery(fixtureRoot("vf"), nil, newDiscardLogger())

	targets, err := discovery.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}

	if len(targets) != 1 {
		t.Fatalf("expected only the physical function, got %d targets (%v)", len(targets), targets)
	}
	if targets[0].Device != "mlx5_0" {
		t.Fatalf("expected mlx5_0, got %q", targets[0].Device)
	}
	if targets[0].PCIAddr != "0000:1a:00.0" {
		t.Fatalf("expected PF pci address, got %q", targets[0].PCIAddr)
	}
}

func TestSysfsDiscovery_ExcludesDevices(t *testing.T) {
	t.Parallel()

	discovery := NewSysfsDiscovery(fixtureRoot("basic"), []string{"mlx5_0"}, newDiscardLogger())

	targets, err := discovery.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if len(targets) != 0 {
		t.Fatalf("expected excluded device to be skipped, got %v", targets)
	}
}

func TestSysfsDiscovery_MissingClassDirectory(t *testing.T) {
	t.Parallel()

	discovery := NewSysfsDiscovery(t.TempDir(), nil, newDiscardLogger())

	targets, err := discovery.Discover(context.Background())
	if err != nil {
		t.Fatalf("expected no error for missing class directory, got %v", err)
	}
	if targets != nil {
		t.Fatalf("expected nil targets, got %v", targets)
	}
}

func TestSysfsDiscovery_MissingDeviceSymlink(t *testing.T) {
	t.Parallel()

	root := newSysfsTree(t, "mlx5_0", "1")
	discovery := NewSysfsDiscovery(root, nil, newDiscardLogger())

	targets, err := discovery.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}

	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d (%v)", len(targets), targets)
	}
	want := Target{Device: "mlx5_0", Port: "1"}
	if targets[0] != want {
		t.Fatalf("expected target %+v, got %+v", want, targets[0])
	}
}

func TestSysfsDiscovery_MultiPortUsesLowestPort(t *testing.T) {
	t.Parallel()

	discovery := NewSysfsDiscovery(fixtureRoot("multiport"), nil, newDiscardLogger())

	targets, err := discovery.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}

	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d (%v)", len(targets), targets)
	}
	want := Target{Device: "mlx5_0", Port: "1", PCIAddr: "0000:5e:00.0", NetDev: "ens1f0np0"}
	if targets[0] != want {
		t.Fatalf("expected target %+v, got %+v", want, targets[0])
	}
}

func TestSysfsDiscovery_LowestPortIsNumeric(t *testing.T) {
	t.Parallel()

	// "10" sorts before "2" lexicographically; the lowest port must be 2.
	root := newSysfsTree(t, "mlx5_0", "10", "2")
	discovery := NewSysfsDiscovery(root, nil, newDiscardLogger())

	targets, err := discovery.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}

	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d (%v)", len(targets), targets)
	}
	if targets[0].Port != "2" {
		t.Fatalf("expected port 2, got %q", targets[0].Port)
	}
}

func TestSysfsDiscovery_SkipsDeviceWithoutPorts(t *testing.T) {
	t.Parallel()

	root := newSysfsTree(t, "mlx5_0")
	discovery := NewSysfsDiscovery(root, nil, newDiscardLogger())

	targets, err := discovery.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if len(targets) != 0 {
		t.Fatalf("expected device without ports to be skipped, got %v", targets)
	}
}

func TestSysfsDiscovery_ClassEntrySymlink(t *testing.T) {
	t.Parallel()

	// The real sysfs exposes class entries as symlinks into /sys/devices, and
	// non-directory entries must be ignored.
	root := t.TempDir()
	deviceDir := filepath.Join(root, "devices", "mlx5_0")
	if err := os.MkdirAll(filepath.Join(deviceDir, portsDirName, "1"), 0o755); err != nil {
		t.Fatalf("create device dir: %v", err)
	}
	classDir := filepath.Join(root, classInfinibandPath)
	if err := os.MkdirAll(classDir, 0o755); err != nil {
		t.Fatalf("create class dir: %v", err)
	}
	if err := os.Symlink(deviceDir, filepath.Join(classDir, "mlx5_0")); err != nil {
		t.Fatalf("create class symlink: %v", err)
	}
	if err := os.WriteFile(filepath.Join(classDir, "notadevice"), nil, 0o644); err != nil {
		t.Fatalf("create stray file: %v", err)
	}

	discovery := NewSysfsDiscovery(root, nil, newDiscardLogger())

	targets, err := discovery.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}

	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d (%v)", len(targets), targets)
	}
	want := Target{Device: "mlx5_0", Port: "1"}
	if targets[0] != want {
		t.Fatalf("expected target %+v, got %+v", want, targets[0])
	}
}

func TestSysfsDiscovery_ClassSymlinkWithDeviceLink(t *testing.T) {
	t.Parallel()

	// Mirrors the real sysfs shape: the class entry is a relative symlink into
	// /sys/devices and the device link below it points back at the PCI node.
	root := t.TempDir()
	pciDir := filepath.Join(root, "devices", "pci0000:1a", "0000:1a:00.0")
	deviceDir := filepath.Join(pciDir, "infiniband", "mlx5_0")
	ndevsDir := filepath.Join(deviceDir, portsDirName, "1", gidAttrsDirName, ndevsDirName)
	if err := os.MkdirAll(ndevsDir, 0o755); err != nil {
		t.Fatalf("create device tree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ndevsDir, "0"), []byte("ens1f0np0\n"), 0o644); err != nil {
		t.Fatalf("write ndevs entry: %v", err)
	}
	if err := os.Symlink("../../../0000:1a:00.0", filepath.Join(deviceDir, deviceLinkName)); err != nil {
		t.Fatalf("create device symlink: %v", err)
	}
	classDir := filepath.Join(root, classInfinibandPath)
	if err := os.MkdirAll(classDir, 0o755); err != nil {
		t.Fatalf("create class dir: %v", err)
	}
	target := filepath.Join("..", "..", "devices", "pci0000:1a", "0000:1a:00.0", "infiniband", "mlx5_0")
	if err := os.Symlink(target, filepath.Join(classDir, "mlx5_0")); err != nil {
		t.Fatalf("create class symlink: %v", err)
	}

	discovery := NewSysfsDiscovery(root, nil, newDiscardLogger())

	targets, err := discovery.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}

	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d (%v)", len(targets), targets)
	}
	want := Target{Device: "mlx5_0", Port: "1", PCIAddr: "0000:1a:00.0", NetDev: "ens1f0np0"}
	if targets[0] != want {
		t.Fatalf("expected target %+v, got %+v", want, targets[0])
	}
}

func TestSysfsDiscovery_ContextCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	discovery := NewSysfsDiscovery(fixtureRoot("basic"), nil, newDiscardLogger())
	if _, err := discovery.Discover(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

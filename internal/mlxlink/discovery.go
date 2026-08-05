package mlxlink

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

const (
	defaultSysfsRoot = "/sys"

	classInfinibandPath = "class/infiniband"
	portsDirName        = "ports"
	gidAttrsDirName     = "gid_attrs"
	ndevsDirName        = "ndevs"
	deviceLinkName      = "device"
	physfnLinkName      = "physfn"
)

// SysfsDiscovery enumerates mlxlink collection targets by walking
// /sys/class/infiniband. SR-IOV virtual functions are skipped because mlxlink
// addresses the physical function that owns the port.
type SysfsDiscovery struct {
	root    string
	exclude map[string]bool
	logger  *slog.Logger

	mu sync.Mutex
	// warnedMultiPort keeps the multi-port limitation warning to once per
	// device: Discover runs on every sweep and the condition is static.
	warnedMultiPort map[string]bool
}

// NewSysfsDiscovery returns a discovery rooted at the given sysfs mount point.
// An empty root falls back to /sys and a nil logger to slog.Default.
func NewSysfsDiscovery(root string, exclude []string, logger *slog.Logger) *SysfsDiscovery {
	if root == "" {
		root = defaultSysfsRoot
	}
	if logger == nil {
		logger = slog.Default()
	}

	excluded := make(map[string]bool, len(exclude))
	for _, device := range exclude {
		excluded[device] = true
	}

	return &SysfsDiscovery{
		root:            filepath.Clean(root),
		exclude:         excluded,
		logger:          logger,
		warnedMultiPort: make(map[string]bool),
	}
}

// Discover returns one target per physical function. A host without the
// infiniband class directory has no RDMA devices, which is not an error.
// Devices whose PCI address cannot be resolved are still returned with an empty
// PCIAddr so that mlxlink data is not lost over a missing label.
func (d *SysfsDiscovery) Discover(ctx context.Context) ([]Target, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	classDir := filepath.Join(d.root, classInfinibandPath)
	entries, err := os.ReadDir(classDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	targets := make([]Target, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		name := entry.Name()
		if d.exclude[name] {
			continue
		}
		if !isDeviceDir(classDir, entry) {
			continue
		}

		devicePath := filepath.Join(classDir, name, deviceLinkName)
		pciAddr, isVF := readPCIInfo(devicePath)
		if isVF {
			d.logger.Debug("skipping SR-IOV virtual function", "device", name, "pci_addr", pciAddr)
			continue
		}

		port, count := lowestPort(filepath.Join(classDir, name, portsDirName))
		if count == 0 {
			d.logger.Warn("skipping device without ports", "device", name, "pci_addr", pciAddr)
			continue
		}
		if count > 1 {
			d.warnMultiPort(name, port, count)
		}

		portDir := filepath.Join(classDir, name, portsDirName, strconv.Itoa(port))
		targets = append(targets, Target{
			Device:  name,
			Port:    strconv.Itoa(port),
			PCIAddr: pciAddr,
			NetDev:  readPortNetDev(portDir),
		})
	}
	return targets, nil
}

// warnMultiPort reports the single-port limitation once per device: mlxlink is
// invoked as "-d <device>" without "-p", so only the lowest port is collected.
func (d *SysfsDiscovery) warnMultiPort(device string, port, count int) {
	d.mu.Lock()
	warned := d.warnedMultiPort[device]
	d.warnedMultiPort[device] = true
	d.mu.Unlock()

	if warned {
		return
	}
	d.logger.Warn("device exposes multiple ports; collecting the lowest one only",
		"device", device, "port", port, "ports", count)
}

// isDeviceDir reports whether the class entry is a device directory, accepting
// the symlinks the real sysfs uses as well as the plain directories fixtures
// use.
func isDeviceDir(classDir string, entry fs.DirEntry) bool {
	if entry.IsDir() {
		return true
	}
	if entry.Type()&fs.ModeSymlink == 0 {
		return false
	}
	info, err := os.Stat(filepath.Join(classDir, entry.Name()))
	return err == nil && info.IsDir()
}

// readPCIInfo resolves the PCI address from the device symlink target and
// detects SR-IOV virtual functions, which carry a physfn link back to their
// physical function. An unresolvable symlink yields an empty address.
func readPCIInfo(devicePath string) (pciAddr string, isVF bool) {
	if link, err := os.Readlink(devicePath); err == nil {
		pciAddr = filepath.Base(link)
	}
	if _, err := os.Lstat(filepath.Join(devicePath, physfnLinkName)); err == nil {
		return pciAddr, true
	}
	return pciAddr, false
}

// lowestPort returns the smallest numeric port under dir and how many ports
// exist. A count of zero means no usable port was found.
func lowestPort(dir string) (port, count int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		if count == 0 || id < port {
			port = id
		}
		count++
	}
	return port, count
}

// readPortNetDev returns the netdev bound to the port, or an empty string when
// sysfs does not expose one (pure InfiniBand ports have none).
func readPortNetDev(portDir string) string {
	ndevsPath := filepath.Join(portDir, gidAttrsDirName, ndevsDirName)
	entries, err := os.ReadDir(ndevsPath)
	if err != nil {
		return ""
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(ndevsPath, entry.Name()))
		if err != nil {
			continue
		}
		if value := strings.TrimSpace(string(data)); value != "" {
			return value
		}
	}
	return ""
}

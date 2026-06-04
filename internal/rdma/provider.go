package rdma

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"unicode"
)

const (
	defaultSysfsRoot = "/sys"

	classInfinibandPath = "class/infiniband"
	portsDirName        = "ports"
	gidAttrsDirName     = "gid_attrs"
	ndevsDirName        = "ndevs"
	countersDirName     = "counters"
	hwCountersDirName   = "hw_counters"
	linkLayerFile       = "link_layer"
	stateFile           = "state"
	physStateFile       = "phys_state"
	linkWidthFile       = "link_width"
	rateFile            = "rate"

	// SR-IOV PF/VF detection paths.
	deviceDirName    = "device"       // symlink under class/infiniband/<dev>/device → PCI addr
	physfnLinkName   = "physfn"       // symlink present only on VFs: device/physfn → PF PCI addr
	infinibandSubDir = "infiniband"   // under /sys/bus/pci/devices/<pci>/infiniband/
	busPCIDevicesDir = "bus/pci/devices" // /sys/bus/pci/devices/<pci>/
)

var (
	// ref. https://codebrowser.dev/linux/linux/include/rdma/ib_verbs.h.html#ib_port_state
	portStateNames = map[int]string{
		0: "NOP",
		1: "DOWN",
		2: "INIT",
		3: "ARMED",
		4: "ACTIVE",
		5: "ACTIVE_DEFER",
	}
	// ref. https://codebrowser.dev/linux/linux/include/rdma/ib_verbs.h.html#ib_port_phys_state
	portPhysStateNames = map[int]string{
		1: "SLEEP",
		2: "POLLING",
		3: "DISABLED",
		4: "PORT_CONFIGURATION_TRAINING",
		5: "LINK_UP",
		6: "LINK_ERROR_RECOVERY",
		7: "PHY_TEST",
	}
)

// Provider exposes RDMA device information sourced from sysfs.
type Provider interface {
	Devices(ctx context.Context) ([]Device, error)
}

// Device represents a single RDMA Host Channel Adapter.
type Device struct {
	Name string
	// PCIAddr is the PCI bus address of this device (e.g. "0000:1a:00.1").
	// Derived from the device symlink under /sys/class/infiniband/<dev>/device.
	// Empty string when the symlink cannot be resolved.
	PCIAddr string
	// IsVF is true when this device is a SR-IOV Virtual Function.
	IsVF bool
	// PFDevice is the IB device name of the parent Physical Function (e.g. "mlx5_0").
	// Only populated when IsVF is true; empty for PFs.
	PFDevice string
	Ports    []Port
}

// Port contains counters and metadata for a single HCA port.
type Port struct {
	ID         int
	Stats      map[string]uint64
	HwStats    map[string]uint64
	Attributes PortAttributes
}

// PortAttributes captures descriptive metadata exposed by sysfs.
type PortAttributes struct {
	LinkLayer string
	State     string
	PhysState string
	LinkWidth string
	LinkSpeed string
	NetDev    string
}

// SysfsProvider implements Provider backed by the node's sysfs.
type SysfsProvider struct {
	mu                   sync.RWMutex
	sysfsRoot            string
	extraHwCountersPaths []string
	excludeDevices       map[string]bool
}

// NewSysfsProvider returns a SysfsProvider using the default sysfs root.
func NewSysfsProvider() *SysfsProvider {
	return &SysfsProvider{sysfsRoot: defaultSysfsRoot}
}

// SetSysfsRoot overrides the root directory used to read sysfs.
// Passing an empty string resets the provider to the default.
func (p *SysfsProvider) SetSysfsRoot(root string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if root == "" {
		p.sysfsRoot = defaultSysfsRoot
		return
	}
	p.sysfsRoot = filepath.Clean(root)
}

// SetExtraHwCountersPaths 配置额外的 hw_counters 目录 glob。
//
// 默认采集逻辑只读取:
//
//	/sys/class/infiniband/<device>/ports/<port>/hw_counters
//
// 该参数用于补充采集不同环境下的非标准目录，例如:
//
//	/sys/class/infiniband/roce_rail*/hw_counters
//	/sys/class/infiniband/mlx5_1*/ports/1/hw_counters
func (p *SysfsProvider) SetExtraHwCountersPaths(paths []string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.extraHwCountersPaths = append([]string(nil), paths...)
}

// SetExcludeDevices configures which devices should be completely skipped.
func (p *SysfsProvider) SetExcludeDevices(devices []string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.excludeDevices = make(map[string]bool, len(devices))
	for _, dev := range devices {
		p.excludeDevices[dev] = true
	}
}

func (p *SysfsProvider) isExcluded(device string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.excludeDevices[device]
}

// Devices returns a snapshot of RDMA devices and associated ports.
func (p *SysfsProvider) Devices(ctx context.Context) ([]Device, error) {
	p.mu.RLock()
	root := p.sysfsRoot
	extraHwCountersPaths := append([]string(nil), p.extraHwCountersPaths...)
	p.mu.RUnlock()

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	devices, err := p.devicesFromRoot(ctx, root)
	if err != nil {
		return nil, err
	}
	return p.addExtraHwCounters(ctx, root, devices, extraHwCountersPaths)
}

func (p *SysfsProvider) deviceFromRoot(ctx context.Context, root, deviceName string) (Device, error) {
	if ctx.Err() != nil {
		return Device{}, ctx.Err()
	}

	// Resolve PCI address and PF/VF relationship via sysfs device symlink.
	devicePath := filepath.Join(root, classInfinibandPath, deviceName, deviceDirName)
	pciAddr, isVF, pfDevice := p.readDevicePCIInfo(root, devicePath)

	ports, err := p.portsFromRoot(ctx, root, deviceName)
	if err != nil {
		return Device{}, fmt.Errorf("collect ports for %s: %w", deviceName, err)
	}

	return Device{
		Name:     deviceName,
		PCIAddr:  pciAddr,
		IsVF:     isVF,
		PFDevice: pfDevice,
		Ports:    ports,
	}, nil
}

// readDevicePCIInfo returns the PCI address, whether the device is a SR-IOV VF,
// and (for VFs) the IB device name of the parent PF.
//
// Detection algorithm:
//  1. Read the device symlink to extract the PCI address
//     e.g. /sys/class/infiniband/mlx5_12/device → ../../../0000:1a:00.1
//  2. Check for the physfn symlink (present only on VFs)
//     e.g. /sys/class/infiniband/mlx5_12/device/physfn → ../0000:1a:00.0
//  3. For VFs, resolve the PF PCI address and look up its IB device name
//     e.g. /sys/bus/pci/devices/0000:1a:00.0/infiniband/ → mlx5_0
func (p *SysfsProvider) readDevicePCIInfo(root, devicePath string) (pciAddr string, isVF bool, pfDevice string) {
	// Step 1: extract PCI address from device symlink target basename.
	if link, err := os.Readlink(devicePath); err == nil {
		pciAddr = filepath.Base(link) // e.g. "0000:1a:00.1"
	}

	// Step 2: physfn symlink exists only on VFs.
	physfnPath := filepath.Join(devicePath, physfnLinkName)
	physfnLink, err := os.Readlink(physfnPath)
	if err != nil {
		// No physfn → this is a PF (or symlink resolution failed; treat as PF).
		return pciAddr, false, ""
	}

	// Step 3: resolve PF PCI address and find the corresponding IB device name.
	isVF = true
	pfPCIAddr := filepath.Base(physfnLink) // e.g. "0000:1a:00.0"

	// /sys/bus/pci/devices/<pfPCIAddr>/infiniband/ lists the PF IB device.
	pfIBDir := filepath.Join(root, busPCIDevicesDir, pfPCIAddr, infinibandSubDir)
	if entries, err := os.ReadDir(pfIBDir); err == nil && len(entries) > 0 {
		pfDevice = entries[0].Name() // e.g. "mlx5_0"
	}

	return pciAddr, true, pfDevice
}

func (p *SysfsProvider) devicesFromRoot(ctx context.Context, root string) ([]Device, error) {
	classDir := filepath.Join(root, classInfinibandPath)
	entries, err := os.ReadDir(classDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	devices := make([]Device, 0, len(entries))
	for _, entry := range entries {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		if !entry.IsDir() {
			if entry.Type()&fs.ModeSymlink == 0 {
				continue
			}
			info, err := os.Stat(filepath.Join(classDir, entry.Name()))
			if err != nil || !info.IsDir() {
				continue
			}
		}

		name := entry.Name()
		if p.isExcluded(name) {
			continue
		}

		device, err := p.deviceFromRoot(ctx, root, name)
		if err != nil {
			return nil, err
		}
		devices = append(devices, device)
	}
	return devices, nil
}

func (p *SysfsProvider) portsFromRoot(ctx context.Context, root, device string) ([]Port, error) {
	dir := filepath.Join(root, classInfinibandPath, device, portsDirName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	ports := make([]Port, 0, len(entries))
	for _, entry := range entries {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		if !entry.IsDir() {
			continue
		}
		portID, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}

		stats, err := p.readCounterDir(filepath.Join(dir, entry.Name(), countersDirName))
		if err != nil {
			return nil, fmt.Errorf("read counters for %s port %d: %w", device, portID, err)
		}
		hwStats, err := p.readCounterDir(filepath.Join(dir, entry.Name(), hwCountersDirName))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("read hw counters for %s port %d: %w", device, portID, err)
		}

		attr, err := p.readPortAttributes(root, device, portID)
		if err != nil {
			return nil, err
		}

		ports = append(ports, Port{
			ID:         portID,
			Stats:      stats,
			HwStats:    hwStats,
			Attributes: attr,
		})
	}
	return ports, nil
}

func (p *SysfsProvider) addExtraHwCounters(ctx context.Context, root string, devices []Device, patterns []string) ([]Device, error) {
	if len(patterns) == 0 {
		return devices, nil
	}

	deviceIndexes := make(map[string]int, len(devices))
	for i := range devices {
		deviceIndexes[devices[i].Name] = i
	}

	for _, pattern := range patterns {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		if !filepath.IsAbs(pattern) {
			pattern = filepath.Join(root, pattern)
		}
		matches, err := filepath.Glob(filepath.Clean(pattern))
		if err != nil {
			return nil, fmt.Errorf("expand extra hw counter path %q: %w", pattern, err)
		}

		for _, match := range matches {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}

			deviceName, portID, ok := parseHwCountersPath(match)
			if !ok || p.isExcluded(deviceName) {
				continue
			}

			hwStats, err := p.readCounterDir(match)
			if err != nil {
				return nil, fmt.Errorf("read extra hw counters for %s port %d: %w", deviceName, portID, err)
			}

			idx, exists := deviceIndexes[deviceName]
			if !exists {
				devices = append(devices, Device{Name: deviceName})
				idx = len(devices) - 1
				deviceIndexes[deviceName] = idx
			}
			mergeHwStats(&devices[idx], portID, hwStats)
		}
	}

	return devices, nil
}

// parseHwCountersPath 从 hw_counters 目录路径中解析 device 和 port。
//
// 支持两种路径:
//  1. device-level: <device>/hw_counters
//     用 port=0 表示没有端口层级，例如 bx 的 roce_rail0/hw_counters。
//  2. port-level: <device>/ports/<port>/hw_counters
//     保留真实端口号，例如 st2 的 mlx5_10/ports/1/hw_counters。
func parseHwCountersPath(path string) (string, int, bool) {
	clean := filepath.Clean(path)
	if filepath.Base(clean) != hwCountersDirName {
		return "", 0, false
	}

	parent := filepath.Dir(clean)
	if filepath.Base(filepath.Dir(parent)) == portsDirName {
		portID, err := strconv.Atoi(filepath.Base(parent))
		if err != nil {
			return "", 0, false
		}
		deviceName := filepath.Base(filepath.Dir(filepath.Dir(parent)))
		return deviceName, portID, deviceName != ""
	}

	deviceName := filepath.Base(parent)
	return deviceName, 0, deviceName != ""
}

// mergeHwStats 将额外采集到的 hw_counters 合并到对应 device/port。
// 如果默认采集已经包含该 port，则只补充/覆盖 HwStats；否则新增一个端口记录。
func mergeHwStats(device *Device, portID int, hwStats map[string]uint64) {
	for i := range device.Ports {
		if device.Ports[i].ID != portID {
			continue
		}
		if device.Ports[i].HwStats == nil {
			device.Ports[i].HwStats = make(map[string]uint64, len(hwStats))
		}
		for name, value := range hwStats {
			device.Ports[i].HwStats[name] = value
		}
		return
	}

	device.Ports = append(device.Ports, Port{
		ID:      portID,
		HwStats: hwStats,
	})
}

func (p *SysfsProvider) readPortAttributes(root, device string, port int) (PortAttributes, error) {
	portDir := filepath.Join(root, classInfinibandPath, device, portsDirName, strconv.Itoa(port))

	readRaw := func(name string) string {
		data, err := os.ReadFile(filepath.Join(portDir, name))
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(data))
	}

	read := func(name string) string {
		value := readRaw(name)
		if idx := strings.Index(value, "("); idx > 0 {
			value = strings.TrimSpace(value[:idx])
		}
		return value
	}

	state := normalizePortState(readRaw(stateFile), portStateNames)
	physState := normalizePortState(readRaw(physStateFile), portPhysStateNames)
	netDev := readPortNetDev(portDir)

	return PortAttributes{
		LinkLayer: read(linkLayerFile),
		State:     state,
		PhysState: physState,
		LinkWidth: read(linkWidthFile),
		LinkSpeed: read(rateFile),
		NetDev:    netDev,
	}, nil
}

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
		value := strings.TrimSpace(string(data))
		if value != "" {
			return value
		}
	}
	return ""
}

func normalizePortState(value string, names map[int]string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}

	if number, ok := extractFirstNumber(value); ok {
		if label, found := names[number]; found {
			return label
		}
	}

	if idx := strings.Index(value, ":"); idx >= 0 {
		if label := canonicalFromLabel(value[idx+1:], names); label != "" {
			return label
		}
	}

	if label := canonicalFromLabel(value, names); label != "" {
		return label
	}

	return value
}

func canonicalFromLabel(label string, names map[int]string) string {
	normalized := normalizeLabelKey(label)
	if normalized == "" {
		return ""
	}

	for _, name := range names {
		if normalizeLabelKey(name) == normalized {
			return name
		}
	}

	return ""
}

func normalizeLabelKey(label string) string {
	var b strings.Builder
	for _, r := range label {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(unicode.ToUpper(r))
		}
	}
	return b.String()
}

func extractFirstNumber(value string) (int, bool) {
	start := -1
	for i, r := range value {
		if r >= '0' && r <= '9' {
			if start == -1 {
				start = i
			}
			continue
		}
		if start != -1 {
			num, err := strconv.Atoi(value[start:i])
			if err == nil {
				return num, true
			}
			start = -1
		}
	}

	if start != -1 {
		num, err := strconv.Atoi(value[start:])
		if err == nil {
			return num, true
		}
	}

	return 0, false
}

func (p *SysfsProvider) readCounterDir(path string) (map[string]uint64, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	counters := make(map[string]uint64, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(path, entry.Name()))
		if err != nil {
			return nil, err
		}
		value, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse counter %s: %w", entry.Name(), err)
		}
		counters[entry.Name()] = value
	}
	return counters, nil
}

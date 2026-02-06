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
	Name  string
	Ports []Port
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
	mu             sync.RWMutex
	sysfsRoot      string
	excludeDevices map[string]bool
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
	p.mu.RUnlock()

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	return p.devicesFromRoot(ctx, root)
}

func (p *SysfsProvider) deviceFromRoot(ctx context.Context, root, deviceName string) (Device, error) {
	if ctx.Err() != nil {
		return Device{}, ctx.Err()
	}

	ports, err := p.portsFromRoot(ctx, root, deviceName)
	if err != nil {
		return Device{}, fmt.Errorf("collect ports for %s: %w", deviceName, err)
	}

	return Device{Name: deviceName, Ports: ports}, nil
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

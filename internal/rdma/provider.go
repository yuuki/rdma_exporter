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

	"github.com/Mellanox/rdmamap"
)

const (
	defaultSysfsRoot = "/sys"

	classInfinibandPath = "class/infiniband"
	portsDirName        = "ports"
	countersDirName     = "counters"
	hwCountersDirName   = "hw_counters"
	linkLayerFile       = "link_layer"
	stateFile           = "state"
	physStateFile       = "phys_state"
	linkWidthFile       = "link_width"
	rateFile            = "rate"
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
}

// SysfsProvider implements Provider backed by the node's sysfs.
type SysfsProvider struct {
	mu        sync.RWMutex
	sysfsRoot string
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

// Devices returns a snapshot of RDMA devices and associated ports.
func (p *SysfsProvider) Devices(ctx context.Context) ([]Device, error) {
	p.mu.RLock()
	root := p.sysfsRoot
	p.mu.RUnlock()

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	if root == defaultSysfsRoot {
		return p.devicesWithRdmamap(ctx)
	}
	return p.devicesFromRoot(ctx, root)
}

func (p *SysfsProvider) devicesWithRdmamap(ctx context.Context) ([]Device, error) {
	deviceNames := rdmamap.GetRdmaDeviceList()
	devices := make([]Device, 0, len(deviceNames))

	for _, name := range deviceNames {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		rdmaStats, err := rdmamap.GetRdmaSysfsAllPortsStats(name)
		if err != nil {
			return nil, fmt.Errorf("rdma stats for %s: %w", name, err)
		}

		ports := make([]Port, 0, len(rdmaStats.PortStats))
		for _, portStats := range rdmaStats.PortStats {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}

			portID := portStats.Port
			stats := make(map[string]uint64, len(portStats.Stats))
			for _, entry := range portStats.Stats {
				stats[entry.Name] = entry.Value
			}

			hwStats := make(map[string]uint64, len(portStats.HwStats))
			for _, entry := range portStats.HwStats {
				hwStats[entry.Name] = entry.Value
			}

			attr, err := p.readPortAttributes(defaultSysfsRoot, name, portID)
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

		devices = append(devices, Device{Name: name, Ports: ports})
	}
	return devices, nil
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
			continue
		}

		name := entry.Name()
		ports, err := p.portsFromRoot(ctx, root, name)
		if err != nil {
			return nil, fmt.Errorf("collect ports for %s: %w", name, err)
		}
		devices = append(devices, Device{Name: name, Ports: ports})
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

		stats, err := readCounterDir(filepath.Join(dir, entry.Name(), countersDirName))
		if err != nil {
			return nil, fmt.Errorf("read counters for %s port %d: %w", device, portID, err)
		}
		hwStats, err := readCounterDir(filepath.Join(dir, entry.Name(), hwCountersDirName))
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

	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(portDir, name))
		if err != nil {
			return ""
		}
		value := strings.TrimSpace(string(data))
		if idx := strings.Index(value, "("); idx > 0 {
			value = strings.TrimSpace(value[:idx])
		}
		return value
	}

	return PortAttributes{
		LinkLayer: read(linkLayerFile),
		State:     read(stateFile),
		PhysState: read(physStateFile),
		LinkWidth: read(linkWidthFile),
		LinkSpeed: read(rateFile),
	}, nil
}

func readCounterDir(path string) (map[string]uint64, error) {
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

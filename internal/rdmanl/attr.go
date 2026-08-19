package rdmanl

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// NETLINK_RDMA nldev identifiers from include/uapi/rdma/rdma_netlink.h.
// Numbers are ABI: do not reorder. STAT_GET_STATUS is Linux 5.16+.
const (
	nlNLDev = 5

	cmdGet           = 1  // RDMA_NLDEV_CMD_GET
	cmdStatGet       = 17 // RDMA_NLDEV_CMD_STAT_GET
	cmdStatGetStatus = 24 // RDMA_NLDEV_CMD_STAT_GET_STATUS

	nlGetClientShift = 10
)

const (
	attrDevIndex                = 1
	attrDevName                 = 2
	attrPortIndex               = 3
	attrResQP                   = 19
	attrResQPEntry              = 20
	attrResLQPN                 = 21
	attrResType                 = 26
	attrResPID                  = 28
	attrStatMode                = 74
	attrStatRes                 = 75
	attrStatAutoModeMask        = 76
	attrStatCounter             = 77
	attrStatCounterEntry        = 78
	attrStatCounterID           = 79
	attrStatHWCounters          = 80
	attrStatHWCounterEntry      = 81
	attrStatHWCounterEntryName  = 82
	attrStatHWCounterEntryValue = 83
	attrStatHWCounterIndex      = 94
	attrStatHWCounterDynamic    = 95
)

const (
	counterModeNone   = 0
	counterModeAuto   = 1
	counterModeManual = 2

	counterMaskQPType = 1
	counterMaskPID    = 1 << 1

	ibQPTCRC = 2
	ibQPTCUD = 4
)

const (
	nlaFNested   = 1 << 15
	nlaTypeMask  = 0x3fff
	nlaHeaderLen = 4
	nlaAlignTo   = 4
)

// OptionalCounter is an IB optional hardware counter (IB_STAT_FLAG_OPTIONAL).
// Static hw_counters that already appear in sysfs are never included.
type OptionalCounter struct {
	Name     string
	Enabled  bool
	Value    uint64
	HasValue bool
}

type deviceIdent struct {
	Name  string
	Index uint32
}

type hwCounter struct {
	Name     string
	Index    uint32
	Value    uint64
	Optional bool
	Enabled  bool
	HasValue bool
}

func nlType(cmd uint16) uint16 {
	return (nlNLDev << nlGetClientShift) | cmd
}

func nlaAlign(n int) int {
	return (n + nlaAlignTo - 1) & ^(nlaAlignTo - 1)
}

func putBytes(typ uint16, payload []byte) []byte {
	total := nlaHeaderLen + len(payload)
	aligned := nlaAlign(total)
	buf := make([]byte, aligned)
	binary.LittleEndian.PutUint16(buf[0:2], uint16(total))
	binary.LittleEndian.PutUint16(buf[2:4], typ)
	copy(buf[nlaHeaderLen:], payload)
	return buf
}

func putU8(typ uint16, v uint8) []byte {
	return putBytes(typ, []byte{v})
}

func putU32(typ uint16, v uint32) []byte {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	return putBytes(typ, b[:])
}

func putU64(typ uint16, v uint64) []byte {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], v)
	return putBytes(typ, b[:])
}

func putString(typ uint16, s string) []byte {
	return putBytes(typ, append([]byte(s), 0))
}

func nest(typ uint16, inner []byte) []byte {
	return putBytes(typ|nlaFNested, inner)
}

func parseAttrs(data []byte) ([]nlAttr, error) {
	var attrs []nlAttr
	off := 0
	for off < len(data) {
		if len(data)-off < nlaHeaderLen {
			return nil, fmt.Errorf("truncated netlink attribute header at offset %d", off)
		}
		nlaLen := int(binary.LittleEndian.Uint16(data[off : off+2]))
		nlaType := binary.LittleEndian.Uint16(data[off+2 : off+4])
		if nlaLen < nlaHeaderLen || off+nlaLen > len(data) {
			return nil, fmt.Errorf("invalid netlink attribute length %d at offset %d", nlaLen, off)
		}
		attrs = append(attrs, nlAttr{
			Type:   nlaType & nlaTypeMask,
			Nested: nlaType&nlaFNested != 0,
			Data:   data[off+nlaHeaderLen : off+nlaLen],
		})
		off += nlaAlign(nlaLen)
	}
	return attrs, nil
}

type nlAttr struct {
	Type   uint16
	Nested bool
	Data   []byte
}

func parseDevices(payload []byte) ([]deviceIdent, error) {
	attrs, err := parseAttrs(payload)
	if err != nil {
		return nil, err
	}
	var dev deviceIdent
	var seen bool
	for _, a := range attrs {
		switch a.Type {
		case attrDevIndex:
			if len(a.Data) < 4 {
				return nil, fmt.Errorf("short DEV_INDEX attribute")
			}
			dev.Index = binary.LittleEndian.Uint32(a.Data[:4])
			seen = true
		case attrDevName:
			dev.Name = nlaString(a.Data)
			seen = true
		}
	}
	if !seen {
		return nil, nil
	}
	if dev.Name == "" {
		return nil, fmt.Errorf("device attribute set missing name")
	}
	return []deviceIdent{dev}, nil
}

func parseHWCounters(payload []byte) ([]hwCounter, error) {
	attrs, err := parseAttrs(payload)
	if err != nil {
		return nil, err
	}
	for _, a := range attrs {
		if a.Type != attrStatHWCounters {
			continue
		}
		return parseHWCounterTable(a.Data)
	}
	return nil, nil
}

func parseHWCounterTable(data []byte) ([]hwCounter, error) {
	entries, err := parseAttrs(data)
	if err != nil {
		return nil, err
	}
	out := make([]hwCounter, 0, len(entries))
	for _, entry := range entries {
		if entry.Type != attrStatHWCounterEntry {
			continue
		}
		c, err := parseHWCounterEntry(entry.Data)
		if err != nil {
			return nil, err
		}
		if c.Name == "" {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

func parseHWCounterEntry(data []byte) (hwCounter, error) {
	attrs, err := parseAttrs(data)
	if err != nil {
		return hwCounter{}, err
	}
	var c hwCounter
	for _, a := range attrs {
		switch a.Type {
		case attrStatHWCounterEntryName:
			c.Name = nlaString(a.Data)
		case attrStatHWCounterIndex:
			if len(a.Data) < 4 {
				return hwCounter{}, fmt.Errorf("short STAT_HWCOUNTER_INDEX")
			}
			c.Index = binary.LittleEndian.Uint32(a.Data[:4])
		case attrStatHWCounterEntryValue:
			if len(a.Data) < 8 {
				return hwCounter{}, fmt.Errorf("short STAT_HWCOUNTER_ENTRY_VALUE")
			}
			c.Value = binary.LittleEndian.Uint64(a.Data[:8])
			c.HasValue = true
		case attrStatHWCounterDynamic:
			if len(a.Data) < 1 {
				return hwCounter{}, fmt.Errorf("short STAT_HWCOUNTER_DYNAMIC")
			}
			c.Optional = true
			c.Enabled = a.Data[0] != 0
		}
	}
	return c, nil
}

func nlaString(data []byte) string {
	return strings.TrimRight(string(data), "\x00")
}

func mergeOptionalCounters(status, values []hwCounter) []OptionalCounter {
	byName := make(map[string]hwCounter, len(values))
	for _, v := range values {
		if v.Name == "" {
			continue
		}
		byName[v.Name] = v
	}

	out := make([]OptionalCounter, 0, len(status))
	for _, s := range status {
		if !s.Optional || s.Name == "" {
			continue
		}
		c := OptionalCounter{Name: s.Name, Enabled: s.Enabled}
		if s.Enabled {
			if v, ok := byName[s.Name]; ok && v.HasValue {
				c.Value = v.Value
				c.HasValue = true
			}
		}
		out = append(out, c)
	}
	return out
}

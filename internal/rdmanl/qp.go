package rdmanl

import (
	"encoding/binary"
	"fmt"
)

// QPMode is the port-level QP counter bind mode from STAT_GET DOIT.
type QPMode struct {
	Mode     string
	MaskType bool
	MaskPID  bool
}

// QPSet is one live bound QP counter set from STAT_GET DUMP.
// Stats hold hw counter names present on the set. LQPN lists are discarded.
type QPSet struct {
	Mode   string
	QPType string
	HasPID bool
	Stats  map[string]uint64
}

// AutoType reports whether this set is auto-grouped by QP type only.
// type+pid and pid-only sets are excluded so {device,port,qp_type} stays unique.
func (s QPSet) AutoType() bool {
	return s.Mode == "auto" && s.QPType != "" && !s.HasPID
}

func parseQPMode(payload []byte) (QPMode, error) {
	attrs, err := parseAttrs(payload)
	if err != nil {
		return QPMode{}, err
	}
	var mode QPMode
	var rawMode uint32
	var haveMode bool
	for _, a := range attrs {
		switch a.Type {
		case attrStatMode:
			if len(a.Data) < 4 {
				return QPMode{}, fmt.Errorf("short STAT_MODE")
			}
			rawMode = binary.LittleEndian.Uint32(a.Data[:4])
			haveMode = true
		case attrStatAutoModeMask:
			if len(a.Data) < 4 {
				return QPMode{}, fmt.Errorf("short STAT_AUTO_MODE_MASK")
			}
			mask := binary.LittleEndian.Uint32(a.Data[:4])
			mode.MaskType = mask&counterMaskQPType != 0
			mode.MaskPID = mask&counterMaskPID != 0
		}
	}
	if !haveMode {
		return QPMode{}, fmt.Errorf("QP mode response missing STAT_MODE")
	}
	mode.Mode = counterModeName(rawMode)
	return mode, nil
}

func parseQPDump(msgs [][]byte) ([]QPSet, error) {
	var out []QPSet
	for _, msg := range msgs {
		sets, err := parseQPDumpMessage(msg)
		if err != nil {
			return nil, err
		}
		out = append(out, sets...)
	}
	return out, nil
}

func parseQPDumpMessage(payload []byte) ([]QPSet, error) {
	attrs, err := parseAttrs(payload)
	if err != nil {
		return nil, err
	}
	for _, a := range attrs {
		if a.Type != attrStatCounter {
			continue
		}
		return parseQPCounterTable(a.Data)
	}
	return nil, nil
}

func parseQPCounterTable(data []byte) ([]QPSet, error) {
	entries, err := parseAttrs(data)
	if err != nil {
		return nil, err
	}
	out := make([]QPSet, 0, len(entries))
	for _, entry := range entries {
		if entry.Type != attrStatCounterEntry {
			continue
		}
		set, err := parseQPSetEntry(entry.Data)
		if err != nil {
			return nil, err
		}
		out = append(out, set)
	}
	return out, nil
}

func parseQPSetEntry(data []byte) (QPSet, error) {
	attrs, err := parseAttrs(data)
	if err != nil {
		return QPSet{}, err
	}
	set := QPSet{Stats: make(map[string]uint64)}
	var rawMode uint32
	var haveMode bool
	for _, a := range attrs {
		switch a.Type {
		case attrStatMode:
			if len(a.Data) < 4 {
				return QPSet{}, fmt.Errorf("short STAT_MODE on counter set")
			}
			rawMode = binary.LittleEndian.Uint32(a.Data[:4])
			haveMode = true
		case attrResType:
			if len(a.Data) < 1 {
				return QPSet{}, fmt.Errorf("short RES_TYPE")
			}
			set.QPType = qpTypeName(a.Data[0])
		case attrResPID:
			set.HasPID = true
		case attrStatHWCounters:
			hw, err := parseHWCounterTable(a.Data)
			if err != nil {
				return QPSet{}, err
			}
			for _, c := range hw {
				if c.Name == "" || !c.HasValue {
					continue
				}
				set.Stats[c.Name] = c.Value
			}
		case attrResQP:
			// LQPN list is kernel dump cost; skip for metrics.
		}
	}
	if haveMode {
		set.Mode = counterModeName(rawMode)
	}
	return set, nil
}

func counterModeName(mode uint32) string {
	switch mode {
	case counterModeNone:
		return "none"
	case counterModeAuto:
		return "auto"
	case counterModeManual:
		return "manual"
	default:
		return "none"
	}
}

func qpTypeName(t uint8) string {
	// Matches iproute2 qp_types_to_str / IB_QPT_*.
	names := []string{
		"SMI", "GSI", "RC", "UC", "UD", "RAW_IPV6", "RAW_ETHERTYPE",
		"UNKNOWN", "RAW_PACKET", "XRC_INI", "XRC_TGT",
	}
	if int(t) < len(names) {
		return names[t]
	}
	return fmt.Sprintf("%d", t)
}

func encodeQPModeQuery(devIndex, port uint32) []byte {
	return concat(
		putU32(attrDevIndex, devIndex),
		putU32(attrPortIndex, port),
		putU32(attrStatRes, attrResQP),
		putU32(attrStatMode, counterModeManual),
	)
}

func encodeQPDumpQuery(devIndex, port uint32) []byte {
	return concat(
		putU32(attrDevIndex, devIndex),
		putU32(attrPortIndex, port),
		putU32(attrStatRes, attrResQP),
	)
}

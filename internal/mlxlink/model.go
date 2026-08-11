// Package mlxlink collects physical link and optical module telemetry from the
// NVIDIA mlxlink utility. A single mlxlink invocation is expensive (roughly one
// second of wall clock time), so collection runs in the background and scrapes
// are served from an in-memory cache.
package mlxlink

// ErrorReason classifies why a collection attempt failed. Values are exported
// as the reason label of mlxlink_collection_errors_total, so they must stay
// low-cardinality and stable across releases.
type ErrorReason string

const (
	// ReasonTimeout indicates the mlxlink invocation exceeded the command timeout.
	ReasonTimeout ErrorReason = "timeout"
	// ReasonCommandNotFound indicates the mlxlink binary is missing.
	ReasonCommandNotFound ErrorReason = "command_not_found"
	// ReasonPermissionDenied indicates the process may not execute mlxlink or
	// access the underlying device.
	ReasonPermissionDenied ErrorReason = "permission_denied"
	// ReasonExitError indicates mlxlink ran but returned a non-zero exit status.
	ReasonExitError ErrorReason = "exit_error"
	// ReasonInvalidJSON indicates the output could not be decoded.
	ReasonInvalidJSON ErrorReason = "invalid_json"
	// ReasonOutputTooLarge indicates the output exceeded the stdout limit and the
	// process was killed.
	ReasonOutputTooLarge ErrorReason = "output_too_large"
	// ReasonOverlapping indicates a sweep was still running when the next tick
	// fired, so the tick was dropped.
	ReasonOverlapping ErrorReason = "overlapping"
	// ReasonUnknown is the fallback for unclassified failures.
	ReasonUnknown ErrorReason = "unknown"
)

// String returns the label value form of the reason.
func (r ErrorReason) String() string { return string(r) }

// Value is an optional float64 sample. Valid is false when mlxlink reported the
// field as missing, "N/A", or otherwise unparsable; invalid values must never
// be exported as a metric sample.
type Value struct {
	Float float64
	Valid bool
}

// LaneValue is a sample scoped to a single lane. Lane carries the lane number
// as reported by mlxlink; the decoder owns the numbering base so that the
// exported lane label matches the vendor tooling.
type LaneValue struct {
	Lane  int
	Value float64
}

// Target identifies one collection unit: a single port of a single RDMA device.
// PCIAddr and NetDev are best-effort and may be empty when sysfs does not
// expose them.
type Target struct {
	Device  string
	Port    string
	PCIAddr string
	NetDev  string
}

// PortData is the decoded result of one mlxlink invocation for one port.
type PortData struct {
	Link         LinkInfo
	Counters     Counters
	Module       Module
	FECHistogram []FECHistogramBin
	SerDesTX     SerDesTX
	Eye          Eye
}

// Eye holds network link eye-opening measurements per lane. Each family uses
// the lane numbers reported by mlxlink rather than positional labels.
type Eye struct {
	InitialFOM []LaneValue
	LastFOM    []LaneValue
	UpperGrade []LaneValue
	MidGrade   []LaneValue
	LowerGrade []LaneValue
}

// PCIeEye holds PCIe link eye-opening measurements per lane.
type PCIeEye struct {
	InitialFOM []LaneValue
	LastFOM    []LaneValue
}

// FECHistogramBin counts received FEC codewords by the number of errors in a
// codeword. A range has equal minimum and maximum values when mlxlink reports
// a single error count such as "[3]".
type FECHistogramBin struct {
	Bin           int
	ErrorCountMin uint64
	ErrorCountMax uint64
	Occurrences   uint64
}

// SerDesFIRCoefficient is one vendor-defined transmitter FIR tuning value.
type SerDesFIRCoefficient struct {
	Lane  int
	Tap   string
	Value float64
}

// SerDesTX holds the transmitter tuning codes that mlxlink reports per lane.
type SerDesTX struct {
	FIRCoefficients []SerDesFIRCoefficient
	DriveAmplitude  []LaneValue
}

// LinkInfo holds the descriptive link attributes exported as labels of
// mlxlink_link_info. Empty strings mean mlxlink did not report the attribute.
type LinkInfo struct {
	State           string
	PhysicalState   string
	Speed           string
	Width           string
	FEC             string
	AutoNegotiation string
}

// Counters holds physical layer error counters and bit error ratios. mlxlink
// counters may be cleared by other tooling, so they can decrease between polls.
type Counters struct {
	EffectivePhysicalErrors Value
	LinkDown                Value
	LinkErrorRecovery       Value
	EffectiveBER            Value
	RawBER                  Value

	RawPhysicalErrorsLane []LaneValue
	RawBERLane            []LaneValue
}

// Module holds optical module diagnostics (DDM), fault flags, and inventory.
// Fault fields carry 0 or 1.
type Module struct {
	TemperatureCelsius Value
	VoltageVolts       Value

	BiasCurrentAmperes []LaneValue
	RxPowerDBm         []LaneValue
	TxPowerDBm         []LaneValue

	ModuleFWFault   Value
	DatapathFWFault Value

	TxFault        []LaneValue
	TxLOS          []LaneValue
	RxLOS          []LaneValue
	TxCDRLOL       []LaneValue
	RxCDRLOL       []LaneValue
	DatapathActive []LaneValue

	Info ModuleInfo
}

// ModuleInfo holds transceiver inventory exported as labels of
// mlxlink_module_info. Empty strings mean mlxlink did not report the attribute.
type ModuleInfo struct {
	Identifier            string
	Vendor                string
	PartNumber            string
	SerialNumber          string
	Revision              string
	FirmwareVersion       string
	ActiveHostCompliance  string
	ActiveMediaCompliance string
	CableType             string
}

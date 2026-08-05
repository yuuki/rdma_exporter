package mlxlink

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// clock and ticker isolate the poller from wall clock time so tests can drive
// sweeps deterministically.
type clock interface {
	Now() time.Time
	NewTicker(d time.Duration) ticker
}

type ticker interface {
	C() <-chan time.Time
	Stop()
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) NewTicker(d time.Duration) ticker { return &realTicker{t: time.NewTicker(d)} }

type realTicker struct{ t *time.Ticker }

func (r *realTicker) C() <-chan time.Time { return r.t.C }

func (r *realTicker) Stop() { r.t.Stop() }

// discoverer and commandRunner are the consumer side views of *SysfsDiscovery
// and *ExecRunner, kept unexported so the package's public surface stays the
// concrete implementations.
type discoverer interface {
	Discover(ctx context.Context) ([]Target, error)
}

type commandRunner interface {
	Run(ctx context.Context, device string) ([]byte, error)
}

// DeviceSnapshot is the last known state of one device. Data and LastSuccess
// survive failed sweeps so a transient error does not blank out the metrics; a
// zero LastSuccess means the device has never been collected successfully.
type DeviceSnapshot struct {
	Target       Target
	Data         PortData
	LastSuccess  time.Time
	LastError    ErrorReason
	LastDuration time.Duration
}

// snapshotSet is an immutable set of device snapshots, sorted by device name so
// that exported metrics keep a stable order. It is replaced wholesale, never
// mutated in place.
type snapshotSet struct {
	devices []DeviceSnapshot
}

func newSnapshotSet(devices []DeviceSnapshot) *snapshotSet {
	sorted := make([]DeviceSnapshot, len(devices))
	copy(sorted, devices)
	slices.SortFunc(sorted, func(a, b DeviceSnapshot) int {
		return strings.Compare(a.Target.Device, b.Target.Device)
	})
	return &snapshotSet{devices: sorted}
}

// lookup finds a device by name; a nil set simply has no devices, which is the
// state before the first sweep completes.
func (s *snapshotSet) lookup(device string) (DeviceSnapshot, bool) {
	if s == nil {
		return DeviceSnapshot{}, false
	}
	for _, snapshot := range s.devices {
		if snapshot.Target.Device == device {
			return snapshot, true
		}
	}
	return DeviceSnapshot{}, false
}

// Poller collects mlxlink data in the background so that scrapes never execute
// mlxlink themselves. A single sweep loop walks every device in turn, which
// keeps concurrency at one and makes overlapping runs structurally impossible.
type Poller struct {
	discovery discoverer
	runner    commandRunner
	interval  time.Duration
	clk       clock
	logger    *slog.Logger

	store  atomic.Pointer[snapshotSet]
	errors *prometheus.CounterVec
	ready  atomic.Bool
}

// PollerOption customises a Poller at construction time.
type PollerOption func(*Poller)

// withClock replaces the time source; tests use it to drive sweeps.
func withClock(c clock) PollerOption {
	return func(p *Poller) { p.clk = c }
}

// NewPoller returns a poller that collects from discovery through runner every
// interval. A nil logger falls back to slog.Default.
func NewPoller(discovery *SysfsDiscovery, runner *ExecRunner, interval time.Duration, logger *slog.Logger, opts ...PollerOption) *Poller {
	return newPoller(discovery, runner, interval, logger, opts...)
}

func newPoller(discovery discoverer, runner commandRunner, interval time.Duration, logger *slog.Logger, opts ...PollerOption) *Poller {
	if logger == nil {
		logger = slog.Default()
	}

	p := &Poller{
		discovery: discovery,
		runner:    runner,
		interval:  interval,
		clk:       realClock{},
		logger:    logger,
		errors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mlxlink_collection_errors_total",
			Help: "Total number of failed mlxlink collection attempts, by failure reason.",
		}, []string{"device", "port", "pci_addr", "reason"}),
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Errors exposes the collection error counter for registration by the caller.
func (p *Poller) Errors() prometheus.Collector { return p.errors }

// Snapshots returns the current immutable snapshot set, or nil before the first
// device has been collected.
func (p *Poller) Snapshots() *snapshotSet { return p.store.Load() }

// Ready reports whether at least one device has ever been collected
// successfully. It never returns to false: once data exists, a later failure
// leaves the exporter serving that data rather than dropping out of service.
func (p *Poller) Ready() bool { return p.ready.Load() }

// Run sweeps every device immediately and then once per interval, blocking
// until ctx is done. Call it once.
func (p *Poller) Run(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}

	// Sweep before waiting for the first tick so readiness does not lag a full
	// interval behind startup.
	p.sweep(ctx)

	t := p.clk.NewTicker(p.interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C():
			p.sweep(ctx)
			p.drainTicks(ctx, t)
		}
	}
}

// drainTicks discards the single tick that may have fired while a sweep was
// running and counts it: it is the sweep that could not start.
//
// Exactly one tick is taken per sweep. A real ticker coalesces the ticks it
// missed, so no more than one can ever be pending however long a sweep runs,
// and taking one keeps the drain finite and free of overcounting even against a
// fake that queues more. The channel length cannot be used to size this:
// since Go 1.23 a ticker channel reports len 0 while a tick is pending.
//
// A tick that arrives just as the drain runs can be consumed here instead of
// starting a sweep, which slips that sweep by one interval and attributes one
// overlap that did not happen. Together with coalescing, overlaps are
// undercounted; the signal is meant for spotting a poll interval that is too
// short, not exact accounting.
func (p *Poller) drainTicks(ctx context.Context, t ticker) {
	select {
	case <-t.C():
		if ctx.Err() != nil {
			return
		}
		p.countOverlap()
	default:
	}
}

func (p *Poller) countOverlap() {
	set := p.store.Load()
	if set == nil {
		return
	}

	for _, snapshot := range set.devices {
		p.countError(snapshot.Target, ReasonOverlapping)
	}
	// Logged after counting so the record marks a completed accounting.
	p.logger.Warn("mlxlink sweep did not finish before the next tick",
		"devices", len(set.devices), "poll_interval", p.interval.String())
}

// sweep collects every discovered device once, in order. Discovery is repeated
// on each sweep so hotplugged devices appear and removed ones fall out of the
// published set.
func (p *Poller) sweep(ctx context.Context) {
	targets, err := p.discovery.Discover(ctx)
	if err != nil {
		if ctx.Err() == nil {
			p.logger.Warn("mlxlink device discovery failed", "err", err)
		}
		// Keep serving the previous set rather than dropping every device
		// because sysfs was momentarily unreadable.
		return
	}

	previous := p.store.Load()
	collected := make([]DeviceSnapshot, 0, len(targets))

	for i, target := range targets {
		snapshot, ok := p.collect(ctx, target, previous)
		if !ok {
			// Shutting down: leave the published set as it is.
			return
		}
		collected = append(collected, snapshot)
		p.publish(collected, targets[i+1:], previous)

		// Readiness is announced only once the data behind it is published:
		// a reader that sees Ready() must be able to scrape that device.
		if snapshot.LastError == "" {
			p.ready.Store(true)
		}
	}

	// Devices that disappeared are absent from targets and therefore from the
	// set published here.
	p.store.Store(newSnapshotSet(collected))
}

// publish makes the devices collected so far visible mid-sweep. Devices this
// sweep has not reached yet keep their previous values so a scrape never sees a
// gap while the sweep walks the remaining devices.
func (p *Poller) publish(collected []DeviceSnapshot, pending []Target, previous *snapshotSet) {
	devices := make([]DeviceSnapshot, 0, len(collected)+len(pending))
	devices = append(devices, collected...)
	for _, target := range pending {
		if snapshot, ok := previous.lookup(target.Device); ok {
			devices = append(devices, snapshot)
		}
	}
	p.store.Store(newSnapshotSet(devices))
}

// collect runs mlxlink for one target and folds the result into the previous
// snapshot. The boolean is false only while shutting down, where the failure is
// expected and must not be recorded as a collection error.
func (p *Poller) collect(ctx context.Context, target Target, previous *snapshotSet) (DeviceSnapshot, bool) {
	snapshot, _ := previous.lookup(target.Device)
	// Discovery may have refreshed the labels of an existing device.
	snapshot.Target = target

	start := p.clk.Now()
	raw, err := p.runner.Run(ctx, target.Device)
	now := p.clk.Now()
	snapshot.LastDuration = now.Sub(start)

	if err != nil {
		if ctx.Err() != nil {
			return DeviceSnapshot{}, false
		}
		p.recordFailure(&snapshot, ReasonFromError(err), err)
		return snapshot, true
	}

	data, err := Decode(raw)
	if err != nil {
		// Decode returns a plain error by design, so the reason is assigned
		// here; ReasonFromError would flatten it to unknown.
		p.recordFailure(&snapshot, ReasonInvalidJSON, err)
		return snapshot, true
	}

	snapshot.Data = data
	snapshot.LastSuccess = now
	// Readiness is deliberately not announced here: the caller publishes the
	// snapshot first. An empty LastError is what marks this round a success.
	snapshot.LastError = ""

	p.logger.Debug("mlxlink device collected",
		"device", target.Device, "port", target.Port, "pci_addr", target.PCIAddr,
		"duration", snapshot.LastDuration)
	return snapshot, true
}

// recordFailure keeps the previous data and last success timestamp: stale data
// with a visible error beats no data at all, and staleness is bounded by the
// collector.
func (p *Poller) recordFailure(snapshot *DeviceSnapshot, reason ErrorReason, err error) {
	snapshot.LastError = reason
	p.countError(snapshot.Target, reason)

	// Only the underlying cause is logged here. A *RunError renders its
	// captured stderr, which would put up to 4 KiB of tool output into the
	// warning of every failed sweep; the runner already records it at debug
	// level, where the volume is asked for.
	logErr := err
	var runErr *RunError
	if errors.As(err, &runErr) {
		logErr = runErr.Err
	}

	p.logger.Warn("mlxlink collection failed",
		"device", snapshot.Target.Device, "port", snapshot.Target.Port,
		"pci_addr", snapshot.Target.PCIAddr, "reason", reason.String(),
		"duration", snapshot.LastDuration, "err", logErr)
}

func (p *Poller) countError(target Target, reason ErrorReason) {
	p.errors.WithLabelValues(target.Device, target.Port, target.PCIAddr, reason.String()).Inc()
}

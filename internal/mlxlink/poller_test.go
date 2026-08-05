package mlxlink

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// notifyHandler forwards warning messages to a channel so tests can wait for a
// completed log record instead of polling for its side effects.
type notifyHandler struct {
	records chan<- string
}

func (h *notifyHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *notifyHandler) Handle(_ context.Context, record slog.Record) error {
	if record.Level < slog.LevelWarn {
		return nil
	}
	select {
	case h.records <- record.Message:
	default:
	}
	return nil
}

func (h *notifyHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *notifyHandler) WithGroup(string) slog.Handler { return h }

const testPollInterval = 30 * time.Second

// minimalMlxlinkJSON is the smallest response Decode accepts. The poller tests
// exercise sweep mechanics, so an output without any section is enough.
var minimalMlxlinkJSON = []byte(`{"result":{"output":{}},"status":{"code":0,"message":"success"}}`)

var (
	targetMlx0 = Target{Device: "mlx5_0", Port: "1", PCIAddr: "0000:1a:00.0", NetDev: "ens1f0np0"}
	targetMlx1 = Target{Device: "mlx5_1", Port: "1", PCIAddr: "0000:1a:00.1", NetDev: "ens1f1np1"}
)

// fakeDiscoverer replays one target list per sweep and repeats the last one
// once the script is exhausted.
type fakeDiscoverer struct {
	mu      sync.Mutex
	batches [][]Target
	err     error
	calls   int
}

func newFakeDiscoverer(batches ...[]Target) *fakeDiscoverer {
	return &fakeDiscoverer{batches: batches}
}

func (f *fakeDiscoverer) Discover(context.Context) ([]Target, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.err != nil {
		return nil, f.err
	}
	index := min(f.calls, len(f.batches)-1)
	f.calls++
	return f.batches[index], nil
}

// fakeRunner returns a canned result and reports every call, which lets tests
// observe sweep progress without polling.
type fakeRunner struct {
	mu     sync.Mutex
	output []byte
	err    error
	clk    *fakeClock
	step   time.Duration
	// onCall observes poller state from inside a sweep, where the effects of
	// the devices collected so far are already published.
	onCall func(device string)

	calls []string
	done  chan string
}

func newFakeRunner(output []byte) *fakeRunner {
	return &fakeRunner{output: output, done: make(chan string, 64)}
}

func (f *fakeRunner) Run(_ context.Context, device string) ([]byte, error) {
	f.mu.Lock()
	output, err := f.output, f.err
	onCall := f.onCall
	f.calls = append(f.calls, device)
	if f.clk != nil {
		f.clk.advance(f.step)
	}
	f.mu.Unlock()

	if onCall != nil {
		onCall(device)
	}

	select {
	case f.done <- device:
	default:
	}
	return output, err
}

func (f *fakeRunner) setResult(output []byte, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.output, f.err = output, err
}

func (f *fakeRunner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// awaitCall waits for the next runner invocation. Anything the poller did
// before that call is guaranteed to be visible, because the sweep loop is a
// single goroutine.
func (f *fakeRunner) awaitCall(t *testing.T) string {
	t.Helper()

	select {
	case device := <-f.done:
		return device
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for a runner call, saw %d", f.callCount())
		return ""
	}
}

// fakeClock hands out a manually driven ticker and reports when the poller
// creates it, which happens only after the initial sweep has been published.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	ticks   chan time.Time
	created chan struct{}
	stopped bool
}

func newFakeClock(buffer int) *fakeClock {
	return &fakeClock{
		now:     time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC),
		ticks:   make(chan time.Time, buffer),
		created: make(chan struct{}, 1),
	}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func (c *fakeClock) NewTicker(time.Duration) ticker {
	select {
	case c.created <- struct{}{}:
	default:
	}
	return &fakeTicker{clk: c}
}

// awaitTicker blocks until the poller finished its initial sweep and asked for
// a ticker.
func (c *fakeClock) awaitTicker(t *testing.T) {
	t.Helper()

	select {
	case <-c.created:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the initial sweep to finish")
	}
}

func (c *fakeClock) tick(t *testing.T) {
	t.Helper()

	select {
	case c.ticks <- c.Now():
	case <-time.After(5 * time.Second):
		t.Fatal("timed out queueing a tick")
	}
}

type fakeTicker struct{ clk *fakeClock }

func (f *fakeTicker) C() <-chan time.Time { return f.clk.ticks }

func (f *fakeTicker) Stop() {
	f.clk.mu.Lock()
	defer f.clk.mu.Unlock()
	f.clk.stopped = true
}

func newTestPoller(t *testing.T, d discoverer, r commandRunner, clk *fakeClock) *Poller {
	t.Helper()

	return newPoller(d, r, testPollInterval, newDiscardLogger(), withClock(clk))
}

func errorCount(t *testing.T, p *Poller, target Target, reason ErrorReason) float64 {
	t.Helper()

	return testutil.ToFloat64(p.errors.WithLabelValues(target.Device, target.Port, target.PCIAddr, reason.String()))
}

func snapshotFor(t *testing.T, p *Poller, device string) DeviceSnapshot {
	t.Helper()

	snapshot, ok := p.Snapshots().lookup(device)
	if !ok {
		t.Fatalf("expected a snapshot for %s, got %+v", device, p.Snapshots())
	}
	return snapshot
}

func deviceNames(set *snapshotSet) []string {
	if set == nil {
		return nil
	}
	names := make([]string, 0, len(set.devices))
	for _, snapshot := range set.devices {
		names = append(names, snapshot.Target.Device)
	}
	return names
}

func TestPoller_InitialSweepPopulatesSnapshotsAndReady(t *testing.T) {
	t.Parallel()

	clk := newFakeClock(1)
	runner := newFakeRunner(minimalMlxlinkJSON)
	// Discovery deliberately reports the devices out of order.
	poller := newTestPoller(t, newFakeDiscoverer([]Target{targetMlx1, targetMlx0}), runner, clk)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		poller.Run(ctx)
	}()

	clk.awaitTicker(t)

	set := poller.Snapshots()
	if set == nil {
		t.Fatal("expected snapshots after the initial sweep")
	}
	if got := deviceNames(set); len(got) != 2 || got[0] != "mlx5_0" || got[1] != "mlx5_1" {
		t.Fatalf("expected snapshots sorted by device name, got %v", got)
	}
	if !poller.Ready() {
		t.Fatal("expected the poller to be ready after a successful sweep")
	}

	snapshot := snapshotFor(t, poller, "mlx5_0")
	if snapshot.Target != targetMlx0 {
		t.Fatalf("expected target %+v, got %+v", targetMlx0, snapshot.Target)
	}
	if snapshot.LastSuccess.IsZero() {
		t.Fatal("expected a last success timestamp")
	}
	if snapshot.LastError != "" {
		t.Fatalf("expected no last error, got %q", snapshot.LastError)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

func TestPoller_ReadyOnlyAfterSnapshotIsPublished(t *testing.T) {
	t.Parallel()

	clk := newFakeClock(1)
	runner := newFakeRunner(minimalMlxlinkJSON)
	poller := newTestPoller(t, newFakeDiscoverer([]Target{targetMlx0}), runner, clk)

	ctx := context.Background()
	snapshot, ok := poller.collect(ctx, targetMlx0, nil)
	if !ok || snapshot.LastError != "" {
		t.Fatalf("expected a successful collection, got %+v", snapshot)
	}

	// Collecting alone must not announce readiness: nothing is published yet,
	// so a reader acting on Ready() would find no data.
	if poller.Ready() {
		t.Fatal("expected the poller to stay unready until the snapshot is published")
	}
	if set := poller.Snapshots(); set != nil {
		t.Fatalf("expected nothing published yet, got %v", deviceNames(set))
	}

	poller.sweep(ctx)

	if !poller.Ready() {
		t.Fatal("expected the poller to be ready after a published sweep")
	}
	if _, found := poller.Snapshots().lookup(targetMlx0.Device); !found {
		t.Fatalf("readiness must imply published data, got %v", deviceNames(poller.Snapshots()))
	}
}

func TestPoller_PartialSweepStaysVisible(t *testing.T) {
	t.Parallel()

	clk := newFakeClock(1)
	runner := newFakeRunner(minimalMlxlinkJSON)
	poller := newTestPoller(t, newFakeDiscoverer([]Target{targetMlx0, targetMlx1}), runner, clk)

	ctx := context.Background()
	poller.sweep(ctx)
	firstSuccess := snapshotFor(t, poller, targetMlx1.Device).LastSuccess

	clk.advance(time.Minute)
	var midSweep *snapshotSet
	runner.mu.Lock()
	runner.onCall = func(device string) {
		if device == targetMlx1.Device {
			midSweep = poller.Snapshots()
		}
	}
	runner.mu.Unlock()

	poller.sweep(ctx)

	// While the second device is being collected, the first already carries
	// this sweep's result and the second still carries the previous one.
	if got := deviceNames(midSweep); len(got) != 2 {
		t.Fatalf("expected both devices to stay published mid sweep, got %v", got)
	}
	updated, _ := midSweep.lookup(targetMlx0.Device)
	if !updated.LastSuccess.Equal(clk.Now()) {
		t.Fatalf("expected the collected device to be published immediately, got %v", updated.LastSuccess)
	}
	pending, _ := midSweep.lookup(targetMlx1.Device)
	if !pending.LastSuccess.Equal(firstSuccess) {
		t.Fatalf("expected the pending device to keep its previous value, got %v", pending.LastSuccess)
	}
}

func TestPoller_ReaddedDeviceAdoptsNewPCIAddr(t *testing.T) {
	t.Parallel()

	moved := Target{Device: "mlx5_0", Port: "1", PCIAddr: "0000:2b:00.0", NetDev: "ens2f0np0"}
	clk := newFakeClock(1)
	runner := newFakeRunner(minimalMlxlinkJSON)
	discovery := newFakeDiscoverer(
		[]Target{targetMlx0},
		[]Target{},
		[]Target{moved},
	)
	poller := newTestPoller(t, discovery, runner, clk)

	ctx := context.Background()
	poller.sweep(ctx)
	if got := snapshotFor(t, poller, moved.Device).Target; got != targetMlx0 {
		t.Fatalf("expected the original target, got %+v", got)
	}

	poller.sweep(ctx)
	if got := deviceNames(poller.Snapshots()); len(got) != 0 {
		t.Fatalf("expected the removed device to drop out, got %v", got)
	}

	clk.advance(time.Minute)
	poller.sweep(ctx)

	readded := snapshotFor(t, poller, moved.Device)
	if readded.Target != moved {
		t.Fatalf("expected the refreshed target %+v, got %+v", moved, readded.Target)
	}
	if !readded.LastSuccess.Equal(clk.Now()) {
		t.Fatalf("expected a fresh success timestamp, got %v", readded.LastSuccess)
	}
}

func TestPoller_RunnerErrorKeepsLastSuccessAndCountsReason(t *testing.T) {
	t.Parallel()

	clk := newFakeClock(1)
	runner := newFakeRunner(minimalMlxlinkJSON)
	runner.clk, runner.step = clk, 700*time.Millisecond
	poller := newTestPoller(t, newFakeDiscoverer([]Target{targetMlx0}), runner, clk)

	poller.sweep(context.Background())
	first := snapshotFor(t, poller, "mlx5_0")
	if first.LastDuration != 700*time.Millisecond {
		t.Fatalf("expected the measured duration, got %v", first.LastDuration)
	}

	clk.advance(time.Minute)
	runner.setResult(nil, &RunError{Reason: ReasonTimeout, Err: context.DeadlineExceeded})
	poller.sweep(context.Background())

	second := snapshotFor(t, poller, "mlx5_0")
	if second.LastError != ReasonTimeout {
		t.Fatalf("expected last error %s, got %q", ReasonTimeout, second.LastError)
	}
	if !second.LastSuccess.Equal(first.LastSuccess) {
		t.Fatalf("expected the previous success timestamp to survive, got %v", second.LastSuccess)
	}
	if second.Target != targetMlx0 {
		t.Fatalf("expected the target to be kept, got %+v", second.Target)
	}
	if got := errorCount(t, poller, targetMlx0, ReasonTimeout); got != 1 {
		t.Fatalf("expected 1 timeout error, got %v", got)
	}
	if got := testutil.CollectAndCount(poller.errors); got != 1 {
		t.Fatalf("expected a single error series, got %d", got)
	}
}

func TestPoller_FailureWarningOmitsStderr(t *testing.T) {
	t.Parallel()

	// The captured stderr belongs in the debug log the runner writes. Repeating
	// it in the warning would put up to 4 KiB of tool output into every failed
	// sweep of an operator running at info level.
	const captured = "mlxlink: E- Cannot open device, permission denied"

	var logged bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelWarn}))

	clk := newFakeClock(1)
	runner := newFakeRunner(nil)
	runner.setResult(nil, &RunError{
		Reason: ReasonExitError,
		Err:    errors.New("exit status 1"),
		Stderr: captured,
	})
	poller := newPoller(newFakeDiscoverer([]Target{targetMlx0}), runner, testPollInterval,
		logger, withClock(clk))

	poller.sweep(context.Background())

	if strings.Contains(logged.String(), captured) {
		t.Fatalf("expected the captured stderr to stay out of the warning, got %q", logged.String())
	}
	if !strings.Contains(logged.String(), "exit status 1") {
		t.Fatalf("expected the underlying cause to be logged, got %q", logged.String())
	}
	if !strings.Contains(logged.String(), ReasonExitError.String()) {
		t.Fatalf("expected the reason to be logged, got %q", logged.String())
	}
}

func TestPoller_DecodeErrorCountsInvalidJSON(t *testing.T) {
	t.Parallel()

	clk := newFakeClock(1)
	runner := newFakeRunner([]byte(`{"result":`))
	poller := newTestPoller(t, newFakeDiscoverer([]Target{targetMlx0}), runner, clk)

	poller.sweep(context.Background())

	snapshot := snapshotFor(t, poller, "mlx5_0")
	if snapshot.LastError != ReasonInvalidJSON {
		t.Fatalf("expected last error %s, got %q", ReasonInvalidJSON, snapshot.LastError)
	}
	if !snapshot.LastSuccess.IsZero() {
		t.Fatalf("expected no success timestamp, got %v", snapshot.LastSuccess)
	}
	if got := errorCount(t, poller, targetMlx0, ReasonInvalidJSON); got != 1 {
		t.Fatalf("expected 1 invalid_json error, got %v", got)
	}
	if poller.Ready() {
		t.Fatal("expected the poller to stay unready when decoding fails")
	}
}

func TestPoller_HotplugAddAndRemove(t *testing.T) {
	t.Parallel()

	clk := newFakeClock(1)
	runner := newFakeRunner(minimalMlxlinkJSON)
	discovery := newFakeDiscoverer(
		[]Target{targetMlx0},
		[]Target{targetMlx0, targetMlx1},
		[]Target{targetMlx1},
	)
	poller := newTestPoller(t, discovery, runner, clk)

	poller.sweep(context.Background())
	if got := deviceNames(poller.Snapshots()); len(got) != 1 || got[0] != "mlx5_0" {
		t.Fatalf("expected [mlx5_0], got %v", got)
	}

	poller.sweep(context.Background())
	if got := deviceNames(poller.Snapshots()); len(got) != 2 || got[0] != "mlx5_0" || got[1] != "mlx5_1" {
		t.Fatalf("expected [mlx5_0 mlx5_1] after hotplug, got %v", got)
	}

	poller.sweep(context.Background())
	if got := deviceNames(poller.Snapshots()); len(got) != 1 || got[0] != "mlx5_1" {
		t.Fatalf("expected [mlx5_1] after removal, got %v", got)
	}
}

func TestPoller_DiscoveryErrorKeepsPreviousSet(t *testing.T) {
	t.Parallel()

	clk := newFakeClock(1)
	runner := newFakeRunner(minimalMlxlinkJSON)
	discovery := newFakeDiscoverer([]Target{targetMlx0})
	poller := newTestPoller(t, discovery, runner, clk)

	poller.sweep(context.Background())

	discovery.mu.Lock()
	discovery.err = errors.New("sysfs unreadable")
	discovery.mu.Unlock()
	poller.sweep(context.Background())

	if got := deviceNames(poller.Snapshots()); len(got) != 1 || got[0] != "mlx5_0" {
		t.Fatalf("expected the previous set to survive discovery failure, got %v", got)
	}
	if got := testutil.CollectAndCount(poller.errors); got != 0 {
		t.Fatalf("expected no per-device errors for a discovery failure, got %d series", got)
	}
}

func TestPoller_OverlappingTickCountsErrors(t *testing.T) {
	t.Parallel()

	// The drain is exercised directly: nothing in the running poller marks the
	// end of a drain, so driving it through Run could only be synchronised by
	// coupling the test to the select structure of the sweep loop.
	clk := newFakeClock(2)
	runner := newFakeRunner(minimalMlxlinkJSON)
	poller := newTestPoller(t, newFakeDiscoverer([]Target{targetMlx0, targetMlx1}), runner, clk)

	ctx := context.Background()
	poller.sweep(ctx)
	tk := clk.NewTicker(testPollInterval)

	// A sweep that finished before the next tick has nothing to drain.
	poller.drainTicks(ctx, tk)
	if got := testutil.CollectAndCount(poller.errors); got != 0 {
		t.Fatalf("expected no errors without pending ticks, got %d series", got)
	}

	// A tick fired while the sweep was running: every target records it.
	clk.tick(t)
	clk.tick(t)
	poller.drainTicks(ctx, tk)

	for _, target := range []Target{targetMlx0, targetMlx1} {
		if got := errorCount(t, poller, target, ReasonOverlapping); got != 1 {
			t.Fatalf("expected 1 overlapping error for %s, got %v", target.Device, got)
		}
	}
	// A real ticker coalesces the ticks it missed, so one drain never accounts
	// for more than one; anything a fake queued beyond that is left for the
	// next sweep.
	if pending := len(clk.ticks); pending != 1 {
		t.Fatalf("expected the extra tick to be left pending, got %d", pending)
	}
}

// floodingTicker always has a tick pending, modelling ticks that keep arriving
// while the drain is running.
type floodingTicker struct {
	mu    sync.Mutex
	ch    chan time.Time
	sends int
}

func newFloodingTicker() *floodingTicker {
	return &floodingTicker{ch: make(chan time.Time, 1)}
}

func (f *floodingTicker) C() <-chan time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()

	if len(f.ch) == 0 {
		f.ch <- time.Now()
		f.sends++
	}
	return f.ch
}

func (f *floodingTicker) Stop() {}

func TestPoller_DrainTakesAtMostOneTick(t *testing.T) {
	t.Parallel()

	clk := newFakeClock(1)
	runner := newFakeRunner(minimalMlxlinkJSON)
	poller := newTestPoller(t, newFakeDiscoverer([]Target{targetMlx0}), runner, clk)

	ctx := context.Background()
	poller.sweep(ctx)

	// Ticks never stop arriving; the drain must still take a single one, so it
	// cannot overcount and cannot starve the sweep loop.
	tk := newFloodingTicker()
	done := make(chan struct{})
	go func() {
		defer close(done)
		poller.drainTicks(ctx, tk)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("drainTicks did not stop while ticks kept arriving")
	}

	if got := errorCount(t, poller, targetMlx0, ReasonOverlapping); got != 1 {
		t.Fatalf("expected exactly one overlap per drain, got %v", got)
	}

	// The loop resumes: a following sweep still collects.
	before := runner.callCount()
	poller.sweep(ctx)
	if got := runner.callCount(); got != before+1 {
		t.Fatalf("expected the sweep to run after the drain, got %d calls", got-before)
	}
}

func TestPoller_DrainCountsPendingRealTicker(t *testing.T) {
	t.Parallel()

	clk := newFakeClock(1)
	runner := newFakeRunner(minimalMlxlinkJSON)
	poller := newTestPoller(t, newFakeDiscoverer([]Target{targetMlx0}), runner, clk)

	ctx := context.Background()
	poller.sweep(ctx)

	// The production ticker, not a fake: since Go 1.23 its channel reports a
	// length of 0 while a tick is pending, so a drain sized from len would
	// count nothing here.
	tk := realClock{}.NewTicker(50 * time.Millisecond)
	defer tk.Stop()
	time.Sleep(80 * time.Millisecond)

	poller.drainTicks(ctx, tk)

	if got := errorCount(t, poller, targetMlx0, ReasonOverlapping); got != 1 {
		t.Fatalf("expected the pending tick to be counted once, got %v", got)
	}
}

func TestPoller_RunDrainsPendingTicks(t *testing.T) {
	t.Parallel()

	// Two ticks are queued before Run starts: the first drives a sweep and the
	// second is the backlog that sweep must account for. This pins the wiring
	// between the sweep loop and the drain.
	clk := newFakeClock(2)
	runner := newFakeRunner(minimalMlxlinkJSON)
	warnings := make(chan string, 8)
	poller := newPoller(newFakeDiscoverer([]Target{targetMlx0}), runner, testPollInterval,
		slog.New(&notifyHandler{records: warnings}), withClock(clk))

	clk.tick(t)
	clk.tick(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		poller.Run(ctx)
	}()

	// countOverlap logs after counting, so the record proves the accounting is
	// complete.
	select {
	case <-warnings:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the overlap warning")
	}

	if got := errorCount(t, poller, targetMlx0, ReasonOverlapping); got != 1 {
		t.Fatalf("expected 1 overlapping error, got %v", got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

func TestPoller_OverlappingTickIgnoredDuringShutdown(t *testing.T) {
	t.Parallel()

	clk := newFakeClock(1)
	runner := newFakeRunner(minimalMlxlinkJSON)
	poller := newTestPoller(t, newFakeDiscoverer([]Target{targetMlx0}), runner, clk)

	poller.sweep(context.Background())
	tk := clk.NewTicker(testPollInterval)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	clk.tick(t)
	poller.drainTicks(ctx, tk)

	if got := testutil.CollectAndCount(poller.errors); got != 0 {
		t.Fatalf("expected no overlapping errors during shutdown, got %d series", got)
	}
}

func TestPoller_ShutdownDoesNotCountErrors(t *testing.T) {
	t.Parallel()

	clk := newFakeClock(1)
	runner := newFakeRunner(minimalMlxlinkJSON)
	poller := newTestPoller(t, newFakeDiscoverer([]Target{targetMlx0, targetMlx1}), runner, clk)

	poller.sweep(context.Background())
	before := snapshotFor(t, poller, "mlx5_0")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// The real runner surfaces the parent cancellation as-is.
	runner.setResult(nil, context.Canceled)
	poller.sweep(ctx)

	if got := testutil.CollectAndCount(poller.errors); got != 0 {
		t.Fatalf("expected no errors to be counted during shutdown, got %d series", got)
	}
	after := snapshotFor(t, poller, "mlx5_0")
	if !after.LastSuccess.Equal(before.LastSuccess) || after.LastError != "" {
		t.Fatalf("expected the published set to be untouched, got %+v", after)
	}
	if got := deviceNames(poller.Snapshots()); len(got) != 2 {
		t.Fatalf("expected both devices to remain published, got %v", got)
	}
}

func TestPoller_ContextCancelStopsRun(t *testing.T) {
	t.Parallel()

	clk := newFakeClock(1)
	runner := newFakeRunner(minimalMlxlinkJSON)
	poller := newTestPoller(t, newFakeDiscoverer([]Target{targetMlx0}), runner, clk)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		poller.Run(ctx)
	}()

	clk.awaitTicker(t)
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}

	clk.mu.Lock()
	stopped := clk.stopped
	clk.mu.Unlock()
	if !stopped {
		t.Fatal("expected the ticker to be stopped when Run returns")
	}
	if calls := runner.callCount(); calls != 1 {
		t.Fatalf("expected exactly the initial sweep, got %d runner calls", calls)
	}
}

func TestPoller_AllFailedNeverReady(t *testing.T) {
	t.Parallel()

	clk := newFakeClock(1)
	runner := newFakeRunner(nil)
	runner.setResult(nil, &RunError{Reason: ReasonPermissionDenied, Err: errors.New("denied")})
	poller := newTestPoller(t, newFakeDiscoverer([]Target{targetMlx0, targetMlx1}), runner, clk)

	poller.sweep(context.Background())

	if poller.Ready() {
		t.Fatal("expected the poller to stay unready when every device fails")
	}
	for _, target := range []Target{targetMlx0, targetMlx1} {
		snapshot := snapshotFor(t, poller, target.Device)
		if snapshot.LastError != ReasonPermissionDenied {
			t.Fatalf("expected %s to report %s, got %q", target.Device, ReasonPermissionDenied, snapshot.LastError)
		}
		if !snapshot.LastSuccess.IsZero() {
			t.Fatalf("expected %s to have no success timestamp, got %v", target.Device, snapshot.LastSuccess)
		}
		if got := errorCount(t, poller, target, ReasonPermissionDenied); got != 1 {
			t.Fatalf("expected 1 permission_denied error for %s, got %v", target.Device, got)
		}
	}
}

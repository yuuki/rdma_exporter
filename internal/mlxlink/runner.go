package mlxlink

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

const (
	// maxOutputBytes caps mlxlink stdout. A larger response means the binary is
	// misbehaving, so the process is killed instead of buffered without bound.
	maxOutputBytes = 4 << 20
	// maxStderrBytes caps the stderr excerpt kept for diagnostics.
	maxStderrBytes = 4 << 10
)

// errOutputTooLarge is reported instead of the kill error so an overproducing
// binary fails identically whether it was killed or exited on its own.
var errOutputTooLarge = fmt.Errorf("mlxlink stdout exceeded %d bytes", maxOutputBytes)

// RunError reports a failed mlxlink invocation. Reason is used as the reason
// label of mlxlink_collection_errors_total.
type RunError struct {
	Reason ErrorReason
	Err    error
	Stderr string
}

func (e *RunError) Error() string {
	if e.Stderr != "" {
		return fmt.Sprintf("mlxlink failed (%s): %v: %s", e.Reason, e.Err, e.Stderr)
	}
	return fmt.Sprintf("mlxlink failed (%s): %v", e.Reason, e.Err)
}

func (e *RunError) Unwrap() error { return e.Err }

// ReasonFromError maps an error to its metric reason label. Errors that did not
// come from a run attempt are reported as unknown.
func ReasonFromError(err error) ErrorReason {
	var runErr *RunError
	if errors.As(err, &runErr) {
		return runErr.Reason
	}
	return ReasonUnknown
}

// ExecRunner invokes the mlxlink binary directly, never through a shell.
type ExecRunner struct {
	path    string
	timeout time.Duration
	logger  *slog.Logger
}

// NewExecRunner returns a runner for the mlxlink binary at path. A nil logger
// falls back to slog.Default.
func NewExecRunner(path string, timeout time.Duration, logger *slog.Logger) *ExecRunner {
	if logger == nil {
		logger = slog.Default()
	}
	return &ExecRunner{path: path, timeout: timeout, logger: logger}
}

// Run executes mlxlink for one device and returns its raw JSON stdout.
//
// Failures are wrapped in *RunError so the caller can attribute them to a
// reason label. A cancelled parent context is returned as-is: shutting down is
// not a collection error. Output past maxOutputBytes always fails the run, even
// when the process then exits successfully, because the buffer is truncated.
func (r *ExecRunner) Run(ctx context.Context, device string) ([]byte, error) {
	return r.run(ctx, device, "-m", "-c", "--rx_fec_histogram", "--show_histogram", "--show_serdes_tx")
}

// RunWithEye executes the normal query with network-port Eye telemetry.
func (r *ExecRunner) RunWithEye(ctx context.Context, device string) ([]byte, error) {
	return r.run(ctx, device, "-m", "-c", "--rx_fec_histogram", "--show_histogram", "--show_serdes_tx", "--show_eye")
}

// RunPCIeEye executes a separate Eye query for the device's root PCIe link.
func (r *ExecRunner) RunPCIeEye(ctx context.Context, device string) ([]byte, error) {
	return r.run(ctx, device, "--port_type", "PCIE", "--show_eye")
}

// RunBaseline executes only the original module and counter queries. The
// poller uses it to preserve base telemetry when an optional query makes
// mlxlink exit unsuccessfully.
func (r *ExecRunner) RunBaseline(ctx context.Context, device string) ([]byte, error) {
	return r.run(ctx, device, "-m", "-c")
}

func (r *ExecRunner) run(ctx context.Context, device string, queryArgs ...string) ([]byte, error) {
	runCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	// Cancelling the run context kills the process once it floods stdout.
	stdout := &limitedBuffer{limit: maxOutputBytes, onExceed: cancel}
	stderr := &limitedBuffer{limit: maxStderrBytes}

	args := make([]string, 0, len(queryArgs)+3)
	args = append(args, "-d", device)
	args = append(args, queryArgs...)
	args = append(args, "--json")
	cmd := exec.CommandContext(runCtx, r.path, args...)
	// mlxlink formats values per locale; pin to C so parsing stays stable.
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	// Run the child in its own process group and kill the group rather than the
	// direct child alone: a grandchild that inherited stdout would otherwise
	// hold the pipe open and keep Wait blocked long past the timeout.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killProcessGroup(cmd) }
	// Backstop for a descendant that survives the kill: the pipes are closed
	// after this delay so a sweep can never stall indefinitely.
	cmd.WaitDelay = r.timeout

	err := cmd.Run()

	// Shutting down is not a collection error.
	if parentErr := ctx.Err(); parentErr != nil {
		return nil, parentErr
	}
	// Checked before success: a process that overproduced and then exited
	// cleanly must not have its truncated output accepted, and that race is
	// exactly what the kill signal competes with.
	if stdout.Exceeded() {
		return nil, r.runError(device, ReasonOutputTooLarge, errOutputTooLarge, stderr.String())
	}
	if err == nil {
		return stdout.Bytes(), nil
	}
	return nil, r.runError(device, classifyRunError(runCtx, err), err, stderr.String())
}

func (r *ExecRunner) runError(device string, reason ErrorReason, err error, stderr string) *RunError {
	runErr := &RunError{Reason: reason, Err: err, Stderr: strings.TrimSpace(stderr)}
	r.logger.Debug("mlxlink invocation failed",
		"device", device, "reason", reason.String(), "err", err, "stderr", runErr.Stderr)
	return runErr
}

// killProcessGroup terminates every process started by the invocation. Killing
// only the direct child leaves grandchildren holding the stdout pipe, which
// keeps Wait blocked on EOF.
//
// The guarantees are deliberately narrow:
//
//   - While the child is unreaped (running or a zombie) its pid is pinned, so
//     killing the group is safe and takes the grandchildren with it.
//   - Cancellation can also fire after the child has been reaped, because
//     os/exec keeps watching the context until the output pipes are drained.
//     Signalling a raw pid then could hit an unrelated process group, so this
//     is a no-op instead.
//   - A grandchild that outlived its already reaped parent is therefore left
//     running; WaitDelay closing the pipes is what still bounds the caller.
func killProcessGroup(cmd *exec.Cmd) error {
	process := cmd.Process
	if process == nil {
		return os.ErrProcessDone
	}
	// os.Process tracks reap state, so this answers without a syscall once Wait
	// has completed, and succeeds for a zombie. The few instructions between
	// this probe and the kill would need a reap plus a full pid wraparound plus
	// a new group leader to matter.
	if err := process.Signal(syscall.Signal(0)); err != nil {
		return os.ErrProcessDone
	}
	// With Setpgid the child leads a group whose id equals its pid.
	if err := syscall.Kill(-process.Pid, syscall.SIGKILL); err != nil {
		return process.Kill()
	}
	return nil
}

// classifyRunError maps an invocation failure to its reason label. The run
// context is inspected instead of the returned error so the timeout is
// recognised regardless of how the process died.
func classifyRunError(runCtx context.Context, err error) ErrorReason {
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return ReasonTimeout
	}
	// The child exited but its output pipe stayed open past WaitDelay: the
	// invocation still failed to deliver in time.
	if errors.Is(err, exec.ErrWaitDelay) {
		return ReasonTimeout
	}
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
		return ReasonCommandNotFound
	}
	if errors.Is(err, fs.ErrPermission) {
		return ReasonPermissionDenied
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return ReasonExitError
	}
	return ReasonUnknown
}

// limitedBuffer keeps at most limit bytes and drops the rest. Writes never
// fail: reporting an error to os/exec would mask the underlying failure, so
// overproduction is signalled through onExceed (fired once) instead.
type limitedBuffer struct {
	limit    int
	onExceed func()
	buf      bytes.Buffer
	exceeded bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if remaining := b.limit - b.buf.Len(); remaining > 0 {
		if len(p) <= remaining {
			b.buf.Write(p)
			return len(p), nil
		}
		b.buf.Write(p[:remaining])
	}

	if !b.exceeded {
		b.exceeded = true
		if b.onExceed != nil {
			b.onExceed()
		}
	}
	return len(p), nil
}

func (b *limitedBuffer) Bytes() []byte { return b.buf.Bytes() }

func (b *limitedBuffer) String() string { return b.buf.String() }

func (b *limitedBuffer) Exceeded() bool { return b.exceeded }

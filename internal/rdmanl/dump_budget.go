package rdmanl

import (
	"context"
	"errors"
	"fmt"
)

// ErrDumpOverflow is returned when a netlink dump exceeds its receive budget.
var ErrDumpOverflow = errors.New("rdma netlink dump exceeded receive budget")

// ErrDumpInterrupted is returned when the kernel sets NLM_F_DUMP_INTR or NLMSG_OVERRUN.
var ErrDumpInterrupted = errors.New("rdma netlink dump was interrupted")

// DumpBudget limits how much of a multipart netlink dump is accepted.
type DumpBudget struct {
	MaxBytes int
	MaxMsgs  int
}

func defaultQPDumpBudget() DumpBudget {
	return DumpBudget{MaxBytes: 1 << 20, MaxMsgs: 256}
}

type dumpBudgetCtxKey struct{}

func withDumpBudget(ctx context.Context, budget DumpBudget) context.Context {
	return context.WithValue(ctx, dumpBudgetCtxKey{}, budget)
}

func dumpBudgetFrom(ctx context.Context) (DumpBudget, bool) {
	if ctx == nil {
		return DumpBudget{}, false
	}
	b, ok := ctx.Value(dumpBudgetCtxKey{}).(DumpBudget)
	return b, ok
}

func (b DumpBudget) check(nBytes, nMsgs int) error {
	if b.MaxBytes > 0 && nBytes > b.MaxBytes {
		return fmt.Errorf("%w: %d bytes", ErrDumpOverflow, nBytes)
	}
	if b.MaxMsgs > 0 && nMsgs > b.MaxMsgs {
		return fmt.Errorf("%w: %d messages", ErrDumpOverflow, nMsgs)
	}
	return nil
}

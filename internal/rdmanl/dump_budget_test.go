package rdmanl

import (
	"errors"
	"testing"
)

func TestDumpBudget_CheckBytes(t *testing.T) {
	t.Parallel()

	b := DumpBudget{MaxBytes: 100, MaxMsgs: 10}
	if err := b.check(100, 1); err != nil {
		t.Fatalf("at limit: %v", err)
	}
	if err := b.check(101, 1); !errors.Is(err, ErrDumpOverflow) {
		t.Fatalf("expected overflow, got %v", err)
	}
}

func TestDumpBudget_CheckMsgs(t *testing.T) {
	t.Parallel()

	b := DumpBudget{MaxBytes: 1 << 20, MaxMsgs: 2}
	if err := b.check(10, 2); err != nil {
		t.Fatalf("at limit: %v", err)
	}
	if err := b.check(10, 3); !errors.Is(err, ErrDumpOverflow) {
		t.Fatalf("expected overflow, got %v", err)
	}
}

func TestDumpBudget_ZeroMeansUnlimited(t *testing.T) {
	t.Parallel()

	if err := (DumpBudget{}).check(1<<30, 1<<20); err != nil {
		t.Fatalf("unlimited budget: %v", err)
	}
}

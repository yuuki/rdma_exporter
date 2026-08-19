//go:build !linux

package rdmanl

import "testing"

func TestNewUnsupported(t *testing.T) {
	t.Parallel()

	if _, err := New(); err == nil {
		t.Fatal("expected New to fail on non-linux")
	}
}

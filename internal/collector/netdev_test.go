package collector

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsRepresentorPhysPortName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		want bool
	}{
		{name: "p0", want: false},
		{name: "", want: false},
		{name: "1", want: false},
		{name: "pf0vf1", want: true},
		{name: "pf0sf1", want: true},
		{name: "c1pf0vf0", want: true},
		{name: "c1pf0sf0", want: true},
		{name: "pf0hpf", want: true},
		{name: "c1pf0hpf", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isRepresentorPhysPortName(tc.name); got != tc.want {
				t.Fatalf("isRepresentorPhysPortName(%q) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestReadNetDevPhysPortNameFromTrimsValue(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	netDir := filepath.Join(root, "class", "net", "ens1f0np0")
	if err := os.MkdirAll(netDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(netDir, "phys_port_name"), []byte("pf0vf1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := readNetDevPhysPortNameFrom(root, "ens1f0np0")
	if got != "pf0vf1" {
		t.Fatalf("got %q, want pf0vf1", got)
	}
}

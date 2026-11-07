package rdma

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// FuzzNormalizePortState checks the state and phys_state parser on any file
// contents: the result is a canonical name or the trimmed input, empty only
// for blank input, and normalizing it again changes nothing.
func FuzzNormalizePortState(f *testing.F) {
	for _, seed := range []string{
		"4: ACTIVE", "1: DOWN", "5: LinkUp", "3: Disabled", "ACTIVE", "LinkUp", "Disabled",
		"DOWN\n", "  2: INIT  ", "7: Phy Test", "9: FUTURE", "x", "", "99999999999999999999: ACTIVE", "4 5",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, value string) {
		for kind, names := range map[string]map[int]string{"state": portStateNames, "phys_state": portPhysStateNames} {
			got := normalizePortState(value, names)
			trimmed := strings.TrimSpace(value)
			if (got == "") != (trimmed == "") {
				t.Fatalf("%s %q normalized to %q", kind, value, got)
			}
			if got != trimmed && !isCanonicalName(got, names) {
				t.Fatalf("%s %q normalized to %q, neither the trimmed input nor a canonical name", kind, value, got)
			}
			if again := normalizePortState(got, names); again != got {
				t.Fatalf("%s %q normalized to %q, then to %q", kind, value, got, again)
			}
		}
	})
}

func isCanonicalName(name string, names map[int]string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

// FuzzSysfsPortFiles writes arbitrary contents into the files of one port and
// reads them back through Devices. Counters accept decimal uint64 values only
// (anything else is skipped, never an error), attributes are trimmed and drop
// a parenthesized suffix such as the "(4X EDR)" of a rate, and the netdev is
// the trimmed ndevs entry.
func FuzzSysfsPortFiles(f *testing.F) {
	f.Add("123", "InfiniBand", "4: ACTIVE", "5: LinkUp", "4X", "100 Gb/sec (4X EDR)", "ens1f0np0")
	f.Add("789\n", "Ethernet", "DOWN", "Disabled", "1X", "0 Gb/sec", "")
	f.Add("18446744073709551615", "Unknown", "", "", "", "", " bond0 \n")
	f.Add("18446744073709551616", "(InfiniBand)", "7: ARMED", "2: Polling", "12X (x)", "(rate)", "\n")
	f.Add("0x10", "", "x", "y", "", "200 Gb/sec (2X NDR)(x)", "eth\xff0")
	f.Add("-1", "\x00", "4", "5", " 4X ", "  ", "a b")

	root := f.TempDir()
	portDir := filepath.Join(root, classInfinibandPath, "mlx5_0", portsDirName, "1")
	for _, dir := range []string{countersDirName, filepath.Join(gidAttrsDirName, ndevsDirName)} {
		if err := os.MkdirAll(filepath.Join(portDir, dir), 0o755); err != nil {
			f.Fatal(err)
		}
	}
	provider := NewSysfsProvider()
	provider.SetSysfsRoot(root)

	f.Fuzz(func(t *testing.T, counter, linkLayer, state, physState, linkWidth, rate, ndev string) {
		for name, contents := range map[string]string{
			filepath.Join(countersDirName, "port_xmit_data"): counter,
			linkLayerFile: linkLayer,
			stateFile:     state,
			physStateFile: physState,
			linkWidthFile: linkWidth,
			rateFile:      rate,
			filepath.Join(gidAttrsDirName, ndevsDirName, "0"): ndev,
		} {
			if err := os.WriteFile(filepath.Join(portDir, name), []byte(contents), 0o644); err != nil {
				t.Fatal(err)
			}
		}

		devices, err := provider.Devices(context.Background())
		if err != nil {
			t.Fatalf("Devices: %v", err)
		}
		if len(devices) != 1 || len(devices[0].Ports) != 1 || devices[0].Ports[0].ID != 1 {
			t.Fatalf("want device mlx5_0 with port 1, got %+v", devices)
		}
		port := devices[0].Ports[0]

		value, got := port.Stats["port_xmit_data"]
		want, parseErr := strconv.ParseUint(strings.TrimSpace(counter), 10, 64)
		if present := parseErr == nil; got != present || (present && value != want) {
			t.Fatalf("counter %q read as %d (present %v), want %d (present %v)", counter, value, got, want, present)
		}

		attr := port.Attributes
		for name, pair := range map[string][2]string{
			"link_layer": {attr.LinkLayer, linkLayer},
			"link_width": {attr.LinkWidth, linkWidth},
			"rate":       {attr.LinkSpeed, rate},
		} {
			if pair[0] != attributeText(pair[1]) {
				t.Fatalf("%s %q read as %q, want %q", name, pair[1], pair[0], attributeText(pair[1]))
			}
		}
		if !isCanonicalName(attr.State, portStateNames) && attr.State != strings.TrimSpace(state) {
			t.Fatalf("state %q read as %q", state, attr.State)
		}
		if !isCanonicalName(attr.PhysState, portPhysStateNames) && attr.PhysState != strings.TrimSpace(physState) {
			t.Fatalf("phys_state %q read as %q", physState, attr.PhysState)
		}
		if want := strings.TrimSpace(ndev); attr.NetDev != want {
			t.Fatalf("ndevs entry %q read as netdev %q, want %q", ndev, attr.NetDev, want)
		}
	})
}

// attributeText is the reference for a port attribute read from sysfs: the
// trimmed contents up to a parenthesized suffix that does not start them.
func attributeText(contents string) string {
	text := strings.TrimSpace(contents)
	if before, _, found := strings.Cut(text, "("); found && before != "" {
		return strings.TrimSpace(before)
	}
	return text
}

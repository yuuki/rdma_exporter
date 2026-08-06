package systemd

import (
	"os"
	"strings"
	"testing"
)

func TestRDMAExporterService_ExecStartUsesBuiltInConfiguration(t *testing.T) {
	service, err := os.ReadFile("rdma_exporter.service")
	if err != nil {
		t.Fatalf("read systemd service: %v", err)
	}

	const want = "ExecStart=/usr/local/bin/rdma_exporter"
	var execStarts []string
	for _, line := range strings.Split(string(service), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ExecStart=") {
			execStarts = append(execStarts, line)
		}
	}

	if len(execStarts) != 1 {
		t.Fatalf("ExecStart lines = %d, want 1: %q", len(execStarts), execStarts)
	}
	if execStarts[0] != want {
		t.Errorf("ExecStart = %q, want %q", execStarts[0], want)
	}
}

func TestMlxlinkExporterRootOverride_RestoresDefaultCapabilityBoundingSet(t *testing.T) {
	override, err := os.ReadFile("mlxlink_exporter-root.conf")
	if err != nil {
		t.Fatalf("read root override: %v", err)
	}

	contents := string(override)
	for _, required := range []string{
		"[Service]",
		"User=root",
		"Group=root",
		"CapabilityBoundingSet=~",
	} {
		if !strings.Contains(contents, required) {
			t.Errorf("root override does not contain %q", required)
		}
	}
}

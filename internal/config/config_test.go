package config

import (
	"bytes"
	"errors"
	"flag"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestParseDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := Parse(nil)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.ListenAddress != defaultListenAddress {
		t.Fatalf("expected listen address %q, got %q", defaultListenAddress, cfg.ListenAddress)
	}
	if cfg.MetricsPath != defaultMetricsPath {
		t.Fatalf("expected metrics path %q, got %q", defaultMetricsPath, cfg.MetricsPath)
	}
	if cfg.LogLevel != defaultLogLevelValue() {
		t.Fatalf("expected log level info, got %v", cfg.LogLevel)
	}
	if cfg.ScrapeTimeout != defaultTimeout {
		t.Fatalf("expected scrape timeout %v, got %v", defaultTimeout, cfg.ScrapeTimeout)
	}
	if !cfg.CollectorEthtool {
		t.Fatal("expected ethtool collector to be enabled by default")
	}
	if !cfg.CollectorOptionalCounters {
		t.Fatal("expected optional-counters collector to be enabled by default")
	}
	if cfg.CollectorQPCounters {
		t.Fatal("expected qp-counters collector to be disabled by default")
	}
	if cfg.ShowVersion {
		t.Fatal("expected show version to be false by default")
	}
}

func TestEnvOverridesDefault(t *testing.T) {
	t.Setenv("RDMA_EXPORTER_LISTEN_ADDRESS", "127.0.0.1:9999")
	t.Setenv("RDMA_EXPORTER_SCRAPE_TIMEOUT", "2s")

	cfg, err := Parse(nil)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.ListenAddress != "127.0.0.1:9999" {
		t.Fatalf("expected listen address to come from env, got %q", cfg.ListenAddress)
	}
	if cfg.ScrapeTimeout != 2*time.Second {
		t.Fatalf("expected scrape timeout 2s, got %v", cfg.ScrapeTimeout)
	}
}

func TestFlagsOverrideEnv(t *testing.T) {
	t.Setenv("RDMA_EXPORTER_LISTEN_ADDRESS", "127.0.0.1:9999")

	cfg, err := Parse([]string{"-listen-address", "0.0.0.0:1234"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.ListenAddress != "0.0.0.0:1234" {
		t.Fatalf("expected listen address from flag, got %q", cfg.ListenAddress)
	}
}

func TestCollectorEthtoolToggleFromEnv(t *testing.T) {
	t.Setenv("RDMA_EXPORTER_COLLECTOR_ETHTOOL", "false")

	cfg, err := Parse(nil)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if cfg.CollectorEthtool {
		t.Fatal("expected ethtool collector to be disabled by env")
	}
}

func TestCollectorFlagsOverrideEnv(t *testing.T) {
	t.Setenv("RDMA_EXPORTER_COLLECTOR_ETHTOOL", "false")
	t.Setenv("RDMA_EXPORTER_COLLECTOR_OPTIONAL_COUNTERS", "false")
	t.Setenv("RDMA_EXPORTER_COLLECTOR_QP_COUNTERS", "false")

	cfg, err := Parse([]string{
		"--collector.ethtool",
		"--collector.optional-counters=true",
		"--collector.qp-counters",
	})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if !cfg.CollectorEthtool {
		t.Fatal("expected ethtool collector to be enabled by flag")
	}
	if !cfg.CollectorOptionalCounters {
		t.Fatal("expected optional-counters collector to be enabled by flag")
	}
	if !cfg.CollectorQPCounters {
		t.Fatal("expected qp-counters collector to be enabled by flag")
	}
}

func TestNoCollectorFlags(t *testing.T) {
	cfg, err := Parse([]string{
		"--no-collector.ethtool",
		"--no-collector.optional-counters",
		"--no-collector.qp-counters",
	})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if cfg.CollectorEthtool {
		t.Fatal("expected ethtool collector disabled")
	}
	if cfg.CollectorOptionalCounters {
		t.Fatal("expected optional-counters collector disabled")
	}
	if cfg.CollectorQPCounters {
		t.Fatal("expected qp-counters collector disabled")
	}
}

func TestCollectorLastExplicitFlagWins(t *testing.T) {
	cfg, err := Parse([]string{
		"--no-collector.ethtool",
		"--collector.ethtool",
		"--collector.qp-counters",
		"--no-collector.qp-counters",
	})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if !cfg.CollectorEthtool {
		t.Fatal("expected last --collector.ethtool to win")
	}
	if cfg.CollectorQPCounters {
		t.Fatal("expected last --no-collector.qp-counters to win")
	}
}

func TestCollectorBoolFlagDoesNotConsumeNextArg(t *testing.T) {
	cfg, err := Parse([]string{"--collector.qp-counters", "--listen-address", "127.0.0.1:1"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if !cfg.CollectorQPCounters {
		t.Fatal("expected qp-counters enabled")
	}
	if cfg.ListenAddress != "127.0.0.1:1" {
		t.Fatalf("listen address = %q, want 127.0.0.1:1", cfg.ListenAddress)
	}
}

func TestNoCollectorFlagRejectsValue(t *testing.T) {
	if _, err := Parse([]string{"--no-collector.qp-counters=true"}); err == nil {
		t.Fatal("expected error for valued --no-collector flag")
	}
}

func TestCollectorToggleRejectsInvalidEnv(t *testing.T) {
	t.Setenv("RDMA_EXPORTER_COLLECTOR_ETHTOOL", "notabool")

	if _, err := Parse(nil); err == nil {
		t.Fatal("expected error for invalid RDMA_EXPORTER_COLLECTOR_ETHTOOL")
	}
}

func TestRemovedEnableFlagsError(t *testing.T) {
	for _, name := range []string{
		"enable-roce-pfc-metrics",
		"enable-netdev-hw-metrics",
		"enable-rdma-optional-counters",
		"enable-rdma-qp-counters",
	} {
		_, err := Parse([]string{"--" + name})
		if err == nil {
			t.Fatalf("expected error for removed flag --%s", name)
		}
		if !strings.Contains(err.Error(), "has been removed") {
			t.Fatalf("removed flag --%s: got %v", name, err)
		}
	}
}

func TestRemovedEnableEnvError(t *testing.T) {
	t.Setenv("RDMA_EXPORTER_ENABLE_NETDEV_HW_METRICS", "false")

	if _, err := Parse(nil); err == nil {
		t.Fatal("expected error for leftover ENABLE_* env")
	} else if !strings.Contains(err.Error(), "RDMA_EXPORTER_ENABLE_NETDEV_HW_METRICS") {
		t.Fatalf("got %v", err)
	}
}

func TestRemovedEnableEnvEmptyStillFatal(t *testing.T) {
	t.Setenv("RDMA_EXPORTER_ENABLE_RDMA_QP_COUNTERS", "")

	if _, err := Parse(nil); err == nil {
		t.Fatal("expected error for empty leftover ENABLE_* env")
	}
}

func TestVersionSkipsRemovedEnv(t *testing.T) {
	t.Setenv("RDMA_EXPORTER_ENABLE_ROCE_PFC_METRICS", "true")

	cfg, err := Parse([]string{"--version"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if !cfg.ShowVersion {
		t.Fatal("expected show version")
	}
}

func TestHelpSkipsRemovedEnv(t *testing.T) {
	t.Setenv("RDMA_EXPORTER_ENABLE_ROCE_PFC_METRICS", "true")

	_, err := Parse([]string{"-h"})
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("expected flag.ErrHelp, got %v", err)
	}
}

func TestRemovedEnableRoCEPFCMetricsMatchingDefault(t *testing.T) {
	t.Setenv("RDMA_EXPORTER_ENABLE_ROCE_PFC_METRICS", "true")

	if _, err := Parse(nil); err == nil {
		t.Fatal("expected leftover ENABLE_ROCE_PFC_METRICS=true to be fatal")
	}
}

func TestRemovedFlagWritesRenameMessage(t *testing.T) {
	var buf bytes.Buffer
	orig := parseOutput
	parseOutput = &buf
	t.Cleanup(func() { parseOutput = orig })

	_, err := Parse([]string{"--enable-rdma-qp-counters"})
	if err == nil {
		t.Fatal("expected error")
	}
	out := buf.String()
	if !strings.Contains(out, "has been removed") || !strings.Contains(out, "--collector.qp-counters") {
		t.Fatalf("stderr missing rename message: %q", out)
	}
}

func TestHelpFromParseOmitsRemovedAndNoCollectorFlags(t *testing.T) {
	var buf bytes.Buffer
	orig := parseOutput
	parseOutput = &buf
	t.Cleanup(func() { parseOutput = orig })

	_, err := Parse([]string{"-h"})
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("expected flag.ErrHelp, got %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "-collector.ethtool") {
		t.Fatalf("help missing -collector.ethtool:\n%s", out)
	}
	if strings.Contains(out, "\n  -no-collector.") {
		t.Fatalf("help listed --no-collector.* as its own flag:\n%s", out)
	}
	if strings.Contains(out, "enable-roce-pfc-metrics") {
		t.Fatalf("help listed removed flag:\n%s", out)
	}
}

func TestInvalidDurationFromEnv(t *testing.T) {
	t.Setenv("RDMA_EXPORTER_SCRAPE_TIMEOUT", "notaduration")

	if _, err := Parse(nil); err == nil {
		t.Fatalf("expected error for invalid duration")
	}
}

func TestVersionFlag(t *testing.T) {
	cfg, err := Parse([]string{"--version"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if !cfg.ShowVersion {
		t.Fatalf("expected show version to be true when flag is set")
	}
}

func TestExcludeDevicesFromFlag(t *testing.T) {
	t.Parallel()

	cfg, err := Parse([]string{"--exclude-devices", "mlx5_0,mlx5_1"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if len(cfg.ExcludeDevices) != 2 {
		t.Fatalf("expected 2 excluded devices, got %d", len(cfg.ExcludeDevices))
	}
	if cfg.ExcludeDevices[0] != "mlx5_0" || cfg.ExcludeDevices[1] != "mlx5_1" {
		t.Fatalf("expected [mlx5_0 mlx5_1], got %v", cfg.ExcludeDevices)
	}
}

func TestExcludeDevicesFromEnv(t *testing.T) {
	t.Setenv("RDMA_EXPORTER_EXCLUDE_DEVICES", "mlx5_0, mlx5_2 ")

	cfg, err := Parse(nil)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if len(cfg.ExcludeDevices) != 2 {
		t.Fatalf("expected 2 excluded devices, got %d", len(cfg.ExcludeDevices))
	}
	if cfg.ExcludeDevices[0] != "mlx5_0" || cfg.ExcludeDevices[1] != "mlx5_2" {
		t.Fatalf("expected [mlx5_0 mlx5_2], got %v", cfg.ExcludeDevices)
	}
}

func TestExcludeDevicesEmpty(t *testing.T) {
	t.Parallel()

	cfg, err := Parse(nil)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.ExcludeDevices != nil {
		t.Fatalf("expected nil excluded devices, got %v", cfg.ExcludeDevices)
	}
}

func TestParseDeviceList(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"empty", "", nil},
		{"single", "mlx5_0", []string{"mlx5_0"}},
		{"multiple", "mlx5_0,mlx5_1", []string{"mlx5_0", "mlx5_1"}},
		{"with spaces", " mlx5_0 , mlx5_1 ", []string{"mlx5_0", "mlx5_1"}},
		{"trailing comma", "mlx5_0,", []string{"mlx5_0"}},
		{"empty parts", "mlx5_0,,mlx5_1", []string{"mlx5_0", "mlx5_1"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseDeviceList(tt.input)
			if len(got) != len(tt.want) {
				t.Errorf("parseDeviceList(%q) length = %d, want %d", tt.input, len(got), len(tt.want))
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("parseDeviceList(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func defaultLogLevelValue() slog.Level {
	lvl, _ := parseLogLevel(defaultLogLevel)
	return lvl
}

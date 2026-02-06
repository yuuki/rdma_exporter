package config

import (
	"log/slog"
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
	if !cfg.EnableRoCEPFCMetrics {
		t.Fatalf("expected RoCE PFC metrics to be enabled by default")
	}
	if cfg.ShowVersion {
		t.Fatalf("expected show version to be false by default")
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

func TestRoCEPFCMetricsToggleFromEnv(t *testing.T) {
	t.Setenv("RDMA_EXPORTER_ENABLE_ROCE_PFC_METRICS", "false")

	cfg, err := Parse(nil)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if cfg.EnableRoCEPFCMetrics {
		t.Fatalf("expected RoCE PFC metrics to be disabled by env")
	}
}

func TestRoCEPFCMetricsToggleFromFlag(t *testing.T) {
	t.Setenv("RDMA_EXPORTER_ENABLE_ROCE_PFC_METRICS", "false")

	cfg, err := Parse([]string{"--enable-roce-pfc-metrics=true"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if !cfg.EnableRoCEPFCMetrics {
		t.Fatalf("expected RoCE PFC metrics to be enabled by flag")
	}
}

func TestRoCEPFCMetricsToggleRejectsInvalidEnv(t *testing.T) {
	t.Setenv("RDMA_EXPORTER_ENABLE_ROCE_PFC_METRICS", "notabool")

	if _, err := Parse(nil); err == nil {
		t.Fatalf("expected error for invalid RDMA_EXPORTER_ENABLE_ROCE_PFC_METRICS")
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

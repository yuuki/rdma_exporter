package mlxlinkexporter

import (
	"log/slog"
	"testing"
	"time"
)

// exporterEnvVars lists every environment variable Parse consults.
var exporterEnvVars = []string{
	"MLXLINK_EXPORTER_LISTEN_ADDRESS",
	"MLXLINK_EXPORTER_METRICS_PATH",
	"MLXLINK_EXPORTER_HEALTH_PATH",
	"MLXLINK_EXPORTER_READY_PATH",
	"MLXLINK_EXPORTER_LOG_LEVEL",
	"MLXLINK_EXPORTER_MLXLINK_PATH",
	"MLXLINK_EXPORTER_SYSFS_ROOT",
	"MLXLINK_EXPORTER_EXCLUDE_DEVICES",
	"MLXLINK_EXPORTER_POLL_INTERVAL",
	"MLXLINK_EXPORTER_COMMAND_TIMEOUT",
	"MLXLINK_EXPORTER_SHOW_EYE",
	"MLXLINK_EXPORTER_SHOW_PCIE_EYE",
}

// clearExporterEnv neutralises ambient MLXLINK_EXPORTER_* variables so tests
// observe the built-in defaults regardless of the developer environment.
func clearExporterEnv(t *testing.T) {
	t.Helper()
	for _, key := range exporterEnvVars {
		t.Setenv(key, "")
	}
}

func TestParse_Defaults(t *testing.T) {
	clearExporterEnv(t)

	cfg, err := Parse(nil)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.ListenAddress != defaultListenAddress {
		t.Errorf("expected listen address %q, got %q", defaultListenAddress, cfg.ListenAddress)
	}
	if cfg.MetricsPath != defaultMetricsPath {
		t.Errorf("expected metrics path %q, got %q", defaultMetricsPath, cfg.MetricsPath)
	}
	if cfg.HealthPath != defaultHealthPath {
		t.Errorf("expected health path %q, got %q", defaultHealthPath, cfg.HealthPath)
	}
	if cfg.ReadyPath != defaultReadyPath {
		t.Errorf("expected ready path %q, got %q", defaultReadyPath, cfg.ReadyPath)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("expected log level info, got %v", cfg.LogLevel)
	}
	if cfg.MlxlinkPath != defaultMlxlinkPath {
		t.Errorf("expected mlxlink path %q, got %q", defaultMlxlinkPath, cfg.MlxlinkPath)
	}
	if cfg.SysfsRoot != defaultSysfsRoot {
		t.Errorf("expected sysfs root %q, got %q", defaultSysfsRoot, cfg.SysfsRoot)
	}
	if cfg.PollInterval != defaultPollInterval {
		t.Errorf("expected poll interval %v, got %v", defaultPollInterval, cfg.PollInterval)
	}
	if cfg.CommandTimeout != defaultCommandTimeout {
		t.Errorf("expected command timeout %v, got %v", defaultCommandTimeout, cfg.CommandTimeout)
	}
	if cfg.ExcludeDevices != nil {
		t.Errorf("expected nil excluded devices, got %v", cfg.ExcludeDevices)
	}
	if cfg.ShowVersion {
		t.Errorf("expected show version to be false by default")
	}
	if cfg.ShowEye {
		t.Errorf("expected show eye to be false by default")
	}
	if cfg.ShowPCIeEye {
		t.Errorf("expected show PCIe eye to be false by default")
	}
	if got, want := cfg.StaleAfter(), defaultPollInterval*staleAfterFactor; got != want {
		t.Errorf("expected stale after %v, got %v", want, got)
	}
}

func TestParse_EnvOverridesDefaults(t *testing.T) {
	clearExporterEnv(t)
	t.Setenv("MLXLINK_EXPORTER_LISTEN_ADDRESS", "127.0.0.1:9999")
	t.Setenv("MLXLINK_EXPORTER_METRICS_PATH", "/env-metrics")
	t.Setenv("MLXLINK_EXPORTER_HEALTH_PATH", "/env-health")
	t.Setenv("MLXLINK_EXPORTER_READY_PATH", "/env-ready")
	t.Setenv("MLXLINK_EXPORTER_LOG_LEVEL", "debug")
	t.Setenv("MLXLINK_EXPORTER_MLXLINK_PATH", "/opt/mft/bin/mlxlink")
	t.Setenv("MLXLINK_EXPORTER_SYSFS_ROOT", "/fake/sys")
	t.Setenv("MLXLINK_EXPORTER_EXCLUDE_DEVICES", "mlx5_3")
	t.Setenv("MLXLINK_EXPORTER_POLL_INTERVAL", "45s")
	t.Setenv("MLXLINK_EXPORTER_COMMAND_TIMEOUT", "7s")
	t.Setenv("MLXLINK_EXPORTER_SHOW_EYE", "true")
	t.Setenv("MLXLINK_EXPORTER_SHOW_PCIE_EYE", "1")

	cfg, err := Parse(nil)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.ListenAddress != "127.0.0.1:9999" {
		t.Errorf("expected listen address from env, got %q", cfg.ListenAddress)
	}
	if cfg.MetricsPath != "/env-metrics" {
		t.Errorf("expected metrics path from env, got %q", cfg.MetricsPath)
	}
	if cfg.HealthPath != "/env-health" {
		t.Errorf("expected health path from env, got %q", cfg.HealthPath)
	}
	if cfg.ReadyPath != "/env-ready" {
		t.Errorf("expected ready path from env, got %q", cfg.ReadyPath)
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("expected log level debug from env, got %v", cfg.LogLevel)
	}
	if cfg.MlxlinkPath != "/opt/mft/bin/mlxlink" {
		t.Errorf("expected mlxlink path from env, got %q", cfg.MlxlinkPath)
	}
	if cfg.SysfsRoot != "/fake/sys" {
		t.Errorf("expected sysfs root from env, got %q", cfg.SysfsRoot)
	}
	if len(cfg.ExcludeDevices) != 1 || cfg.ExcludeDevices[0] != "mlx5_3" {
		t.Errorf("expected [mlx5_3] from env, got %v", cfg.ExcludeDevices)
	}
	if cfg.PollInterval != 45*time.Second {
		t.Errorf("expected poll interval 45s from env, got %v", cfg.PollInterval)
	}
	if cfg.CommandTimeout != 7*time.Second {
		t.Errorf("expected command timeout 7s from env, got %v", cfg.CommandTimeout)
	}
	if !cfg.ShowEye {
		t.Errorf("expected show eye from env")
	}
	if !cfg.ShowPCIeEye {
		t.Errorf("expected show PCIe eye from env")
	}
	if got, want := cfg.StaleAfter(), 45*time.Second*staleAfterFactor; got != want {
		t.Errorf("expected stale after %v, got %v", want, got)
	}
}

func TestParse_FlagsOverrideEnv(t *testing.T) {
	clearExporterEnv(t)
	t.Setenv("MLXLINK_EXPORTER_LISTEN_ADDRESS", "127.0.0.1:9999")
	t.Setenv("MLXLINK_EXPORTER_MLXLINK_PATH", "/opt/mft/bin/mlxlink")
	t.Setenv("MLXLINK_EXPORTER_POLL_INTERVAL", "45s")
	t.Setenv("MLXLINK_EXPORTER_COMMAND_TIMEOUT", "7s")
	t.Setenv("MLXLINK_EXPORTER_SHOW_EYE", "true")
	t.Setenv("MLXLINK_EXPORTER_SHOW_PCIE_EYE", "true")

	cfg, err := Parse([]string{
		"--listen-address", "0.0.0.0:1234",
		"--mlxlink-path", "/usr/local/bin/mlxlink",
		"--poll-interval", "10s",
		"--command-timeout", "2s",
		"--show-eye=false",
		"--show-pcie-eye=false",
	})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.ListenAddress != "0.0.0.0:1234" {
		t.Errorf("expected listen address from flag, got %q", cfg.ListenAddress)
	}
	if cfg.MlxlinkPath != "/usr/local/bin/mlxlink" {
		t.Errorf("expected mlxlink path from flag, got %q", cfg.MlxlinkPath)
	}
	if cfg.PollInterval != 10*time.Second {
		t.Errorf("expected poll interval 10s from flag, got %v", cfg.PollInterval)
	}
	if cfg.CommandTimeout != 2*time.Second {
		t.Errorf("expected command timeout 2s from flag, got %v", cfg.CommandTimeout)
	}
	if cfg.ShowEye {
		t.Errorf("expected CLI to disable show eye")
	}
	if cfg.ShowPCIeEye {
		t.Errorf("expected CLI to disable show PCIe eye")
	}
}

func TestParse_ShowEyeFlags(t *testing.T) {
	clearExporterEnv(t)

	cfg, err := Parse([]string{"--show-eye", "--show-pcie-eye"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if !cfg.ShowEye {
		t.Errorf("expected --show-eye to enable network eye collection")
	}
	if !cfg.ShowPCIeEye {
		t.Errorf("expected --show-pcie-eye to enable PCIe eye collection")
	}
}

func TestParse_InvalidBooleanFromEnv(t *testing.T) {
	for _, key := range []string{"MLXLINK_EXPORTER_SHOW_EYE", "MLXLINK_EXPORTER_SHOW_PCIE_EYE"} {
		t.Run(key, func(t *testing.T) {
			clearExporterEnv(t)
			t.Setenv(key, "sometimes")

			if _, err := Parse(nil); err == nil {
				t.Fatalf("expected invalid boolean error for %s", key)
			}
		})
	}
}

func TestParse_InvalidLogLevel(t *testing.T) {
	clearExporterEnv(t)

	if _, err := Parse([]string{"--log-level", "verbose"}); err == nil {
		t.Fatalf("expected error for invalid log level")
	}
}

func TestParse_InvalidDurationFromEnv(t *testing.T) {
	tests := []struct {
		name string
		key  string
	}{
		{"poll interval", "MLXLINK_EXPORTER_POLL_INTERVAL"},
		{"command timeout", "MLXLINK_EXPORTER_COMMAND_TIMEOUT"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearExporterEnv(t)
			t.Setenv(tt.key, "notaduration")

			if _, err := Parse(nil); err == nil {
				t.Fatalf("expected error for invalid %s", tt.key)
			}
		})
	}
}

func TestParse_RejectsNonPositivePollInterval(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"zero", []string{"--poll-interval", "0s"}},
		{"negative", []string{"--poll-interval", "-1s"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearExporterEnv(t)

			if _, err := Parse(tt.args); err == nil {
				t.Fatalf("expected error for poll interval %v", tt.args)
			}
		})
	}
}

func TestParse_RejectsNonPositiveCommandTimeout(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"zero", []string{"--command-timeout", "0s"}},
		{"negative", []string{"--command-timeout", "-1s"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearExporterEnv(t)

			if _, err := Parse(tt.args); err == nil {
				t.Fatalf("expected error for command timeout %v", tt.args)
			}
		})
	}
}

func TestParse_ExcludeDevicesFromFlag(t *testing.T) {
	clearExporterEnv(t)

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

func TestParse_ExcludeDevicesFromEnv(t *testing.T) {
	clearExporterEnv(t)
	t.Setenv("MLXLINK_EXPORTER_EXCLUDE_DEVICES", "mlx5_0, mlx5_2 ")

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

func TestParse_VersionFlag(t *testing.T) {
	clearExporterEnv(t)

	cfg, err := Parse([]string{"--version"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if !cfg.ShowVersion {
		t.Fatalf("expected show version to be true when flag is set")
	}
}

func TestConfig_StaleAfter(t *testing.T) {
	t.Parallel()

	cfg := Config{PollInterval: 12 * time.Second}
	if got, want := cfg.StaleAfter(), 60*time.Second; got != want {
		t.Fatalf("expected stale after %v, got %v", want, got)
	}
}

func TestParseLogLevel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input   string
		want    slog.Level
		wantErr bool
	}{
		{input: "debug", want: slog.LevelDebug},
		{input: "INFO", want: slog.LevelInfo},
		{input: "", want: slog.LevelInfo},
		{input: " warn ", want: slog.LevelWarn},
		{input: "warning", want: slog.LevelWarn},
		{input: "error", want: slog.LevelError},
		{input: "err", want: slog.LevelError},
		{input: "trace", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseLogLevel(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseLogLevel(%q) expected error", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseLogLevel(%q) returned error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("parseLogLevel(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
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

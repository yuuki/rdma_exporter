package mlxlinkexporter

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"log/slog"
)

const (
	defaultListenAddress  = ":9880"
	defaultMetricsPath    = "/metrics"
	defaultHealthPath     = "/healthz"
	defaultReadyPath      = "/readyz"
	defaultLogLevel       = "info"
	defaultMlxlinkPath    = "/usr/bin/mlxlink"
	defaultSysfsRoot      = "/sys"
	defaultPollInterval   = 30 * time.Second
	defaultCommandTimeout = 3 * time.Second

	// staleAfterFactor derives the staleness horizon from the poll interval: a
	// cached snapshot survives this many consecutive failed sweeps before its
	// device metrics stop being exported.
	staleAfterFactor = 5
)

// Config captures runtime configuration options.
type Config struct {
	ListenAddress  string
	MetricsPath    string
	HealthPath     string
	ReadyPath      string
	LogLevel       slog.Level
	MlxlinkPath    string
	SysfsRoot      string
	PollInterval   time.Duration
	CommandTimeout time.Duration
	ExcludeDevices []string
	ShowEye        bool
	ShowPCIeEye    bool
	ShowVersion    bool
}

// StaleAfter returns how long a cached device snapshot remains exportable after
// its last successful collection.
func (c Config) StaleAfter() time.Duration {
	return c.PollInterval * staleAfterFactor
}

// Parse constructs a Config from command-line flags and environment variables.
func Parse(args []string) (Config, error) {
	var cfg Config

	fs := flag.NewFlagSet("mlxlink_exporter", flag.ContinueOnError)
	// Reporting failures is the caller's job: letting the flag package print
	// too would show syntax errors twice while validation errors below are
	// printed once. Help is reprinted explicitly when it is asked for.
	fs.SetOutput(io.Discard)

	listen := fs.String("listen-address", envOrDefault("MLXLINK_EXPORTER_LISTEN_ADDRESS", defaultListenAddress), "Address to listen on for HTTP requests.")
	metricsPath := fs.String("metrics-path", envOrDefault("MLXLINK_EXPORTER_METRICS_PATH", defaultMetricsPath), "HTTP path under which metrics are served.")
	healthPath := fs.String("health-path", envOrDefault("MLXLINK_EXPORTER_HEALTH_PATH", defaultHealthPath), "HTTP path for liveness checks.")
	readyPath := fs.String("ready-path", envOrDefault("MLXLINK_EXPORTER_READY_PATH", defaultReadyPath), "HTTP path for readiness checks.")
	logLevel := fs.String("log-level", envOrDefault("MLXLINK_EXPORTER_LOG_LEVEL", defaultLogLevel), "Log level (debug, info, warn, error).")
	mlxlinkPath := fs.String("mlxlink-path", envOrDefault("MLXLINK_EXPORTER_MLXLINK_PATH", defaultMlxlinkPath), "Path to the mlxlink binary.")
	sysfsRoot := fs.String("sysfs-root", envOrDefault("MLXLINK_EXPORTER_SYSFS_ROOT", defaultSysfsRoot), "Root of the sysfs tree used to discover RDMA devices.")
	excludeDevices := fs.String("exclude-devices", envOrDefault("MLXLINK_EXPORTER_EXCLUDE_DEVICES", ""), "Comma-separated list of RDMA devices to exclude from monitoring (e.g., mlx5_0,mlx5_1).")
	showEyeDefault, err := envBoolOrDefault("MLXLINK_EXPORTER_SHOW_EYE", false)
	if err != nil {
		return cfg, err
	}
	showPCIeEyeDefault, err := envBoolOrDefault("MLXLINK_EXPORTER_SHOW_PCIE_EYE", false)
	if err != nil {
		return cfg, err
	}
	showEye := fs.Bool("show-eye", showEyeDefault, "Collect network-port Eye telemetry with mlxlink --show_eye.")
	showPCIeEye := fs.Bool("show-pcie-eye", showPCIeEyeDefault, "Collect root PCIe Eye telemetry with a separate mlxlink query.")

	pollIntervalDefault := defaultPollInterval
	if envInterval := os.Getenv("MLXLINK_EXPORTER_POLL_INTERVAL"); envInterval != "" {
		parsed, err := time.ParseDuration(envInterval)
		if err != nil {
			return cfg, fmt.Errorf("invalid MLXLINK_EXPORTER_POLL_INTERVAL: %w", err)
		}
		pollIntervalDefault = parsed
	}
	pollInterval := fs.Duration("poll-interval", pollIntervalDefault, "Interval between background collection sweeps over all discovered devices.")

	commandTimeoutDefault := defaultCommandTimeout
	if envTimeout := os.Getenv("MLXLINK_EXPORTER_COMMAND_TIMEOUT"); envTimeout != "" {
		parsed, err := time.ParseDuration(envTimeout)
		if err != nil {
			return cfg, fmt.Errorf("invalid MLXLINK_EXPORTER_COMMAND_TIMEOUT: %w", err)
		}
		commandTimeoutDefault = parsed
	}
	commandTimeout := fs.Duration("command-timeout", commandTimeoutDefault, "Maximum duration of a single mlxlink invocation.")

	showVersion := fs.Bool("version", false, "Print version information and exit.")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			// The usage text was discarded above; a user who asked for help
			// still expects it, on stdout.
			fs.SetOutput(os.Stdout)
			fs.Usage()
			return cfg, err
		}
		return cfg, fmt.Errorf("parse flags: %w", err)
	}

	level, err := parseLogLevel(*logLevel)
	if err != nil {
		return cfg, err
	}

	if *pollInterval <= 0 {
		return cfg, fmt.Errorf("invalid poll-interval %v: must be positive", *pollInterval)
	}
	if *commandTimeout <= 0 {
		return cfg, fmt.Errorf("invalid command-timeout %v: must be positive", *commandTimeout)
	}

	cfg = Config{
		ListenAddress:  *listen,
		MetricsPath:    *metricsPath,
		HealthPath:     *healthPath,
		ReadyPath:      *readyPath,
		LogLevel:       level,
		MlxlinkPath:    *mlxlinkPath,
		SysfsRoot:      *sysfsRoot,
		PollInterval:   *pollInterval,
		CommandTimeout: *commandTimeout,
		ExcludeDevices: parseDeviceList(*excludeDevices),
		ShowEye:        *showEye,
		ShowPCIeEye:    *showPCIeEye,
		ShowVersion:    *showVersion,
	}
	return cfg, nil
}

func envBoolOrDefault(key string, fallback bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("invalid %s: %w", key, err)
	}
	return parsed, nil
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func parseLogLevel(value string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error", "err":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("invalid log level %q", value)
	}
}

func parseDeviceList(list string) []string {
	if list == "" {
		return nil
	}
	parts := strings.Split(list, ",")
	devices := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			devices = append(devices, trimmed)
		}
	}
	return devices
}

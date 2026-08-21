package config

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"log/slog"
)

const (
	defaultListenAddress          = ":9879"
	defaultMetricsPath            = "/metrics"
	defaultHealthPath             = "/healthz"
	defaultLogLevel               = "info"
	defaultSysfsRoot              = "/sys"
	defaultTimeout                = 5 * time.Second
	defaultEnableRoCEPFC          = true
	defaultEnableNetDevHW         = false
	defaultEnableOptionalCounters = false
	defaultEnableQPCounters       = false
)

// Config captures runtime configuration options.
type Config struct {
	ListenAddress          string
	MetricsPath            string
	HealthPath             string
	LogLevel               slog.Level
	SysfsRoot              string
	ScrapeTimeout          time.Duration
	EnableRoCEPFCMetrics   bool
	EnableNetDevHWMetrics  bool
	EnableOptionalCounters bool
	EnableQPCounters       bool
	ExcludeDevices         []string
	ShowVersion            bool
}

// Parse constructs a Config from command-line flags and environment variables.
func Parse(args []string) (Config, error) {
	var cfg Config

	fs := flag.NewFlagSet("rdma_exporter", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	listen := fs.String("listen-address", envOrDefault("RDMA_EXPORTER_LISTEN_ADDRESS", defaultListenAddress), "Address to listen on for HTTP requests.")
	metricsPath := fs.String("metrics-path", envOrDefault("RDMA_EXPORTER_METRICS_PATH", defaultMetricsPath), "HTTP path under which metrics are served.")
	healthPath := fs.String("health-path", envOrDefault("RDMA_EXPORTER_HEALTH_PATH", defaultHealthPath), "HTTP path for health checks.")
	logLevel := fs.String("log-level", envOrDefault("RDMA_EXPORTER_LOG_LEVEL", defaultLogLevel), "Log level (debug, info, warn, error).")
	sysfsRoot := fs.String("sysfs-root", envOrDefault("RDMA_EXPORTER_SYSFS_ROOT", defaultSysfsRoot), "Root of the sysfs tree to read RDMA data from.")
	excludeDevices := fs.String("exclude-devices", envOrDefault("RDMA_EXPORTER_EXCLUDE_DEVICES", ""), "Comma-separated list of RDMA devices to exclude from monitoring (e.g., mlx5_0,mlx5_1).")

	enableRoCEPFCDefault, err := boolEnvOrDefault("RDMA_EXPORTER_ENABLE_ROCE_PFC_METRICS", defaultEnableRoCEPFC)
	if err != nil {
		return cfg, err
	}
	enableRoCEPFCMetrics := fs.Bool("enable-roce-pfc-metrics", enableRoCEPFCDefault, "Enable collection of RoCEv2 PFC metrics from netdev ethtool stats.")

	enableNetDevHWDefault, err := boolEnvOrDefault("RDMA_EXPORTER_ENABLE_NETDEV_HW_METRICS", defaultEnableNetDevHW)
	if err != nil {
		return cfg, err
	}
	enableNetDevHWMetrics := fs.Bool("enable-netdev-hw-metrics", enableNetDevHWDefault, "Enable collection of netdev ethtool hardware counters (buffer, PCIe, PHY/FEC, IEEE 802.3x global pause, pause storm, vport RDMA). Linux only, opt-in.")

	enableOptionalDefault, err := boolEnvOrDefault("RDMA_EXPORTER_ENABLE_RDMA_OPTIONAL_COUNTERS", defaultEnableOptionalCounters)
	if err != nil {
		return cfg, err
	}
	enableOptionalCounters := fs.Bool("enable-rdma-optional-counters", enableOptionalDefault, "Enable optional RDMA hardware counters (mlx5 cc_* and rdma_{rx,tx}_{bytes,packets}) via NETLINK_RDMA. The exporter never enables counters; use rdma statistic set.")

	enableQPDefault, err := boolEnvOrDefault("RDMA_EXPORTER_ENABLE_RDMA_QP_COUNTERS", defaultEnableQPCounters)
	if err != nil {
		return cfg, err
	}
	enableQPCounters := fs.Bool("enable-rdma-qp-counters", enableQPDefault, "Enable live auto-type QP counters via NETLINK_RDMA GET/DUMP. The exporter never binds QPs or enables auto mode; use rdma statistic qp set.")

	timeoutDefault := defaultTimeout
	if envTimeout := os.Getenv("RDMA_EXPORTER_SCRAPE_TIMEOUT"); envTimeout != "" {
		parsed, err := time.ParseDuration(envTimeout)
		if err != nil {
			return cfg, fmt.Errorf("invalid RDMA_EXPORTER_SCRAPE_TIMEOUT: %w", err)
		}
		timeoutDefault = parsed
	}
	scrapeTimeout := fs.Duration("scrape-timeout", timeoutDefault, "Maximum duration to spend gathering metrics per scrape.")
	showVersion := fs.Bool("version", false, "Print version information and exit.")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return cfg, err
		}
		return cfg, fmt.Errorf("parse flags: %w", err)
	}

	level, err := parseLogLevel(*logLevel)
	if err != nil {
		return cfg, err
	}

	cfg = Config{
		ListenAddress:          *listen,
		MetricsPath:            *metricsPath,
		HealthPath:             *healthPath,
		LogLevel:               level,
		SysfsRoot:              *sysfsRoot,
		ScrapeTimeout:          *scrapeTimeout,
		EnableRoCEPFCMetrics:   *enableRoCEPFCMetrics,
		EnableNetDevHWMetrics:  *enableNetDevHWMetrics,
		EnableOptionalCounters: *enableOptionalCounters,
		EnableQPCounters:       *enableQPCounters,
		ExcludeDevices:         parseDeviceList(*excludeDevices),
		ShowVersion:            *showVersion,
	}
	return cfg, nil
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func boolEnvOrDefault(key string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback, fmt.Errorf("invalid %s: %w", key, err)
	}
	return parsed, nil
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

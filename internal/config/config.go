package config

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"log/slog"
)

const (
	defaultListenAddress = ":9879"
	defaultMetricsPath   = "/metrics"
	defaultHealthPath    = "/healthz"
	defaultLogLevel      = "info"
	defaultSysfsRoot     = "/sys"
	defaultTimeout       = 5 * time.Second
)

// Config captures runtime configuration options.
type Config struct {
	ListenAddress string
	MetricsPath   string
	HealthPath    string
	LogLevel      slog.Level
	SysfsRoot     string
	ScrapeTimeout time.Duration
	ShowVersion   bool
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
		ListenAddress: *listen,
		MetricsPath:   *metricsPath,
		HealthPath:    *healthPath,
		LogLevel:      level,
		SysfsRoot:     *sysfsRoot,
		ScrapeTimeout: *scrapeTimeout,
		ShowVersion:   *showVersion,
	}
	return cfg, nil
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

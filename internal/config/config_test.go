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

func defaultLogLevelValue() slog.Level {
	lvl, _ := parseLogLevel(defaultLogLevel)
	return lvl
}

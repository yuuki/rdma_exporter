package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/yuuki/rdma_exporter/internal/collector"
	"github.com/yuuki/rdma_exporter/internal/config"
	"github.com/yuuki/rdma_exporter/internal/netdev"
	"github.com/yuuki/rdma_exporter/internal/rdma"
	"github.com/yuuki/rdma_exporter/internal/rdmanl"
	"github.com/yuuki/rdma_exporter/internal/server"
)

var (
	version = "0.7.4"
	commit  = "unknown"
)

func main() {
	cfg, err := config.Parse(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		// flag package already printed the error to stderr.
		os.Exit(2)
	}

	if cfg.ShowVersion {
		fmt.Printf("rdma_exporter v%s\ncommit: %s\nbuilt with: %s\n", version, commit, runtime.Version())
		os.Exit(0)
	}

	logger := newLogger(cfg.LogLevel)
	logger.Info("starting prometheus rdma exporter",
		"listen_address", cfg.ListenAddress,
		"metrics_path", cfg.MetricsPath,
		"health_path", cfg.HealthPath,
		"scrape_timeout", cfg.ScrapeTimeout.String(),
		"sysfs_root", cfg.SysfsRoot,
		"collector_ethtool", cfg.CollectorEthtool,
		"collector_optional_counters", cfg.CollectorOptionalCounters,
		"collector_qp_counters", cfg.CollectorQPCounters,
	)

	provider := newSysfsProvider(cfg, logger)
	collectorOpts, providers := newCollectorOptions(cfg, logger)

	rdmaCollector := collector.New(provider, logger, collectorOpts...)
	probeNetDevStats(rdmaCollector, cfg.ScrapeTimeout, logger)

	registry := prometheus.NewRegistry()
	registry.MustRegister(
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		collectors.NewGoCollector(),
		rdmaCollector,
	)

	srv := server.New(server.Options{
		ListenAddress: cfg.ListenAddress,
		MetricsPath:   cfg.MetricsPath,
		HealthPath:    cfg.HealthPath,
		ScrapeTimeout: cfg.ScrapeTimeout,
	}, registry, rdmaCollector, logger)

	if serveErr := serveUntilSignal(srv, logger); serveErr != nil {
		logger.Error("server exited with error", "err", serveErr)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("graceful shutdown failed", "err", err)
		os.Exit(1)
	}
	providers.close(logger)

	logger.Info("shutdown complete")
}

// newSysfsProvider builds the sysfs provider from the sysfs root and device
// exclusion settings.
func newSysfsProvider(cfg config.Config, logger *slog.Logger) *rdma.SysfsProvider {
	provider := rdma.NewSysfsProvider()
	if cfg.SysfsRoot != "" {
		provider.SetSysfsRoot(cfg.SysfsRoot)
	}
	if len(cfg.ExcludeDevices) > 0 {
		provider.SetExcludeDevices(cfg.ExcludeDevices)
		logger.Info("excluding devices from monitoring", "devices", cfg.ExcludeDevices)
	}
	return provider
}

// collectorProviders holds the providers the enabled collectors opened; main
// closes them on shutdown. A nil field is a collector that is disabled or
// whose provider failed to open.
type collectorProviders struct {
	ethtool  *netdev.EthtoolStatsProvider
	optional *rdmanl.Provider
	qp       *rdmanl.Provider
}

// newCollectorOptions turns the collector flags into collector options. An
// enabled collector whose provider fails to open is logged and still marked
// enabled, so it reports rdma_scrape_collector_success 0.
func newCollectorOptions(cfg config.Config, logger *slog.Logger) ([]collector.Option, collectorProviders) {
	opts := make([]collector.Option, 0, 5)
	var providers collectorProviders

	if cfg.CollectorEthtool {
		opts = append(opts, collector.WithEthtoolCollector())
		providers.ethtool = newEthtoolStatsProvider(logger)
		if providers.ethtool != nil {
			opts = append(opts, collector.WithNetDevStatsProvider(providers.ethtool))
		}
	}

	if cfg.CollectorOptionalCounters {
		opts = append(opts, collector.WithOptionalCollector())
		providers.optional = newNetlinkProvider(logger, "failed to initialize optional RDMA counter provider; optional counters are disabled")
		if providers.optional != nil {
			opts = append(opts, collector.WithOptionalCounterProvider(providers.optional))
		}
	}

	if cfg.CollectorQPCounters {
		opts = append(opts, collector.WithQPCollector())
		providers.qp = newNetlinkProvider(logger, "failed to initialize QP counter provider; QP counters are disabled")
		if providers.qp != nil {
			opts = append(opts, collector.WithQPCounterProvider(providers.qp))
		}
	}

	return opts, providers
}

// newEthtoolStatsProvider opens the ethtool client, or logs why it could not
// and returns nil.
func newEthtoolStatsProvider(logger *slog.Logger) *netdev.EthtoolStatsProvider {
	provider, err := netdev.NewEthtoolStatsProvider()
	if err != nil {
		logger.Warn("failed to initialize netdev ethtool stats provider; ethtool metrics are disabled", "err", err)
		return nil
	}
	return provider
}

// newNetlinkProvider opens a NETLINK_RDMA provider, or logs warnMsg and
// returns nil.
func newNetlinkProvider(logger *slog.Logger, warnMsg string) *rdmanl.Provider {
	provider, err := rdmanl.New()
	if err != nil {
		logger.Warn(warnMsg, "err", err)
		return nil
	}
	return provider
}

// close closes the opened providers, logging failures.
func (p collectorProviders) close(logger *slog.Logger) {
	if p.ethtool != nil {
		if err := p.ethtool.Close(); err != nil {
			logger.Warn("failed to close netdev ethtool stats provider", "err", err)
		}
	}
	if p.optional != nil {
		if err := p.optional.Close(); err != nil {
			logger.Warn("failed to close optional RDMA counter provider", "err", err)
		}
	}
	if p.qp != nil {
		if err := p.qp.Close(); err != nil {
			logger.Warn("failed to close QP counter provider", "err", err)
		}
	}
}

// probeNetDevStats runs the one-time ethtool capability probe, bounded by the
// scrape timeout.
func probeNetDevStats(rdmaCollector *collector.RdmaCollector, timeout time.Duration, logger *slog.Logger) {
	probeCtx, probeCancel := context.WithTimeout(context.Background(), timeout)
	defer probeCancel()
	if err := rdmaCollector.ProbeNetDevStats(probeCtx); err != nil {
		logger.Warn("netdev capability probe failed", "err", err)
	}
}

// serveUntilSignal serves until SIGINT or SIGTERM arrives, which it logs and
// returns nil for, or returns the error the server stopped with.
func serveUntilSignal(srv *server.Server, logger *slog.Logger) error {
	errCh := make(chan error, 1)
	go func() {
		if serveErr := srv.ListenAndServe(); serveErr != nil {
			errCh <- serveErr
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		logger.Info("signal received, shutting down", "signal", sig.String())
		return nil
	case serveErr := <-errCh:
		return serveErr
	}
}

func newLogger(level slog.Level) *slog.Logger {
	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	return slog.New(handler)
}

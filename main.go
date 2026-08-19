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
	version = "0.5.0"
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
		"enable_roce_pfc_metrics", cfg.EnableRoCEPFCMetrics,
		"enable_netdev_hw_metrics", cfg.EnableNetDevHWMetrics,
		"enable_rdma_optional_counters", cfg.EnableOptionalCounters,
		"enable_rdma_qp_counters", cfg.EnableQPCounters,
	)

	provider := rdma.NewSysfsProvider()
	if cfg.SysfsRoot != "" {
		provider.SetSysfsRoot(cfg.SysfsRoot)
	}
	if len(cfg.ExcludeDevices) > 0 {
		provider.SetExcludeDevices(cfg.ExcludeDevices)
		logger.Info("excluding devices from monitoring", "devices", cfg.ExcludeDevices)
	}

	collectorOpts := make([]collector.Option, 0, 5)
	var ethtoolProvider *netdev.EthtoolStatsProvider
	if cfg.EnableRoCEPFCMetrics || cfg.EnableNetDevHWMetrics {
		ethtoolStatsProvider, err := netdev.NewEthtoolStatsProvider()
		if err != nil {
			logger.Warn("failed to initialize netdev ethtool stats provider; netdev metrics are disabled", "err", err)
		} else {
			ethtoolProvider = ethtoolStatsProvider
			collectorOpts = append(collectorOpts, collector.WithNetDevStatsProvider(ethtoolStatsProvider))
			if !cfg.EnableRoCEPFCMetrics {
				collectorOpts = append(collectorOpts, collector.WithRoCEPFCMetrics(false))
			}
			if cfg.EnableNetDevHWMetrics {
				collectorOpts = append(collectorOpts, collector.WithNetDevHWMetrics(true))
			}
		}
	}

	var optionalProvider *rdmanl.Provider
	if cfg.EnableOptionalCounters {
		optionalCounters, err := rdmanl.New()
		if err != nil {
			logger.Warn("failed to initialize optional RDMA counter provider; optional counters are disabled", "err", err)
		} else {
			optionalProvider = optionalCounters
			collectorOpts = append(collectorOpts, collector.WithOptionalCounterProvider(optionalCounters))
		}
	}

	var qpProvider *rdmanl.Provider
	if cfg.EnableQPCounters {
		qpCounters, err := rdmanl.New()
		if err != nil {
			logger.Warn("failed to initialize QP counter provider; QP counters are disabled", "err", err)
		} else {
			qpProvider = qpCounters
			collectorOpts = append(collectorOpts, collector.WithQPCounterProvider(qpCounters))
		}
	}

	rdmaCollector := collector.New(provider, logger, collectorOpts...)

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
	case serveErr := <-errCh:
		logger.Error("server exited with error", "err", serveErr)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("graceful shutdown failed", "err", err)
		os.Exit(1)
	}
	if ethtoolProvider != nil {
		if err := ethtoolProvider.Close(); err != nil {
			logger.Warn("failed to close netdev ethtool stats provider", "err", err)
		}
	}
	if optionalProvider != nil {
		if err := optionalProvider.Close(); err != nil {
			logger.Warn("failed to close optional RDMA counter provider", "err", err)
		}
	}
	if qpProvider != nil {
		if err := qpProvider.Close(); err != nil {
			logger.Warn("failed to close QP counter provider", "err", err)
		}
	}

	logger.Info("shutdown complete")
}

func newLogger(level slog.Level) *slog.Logger {
	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	return slog.New(handler)
}

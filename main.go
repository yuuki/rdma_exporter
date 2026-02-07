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

	"github.com/yuuki/rdma_exporter/internal/collector"
	"github.com/yuuki/rdma_exporter/internal/config"
	"github.com/yuuki/rdma_exporter/internal/netdev"
	"github.com/yuuki/rdma_exporter/internal/rdma"
	"github.com/yuuki/rdma_exporter/internal/server"
)

var (
	version = "0.3.0"
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
	)

	provider := rdma.NewSysfsProvider()
	if cfg.SysfsRoot != "" {
		provider.SetSysfsRoot(cfg.SysfsRoot)
	}
	if len(cfg.ExcludeDevices) > 0 {
		provider.SetExcludeDevices(cfg.ExcludeDevices)
		logger.Info("excluding devices from monitoring", "devices", cfg.ExcludeDevices)
	}

	collectorOpts := make([]collector.Option, 0, 1)
	var ethtoolProvider *netdev.EthtoolStatsProvider
	if cfg.EnableRoCEPFCMetrics {
		ethtoolStatsProvider, err := netdev.NewEthtoolStatsProvider()
		if err != nil {
			logger.Warn("failed to initialize RoCE PFC stats provider; PFC metrics are disabled", "err", err)
		} else {
			ethtoolProvider = ethtoolStatsProvider
			collectorOpts = append(collectorOpts, collector.WithNetDevStatsProvider(ethtoolStatsProvider))
		}
	}

	rdmaCollector := collector.New(provider, logger, collectorOpts...)

	registry := prometheus.NewRegistry()
	registry.MustRegister(
		prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}),
		prometheus.NewGoCollector(),
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
			logger.Warn("failed to close RoCE PFC stats provider", "err", err)
		}
	}

	logger.Info("shutdown complete")
}

func newLogger(level slog.Level) *slog.Logger {
	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	return slog.New(handler)
}

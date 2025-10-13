package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/yuuki/prometheus-rdma-exporter/internal/collector"
	"github.com/yuuki/prometheus-rdma-exporter/internal/config"
	"github.com/yuuki/prometheus-rdma-exporter/internal/rdma"
	"github.com/yuuki/prometheus-rdma-exporter/internal/server"
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

	logger := newLogger(cfg.LogLevel)
	logger.Info("starting prometheus rdma exporter",
		"listen_address", cfg.ListenAddress,
		"metrics_path", cfg.MetricsPath,
		"health_path", cfg.HealthPath,
		"scrape_timeout", cfg.ScrapeTimeout.String(),
		"sysfs_root", cfg.SysfsRoot,
	)

	provider := rdma.NewSysfsProvider()
	if cfg.SysfsRoot != "" {
		provider.SetSysfsRoot(cfg.SysfsRoot)
	}

	rdmaCollector := collector.New(provider, logger)

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

	logger.Info("shutdown complete")
}

func newLogger(level slog.Level) *slog.Logger {
	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	return slog.New(handler)
}

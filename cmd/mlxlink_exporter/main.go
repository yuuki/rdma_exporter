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
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/yuuki/rdma_exporter/internal/mlxlink"
	"github.com/yuuki/rdma_exporter/internal/mlxlinkexporter"
)

var (
	version = "0.1.0"
	commit  = "unknown"
)

func main() {
	cfg, err := mlxlinkexporter.Parse(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		// Parse keeps quiet so that flag syntax errors and validation errors
		// are reported the same way, here.
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	if cfg.ShowVersion {
		fmt.Printf("mlxlink_exporter v%s\ncommit: %s\nbuilt with: %s\n", version, commit, runtime.Version())
		os.Exit(0)
	}

	logger := newLogger(cfg.LogLevel)

	// Fail fast: without the binary every sweep would do nothing but count
	// command_not_found errors.
	if _, err := os.Stat(cfg.MlxlinkPath); err != nil {
		logger.Error("mlxlink binary is not accessible", "mlxlink_path", cfg.MlxlinkPath, "err", err)
		os.Exit(1)
	}
	if cfg.CommandTimeout >= cfg.PollInterval {
		logger.Warn("command timeout is not shorter than the poll interval; one slow device can consume a whole sweep",
			"command_timeout", cfg.CommandTimeout.String(), "poll_interval", cfg.PollInterval.String())
	}

	logger.Info("starting prometheus mlxlink exporter",
		"version", version,
		"listen_address", cfg.ListenAddress,
		"metrics_path", cfg.MetricsPath,
		"health_path", cfg.HealthPath,
		"ready_path", cfg.ReadyPath,
		"mlxlink_path", cfg.MlxlinkPath,
		"sysfs_root", cfg.SysfsRoot,
		"poll_interval", cfg.PollInterval.String(),
		"command_timeout", cfg.CommandTimeout.String(),
		"stale_after", cfg.StaleAfter().String(),
		"show_eye", cfg.ShowEye,
		"show_pcie_eye", cfg.ShowPCIeEye,
	)
	if len(cfg.ExcludeDevices) > 0 {
		logger.Info("excluding devices from monitoring", "devices", cfg.ExcludeDevices)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	discovery := mlxlink.NewSysfsDiscovery(cfg.SysfsRoot, cfg.ExcludeDevices, logger)
	runner := mlxlink.NewExecRunner(cfg.MlxlinkPath, cfg.CommandTimeout, logger)
	poller := mlxlink.NewPoller(discovery, runner, cfg.PollInterval, logger,
		mlxlink.WithShowEye(cfg.ShowEye),
		mlxlink.WithShowPCIeEye(cfg.ShowPCIeEye),
	)
	mlxlinkCollector := mlxlink.NewCollector(poller, cfg.StaleAfter(), logger)

	registry := prometheus.NewRegistry()
	collectors := []prometheus.Collector{
		prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}),
		prometheus.NewGoCollector(),
		mlxlinkCollector,
		poller.Errors(),
	}
	if cfg.ShowPCIeEye {
		collectors = append(collectors,
			mlxlink.NewPCIeEyeCollector(poller, cfg.StaleAfter(), logger),
			poller.PCIeEyeErrors(),
		)
	}
	registry.MustRegister(collectors...)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		poller.Run(ctx)
	}()

	srv := mlxlinkexporter.New(mlxlinkexporter.Options{
		ListenAddress: cfg.ListenAddress,
		MetricsPath:   cfg.MetricsPath,
		HealthPath:    cfg.HealthPath,
		ReadyPath:     cfg.ReadyPath,
	}, registry, poller.Ready, logger)

	errCh := make(chan error, 1)
	go func() {
		if serveErr := srv.ListenAndServe(); serveErr != nil {
			errCh <- serveErr
		}
	}()

	exitCode := 0
	select {
	case <-ctx.Done():
		logger.Info("signal received, shutting down")
	case serveErr := <-errCh:
		logger.Error("server exited with error", "err", serveErr)
		exitCode = 1
	}

	// Both paths leave through the same door. Cancelling the context stops the
	// sweep loop and any mlxlink invocation in flight, so waiting for the
	// poller here is what keeps a failed server from orphaning a child
	// process. Shutdown is idempotent, so calling it after ListenAndServe
	// already failed is harmless.
	stop()
	wg.Wait()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "err", err)
		exitCode = 1
	}

	logger.Info("shutdown complete")
	os.Exit(exitCode)
}

func newLogger(level slog.Level) *slog.Logger {
	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	return slog.New(handler)
}

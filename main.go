package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	listenAddress = flag.String("web.listen-address", ":9315", "Address on which to expose metrics and web interface.")
	metricsPath   = flag.String("web.telemetry-path", "/metrics", "Path under which to expose metrics.")
	logLevel      = flag.String("log.level", "info", "Log level (debug, info, warn, error).")
	version       = "dev" // Set via ldflags during build
)

func main() {
	flag.Parse()

	// Setup logger
	logger := setupLogger(*logLevel)
	logger.Info("Starting RDMA Exporter", "version", version)

	// Create RDMA collector
	collector := NewRDMACollector(logger)

	// Register collector
	registry := prometheus.NewRegistry()
	if err := registry.Register(collector); err != nil {
		logger.Error("Failed to register collector", "error", err)
		os.Exit(1)
	}

	// Add process and Go metrics
	registry.MustRegister(prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
	registry.MustRegister(prometheus.NewGoCollector())

	// Setup HTTP server
	mux := http.NewServeMux()

	// Metrics endpoint
	mux.Handle(*metricsPath, promhttp.HandlerFor(registry, promhttp.HandlerOpts{
		ErrorLog:      slog.NewLogLogger(logger.Handler(), slog.LevelError),
		ErrorHandling: promhttp.ContinueOnError,
	}))

	// Landing page
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<html>
<head><title>RDMA Exporter</title></head>
<body>
<h1>RDMA Exporter</h1>
<p><a href="%s">Metrics</a></p>
<p>Version: %s</p>
</body>
</html>`, *metricsPath, version)
	})

	server := &http.Server{
		Addr:         *listenAddress,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Setup graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Start server in goroutine
	go func() {
		logger.Info("Listening on address", "address", *listenAddress)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("HTTP server error", "error", err)
			os.Exit(1)
		}
	}()

	// Wait for interrupt signal
	<-ctx.Done()
	logger.Info("Shutting down gracefully...")

	// Give outstanding requests a deadline for completion
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("Server shutdown error", "error", err)
		os.Exit(1)
	}

	logger.Info("Server stopped")
}

// setupLogger creates a structured logger with the specified level
func setupLogger(level string) *slog.Logger {
	var logLevel slog.Level

	switch level {
	case "debug":
		logLevel = slog.LevelDebug
	case "info":
		logLevel = slog.LevelInfo
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo
	}

	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	})

	return slog.New(handler)
}

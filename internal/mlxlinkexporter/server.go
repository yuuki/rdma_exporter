package mlxlinkexporter

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Options contains the configuration required to start the HTTP server.
type Options struct {
	ListenAddress string
	MetricsPath   string
	HealthPath    string
	ReadyPath     string
}

// Server exposes the exporter over HTTP. Scrapes are answered from the poller's
// cache, so none of the scrape timeout machinery the sysfs exporter needs
// applies here: gathering cannot block on a device.
type Server struct {
	httpServer *http.Server
	ready      func() bool
	logger     *slog.Logger
}

// New constructs a Server. ready reports whether the background poller has
// collected anything yet, which keeps the dependency on the poller to a single
// function value; a nil ready means always ready.
func New(opts Options, registry *prometheus.Registry, ready func() bool, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	if ready == nil {
		ready = func() bool { return true }
	}

	s := &Server{ready: ready, logger: logger}

	mux := http.NewServeMux()
	mux.Handle(opts.MetricsPath, promhttp.InstrumentMetricHandler(
		registry,
		promhttp.HandlerFor(registry, promhttp.HandlerOpts{}),
	))
	mux.HandleFunc(opts.HealthPath, s.handleHealth)
	mux.HandleFunc(opts.ReadyPath, s.handleReady)

	s.httpServer = &http.Server{
		Addr:              opts.ListenAddress,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return s
}

// ListenAndServe starts the HTTP server.
func (s *Server) ListenAndServe() error {
	err := s.httpServer.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown gracefully stops the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// handleHealth reports process liveness only: it stays 200 even when no device
// can be collected, so a broken adapter does not get the process restarted.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

// handleReady reports whether a scrape would return device data. A host with no
// RDMA devices never becomes ready.
func (s *Server) handleReady(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")

	if !s.ready() {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("not ready\n"))
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

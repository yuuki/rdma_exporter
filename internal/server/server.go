package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"

	"github.com/yuuki/prometheus-rdma-exporter/internal/collector"
)

// Options contains the configuration required to start the HTTP server.
type Options struct {
	ListenAddress string
	MetricsPath   string
	HealthPath    string
	ScrapeTimeout time.Duration
}

// Server wraps an http.Server with Prometheus-specific handlers.
type Server struct {
	httpServer    *http.Server
	registry      *prometheus.Registry
	collector     *collector.RdmaCollector
	logger        *slog.Logger
	scrapeTimeout time.Duration
}

// New constructs a Server using the provided registry and collector.
func New(opts Options, registry *prometheus.Registry, col *collector.RdmaCollector, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}

	s := &Server{
		registry:      registry,
		collector:     col,
		logger:        logger,
		scrapeTimeout: opts.ScrapeTimeout,
	}

	mux := http.NewServeMux()

	metricsPath := opts.MetricsPath
	if metricsPath == "" {
		metricsPath = "/metrics"
	}
	healthPath := opts.HealthPath
	if healthPath == "" {
		healthPath = "/healthz"
	}

	metricsHandler := promhttp.InstrumentMetricHandler(
		registry,
		http.HandlerFunc(s.handleMetrics),
	)

	mux.Handle(metricsPath, metricsHandler)
	mux.HandleFunc(healthPath, s.handleHealth)

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

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if s.scrapeTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.scrapeTimeout)
		defer cancel()
	}

	if s.collector != nil {
		s.collector.SetContext(ctx)
		defer s.collector.ResetContext()
	}

	type gatherResult struct {
		metrics []*dto.MetricFamily
		err     error
	}

	resultCh := make(chan gatherResult, 1)
	go func() {
		mfs, err := s.registry.Gather()
		resultCh <- gatherResult{metrics: mfs, err: err}
	}()

	var result gatherResult
	select {
	case result = <-resultCh:
	case <-ctx.Done():
		s.logger.Warn("metrics gather timed out", "err", ctx.Err())
		http.Error(w, "scrape timed out", http.StatusGatewayTimeout)
		return
	}

	if result.err != nil {
		s.logger.Error("metrics gather failed", "err", result.err)
		http.Error(w, "metrics gather failed", http.StatusInternalServerError)
		return
	}

	contentType := expfmt.Negotiate(r.Header)
	w.Header().Set("Content-Type", string(contentType))

	encoder := expfmt.NewEncoder(w, contentType)
	for _, mf := range result.metrics {
		if err := encoder.Encode(mf); err != nil {
			s.logger.Error("encode metric family failed", "err", err)
			return
		}
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

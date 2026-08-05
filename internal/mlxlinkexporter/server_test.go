package mlxlinkexporter

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func newDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func testOptions() Options {
	return Options{
		MetricsPath: defaultMetricsPath,
		HealthPath:  defaultHealthPath,
		ReadyPath:   defaultReadyPath,
	}
}

// newTestServer serves the exporter handlers over a local listener.
func newTestServer(t *testing.T, registry *prometheus.Registry, ready func() bool) *httptest.Server {
	t.Helper()

	server := New(testOptions(), registry, ready, newDiscardLogger())
	httpServer := httptest.NewServer(server.httpServer.Handler)
	t.Cleanup(httpServer.Close)
	return httpServer
}

func get(t *testing.T, base, path string) (int, string) {
	t.Helper()

	resp, err := http.Get(base + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s body: %v", path, err)
	}
	return resp.StatusCode, string(body)
}

func TestServer_HealthAlwaysOK(t *testing.T) {
	t.Parallel()

	// Health must not depend on readiness: a host without collectable devices
	// is unhealthy to page on, not to restart.
	server := newTestServer(t, prometheus.NewRegistry(), func() bool { return false })

	status, body := get(t, server.URL, defaultHealthPath)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	if body != "ok\n" {
		t.Fatalf("expected %q, got %q", "ok\n", body)
	}
}

func TestServer_ReadyReflectsPoller(t *testing.T) {
	t.Parallel()

	var ready atomic.Bool
	server := newTestServer(t, prometheus.NewRegistry(), ready.Load)

	status, body := get(t, server.URL, defaultReadyPath)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 before the first sweep, got %d", status)
	}
	if body != "not ready\n" {
		t.Fatalf("expected %q, got %q", "not ready\n", body)
	}

	ready.Store(true)

	status, body = get(t, server.URL, defaultReadyPath)
	if status != http.StatusOK {
		t.Fatalf("expected 200 once data exists, got %d", status)
	}
	if body != "ok\n" {
		t.Fatalf("expected %q, got %q", "ok\n", body)
	}
}

func TestServer_ReadyDefaultsToReady(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, prometheus.NewRegistry(), nil)

	if status, _ := get(t, server.URL, defaultReadyPath); status != http.StatusOK {
		t.Fatalf("expected 200 without a readiness source, got %d", status)
	}
}

func TestServer_MetricsServesRegistry(t *testing.T) {
	t.Parallel()

	registry := prometheus.NewRegistry()
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "mlxlink_collector_up",
		Help: "Whether the most recent mlxlink poll for this device succeeded.",
	})
	gauge.Set(1)
	registry.MustRegister(gauge)

	server := newTestServer(t, registry, func() bool { return true })

	status, body := get(t, server.URL, defaultMetricsPath)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	if !strings.Contains(body, "mlxlink_collector_up 1") {
		t.Fatalf("expected the registry contents, got:\n%s", body)
	}
	// InstrumentMetricHandler adds its own counters to the same registry.
	if !strings.Contains(body, "promhttp_metric_handler_requests_total") {
		t.Fatalf("expected the handler to be instrumented, got:\n%s", body)
	}
}

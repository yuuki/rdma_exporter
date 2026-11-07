package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/goleak"

	"github.com/yuuki/rdma_exporter/internal/collector"
	"github.com/yuuki/rdma_exporter/internal/rdma"
)

// The metrics handler gathers on a goroutine of its own and gives up on it at
// the scrape timeout, so every test must leave no goroutine behind.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// provider returns one device, or blocks until release is closed.
type provider struct {
	release <-chan struct{}

	mu        sync.Mutex
	deadlines []bool
}

func (p *provider) Devices(ctx context.Context) ([]rdma.Device, error) {
	p.mu.Lock()
	_, ok := ctx.Deadline()
	p.deadlines = append(p.deadlines, ok)
	p.mu.Unlock()
	if p.release != nil {
		<-p.release
	}
	return []rdma.Device{{
		Name: "mlx5_0",
		Ports: []rdma.Port{{
			ID:         1,
			Stats:      map[string]uint64{"port_xmit_data": 42},
			Attributes: rdma.PortAttributes{LinkLayer: "InfiniBand", State: "ACTIVE"},
		}},
	}}, nil
}

func newTestServer(t *testing.T, p *provider, timeout time.Duration) (*Server, *httptest.Server) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	col := collector.New(p, logger)
	registry := prometheus.NewRegistry()
	registry.MustRegister(col)
	s := New(Options{MetricsPath: "/metrics", HealthPath: "/healthz", ScrapeTimeout: timeout}, registry, col, logger)
	ts := httptest.NewServer(s.httpServer.Handler)
	t.Cleanup(ts.Close)
	return s, ts
}

// get returns the status, headers and body of a GET of url.
func get(t *testing.T, url string, header http.Header) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return resp.StatusCode, resp.Header, string(body)
}

func TestServer_Metrics(t *testing.T) {
	p := &provider{}
	_, ts := newTestServer(t, p, 5*time.Second)

	status, header, body := get(t, ts.URL+"/metrics", nil)
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, body)
	}
	if ct := header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
		t.Fatalf("Content-Type %q, want the text format", ct)
	}
	if want := `rdma_port_xmit_data_total{device="mlx5_0",port="1"} 42`; !strings.Contains(body, want) {
		t.Fatalf("body lacks %q:\n%s", want, body)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.deadlines) != 1 || !p.deadlines[0] {
		t.Fatalf("the collector must see the scrape deadline, got %v", p.deadlines)
	}
}

func TestServer_MetricsNegotiatesProtobuf(t *testing.T) {
	_, ts := newTestServer(t, &provider{}, 5*time.Second)

	const protobuf = "application/vnd.google.protobuf;proto=io.prometheus.client.MetricFamily;encoding=delimited"
	status, header, _ := get(t, ts.URL+"/metrics", http.Header{"Accept": {protobuf}})
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	if ct := header.Get("Content-Type"); !strings.HasPrefix(ct, "application/vnd.google.protobuf;") || !strings.Contains(ct, "encoding=delimited") {
		t.Fatalf("Content-Type %q, want delimited protobuf", ct)
	}
}

func TestServer_Health(t *testing.T) {
	_, ts := newTestServer(t, &provider{}, 5*time.Second)

	status, _, body := get(t, ts.URL+"/healthz", nil)
	if status != http.StatusOK || body != "ok\n" {
		t.Fatalf("status %d body %q", status, body)
	}
}

// A scrape that outlives the timeout answers 504, and its gather goroutine
// ends once the provider returns (TestMain checks that nothing leaked).
func TestServer_ScrapeTimeout(t *testing.T) {
	release := make(chan struct{})
	_, ts := newTestServer(t, &provider{release: release}, 50*time.Millisecond)
	defer close(release)

	status, _, body := get(t, ts.URL+"/metrics", nil)
	if status != http.StatusGatewayTimeout {
		t.Fatalf("status %d, want %d: %s", status, http.StatusGatewayTimeout, body)
	}
}

func TestServer_ShutdownStopsListenAndServe(t *testing.T) {
	s := New(Options{ListenAddress: "127.0.0.1:0", MetricsPath: "/metrics", HealthPath: "/healthz"},
		prometheus.NewRegistry(), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	errCh := make(chan error, 1)
	go func() { errCh <- s.ListenAndServe() }()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("ListenAndServe after Shutdown: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("ListenAndServe did not return after Shutdown")
	}
}

func TestServer_ListenAndServeReportsListenErrors(t *testing.T) {
	s := New(Options{ListenAddress: "127.0.0.1:-1", MetricsPath: "/metrics", HealthPath: "/healthz"},
		prometheus.NewRegistry(), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := s.ListenAndServe(); err == nil {
		t.Fatal("ListenAndServe on an invalid address returned nil")
	}
}

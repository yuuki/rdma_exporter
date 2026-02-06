package netdev

import (
	"context"
	"errors"
	"testing"
)

type stubStatsClient struct {
	stats map[string]uint64
	err   error

	closed bool
	calls  int
}

func (s *stubStatsClient) Stats(_ string) (map[string]uint64, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	out := make(map[string]uint64, len(s.stats))
	for k, v := range s.stats {
		out[k] = v
	}
	return out, nil
}

func (s *stubStatsClient) Close() {
	s.closed = true
}

func TestEthtoolStatsProvider_Stats(t *testing.T) {
	t.Parallel()

	client := &stubStatsClient{
		stats: map[string]uint64{
			"rx_prio0_pause": 12,
		},
	}
	provider := newEthtoolStatsProvider(client)

	got, err := provider.Stats(context.Background(), "ens1f0np0")
	if err != nil {
		t.Fatalf("Stats returned error: %v", err)
	}
	if got["rx_prio0_pause"] != 12 {
		t.Fatalf("expected rx_prio0_pause=12, got %d", got["rx_prio0_pause"])
	}
	if client.calls != 1 {
		t.Fatalf("expected stats client to be called once, got %d", client.calls)
	}
}

func TestEthtoolStatsProvider_ContextCanceled(t *testing.T) {
	t.Parallel()

	client := &stubStatsClient{}
	provider := newEthtoolStatsProvider(client)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := provider.Stats(ctx, "ens1f0np0")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
	if client.calls != 0 {
		t.Fatalf("expected stats client not to be called, got %d", client.calls)
	}
}

func TestEthtoolStatsProvider_StatsError(t *testing.T) {
	t.Parallel()

	client := &stubStatsClient{err: errors.New("boom")}
	provider := newEthtoolStatsProvider(client)

	_, err := provider.Stats(context.Background(), "ens1f0np0")
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestEthtoolStatsProvider_Close(t *testing.T) {
	t.Parallel()

	client := &stubStatsClient{}
	provider := newEthtoolStatsProvider(client)

	if err := provider.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	if !client.closed {
		t.Fatalf("expected stats client to be closed")
	}
}

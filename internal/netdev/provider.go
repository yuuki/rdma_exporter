package netdev

import (
	"context"
	"fmt"
	"sync"
)

type statsClient interface {
	Stats(intf string) (map[string]uint64, error)
	Close()
}

// EthtoolStatsProvider reads per-interface counters via ethtool.
type EthtoolStatsProvider struct {
	mu     sync.Mutex
	client statsClient
}

func newEthtoolStatsProvider(client statsClient) *EthtoolStatsProvider {
	return &EthtoolStatsProvider{client: client}
}

// Stats fetches counters for the specified netdev.
func (p *EthtoolStatsProvider) Stats(ctx context.Context, netDev string) (map[string]uint64, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	stats, err := p.client.Stats(netDev)
	if err != nil {
		return nil, fmt.Errorf("read ethtool stats for %s: %w", netDev, err)
	}

	out := make(map[string]uint64, len(stats))
	for k, v := range stats {
		out[k] = v
	}
	return out, nil
}

// Close closes the underlying ethtool client.
func (p *EthtoolStatsProvider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.client == nil {
		return nil
	}
	p.client.Close()
	p.client = nil
	return nil
}

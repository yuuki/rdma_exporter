//go:build linux

package netdev

import (
	"fmt"

	"github.com/safchain/ethtool"
)

// NewEthtoolStatsProvider creates a provider backed by an ethtool client.
func NewEthtoolStatsProvider() (*EthtoolStatsProvider, error) {
	client, err := ethtool.NewEthtool()
	if err != nil {
		return nil, fmt.Errorf("open ethtool client: %w", err)
	}
	return newEthtoolStatsProvider(client), nil
}

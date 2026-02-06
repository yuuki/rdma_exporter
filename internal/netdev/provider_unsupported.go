//go:build !linux

package netdev

import "errors"

// NewEthtoolStatsProvider is only supported on Linux hosts.
func NewEthtoolStatsProvider() (*EthtoolStatsProvider, error) {
	return nil, errors.New("ethtool stats provider is supported on linux only")
}

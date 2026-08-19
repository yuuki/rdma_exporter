//go:build !linux

package rdmanl

import "errors"

// New is only supported on Linux hosts.
func New() (*Provider, error) {
	return nil, errors.New("rdma netlink optional counters are supported on linux only")
}

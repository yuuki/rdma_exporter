package rdmanl

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"syscall"
)

const (
	nlmFRequest = 1
	nlmFAck     = 4
	nlmFDump    = 0x300 // NLM_F_ROOT|NLM_F_MATCH
)

type queryClient interface {
	Execute(ctx context.Context, typ uint16, flags uint16, payload []byte) ([][]byte, error)
	Close() error
}

// Provider reads optional RDMA hardware counters via NETLINK_RDMA.
// It never issues STAT_SET; operators enable counters with `rdma statistic set`.
type Provider struct {
	client queryClient

	mu          sync.Mutex
	devices     map[string]uint32
	unsupported bool
}

func newProvider(client queryClient) *Provider {
	return &Provider{client: client}
}

// Prepare dumps RDMA devices so later Counters calls can resolve name → index.
func (p *Provider) Prepare(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.client == nil {
		return fmt.Errorf("rdma netlink client is not initialized")
	}

	msgs, err := p.client.Execute(ctx, nlType(cmdGet), nlmFRequest|nlmFDump, nil)
	if err != nil {
		return fmt.Errorf("dump rdma devices: %w", err)
	}

	devices := make(map[string]uint32, len(msgs))
	for _, msg := range msgs {
		devs, err := parseDevices(msg)
		if err != nil {
			return err
		}
		for _, d := range devs {
			devices[d.Name] = d.Index
		}
	}

	p.mu.Lock()
	p.devices = devices
	p.mu.Unlock()
	return nil
}

// Counters returns optional hardware counters for device/port.
// Port is 1-based, matching sysfs and `rdma statistic`.
func (p *Provider) Counters(ctx context.Context, device string, port int) ([]OptionalCounter, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if port <= 0 {
		return nil, fmt.Errorf("invalid rdma port %d", port)
	}

	p.mu.Lock()
	if p.unsupported {
		p.mu.Unlock()
		return nil, nil
	}
	index, ok := p.devices[device]
	p.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("rdma device %s not found", device)
	}

	payload := encodePortQuery(index, uint32(port))
	statusMsgs, err := p.client.Execute(ctx, nlType(cmdStatGetStatus), nlmFRequest|nlmFAck, payload)
	if err != nil {
		if isUnsupportedNetlink(err) {
			p.mu.Lock()
			p.unsupported = true
			p.mu.Unlock()
			return nil, fmt.Errorf("optional counters require Linux 5.16+ STAT_GET_STATUS: %w", err)
		}
		return nil, fmt.Errorf("get optional counter status for %s/%d: %w", device, port, err)
	}

	var status []hwCounter
	for _, msg := range statusMsgs {
		parsed, err := parseHWCounters(msg)
		if err != nil {
			return nil, err
		}
		status = append(status, parsed...)
	}

	anyEnabled := false
	for _, c := range status {
		if c.Optional && c.Enabled {
			anyEnabled = true
			break
		}
	}
	if !anyEnabled {
		return mergeOptionalCounters(status, nil), nil
	}

	valueMsgs, err := p.client.Execute(ctx, nlType(cmdStatGet), nlmFRequest|nlmFAck, payload)
	if err != nil {
		return nil, fmt.Errorf("get optional counter values for %s/%d: %w", device, port, err)
	}
	var values []hwCounter
	for _, msg := range valueMsgs {
		parsed, err := parseHWCounters(msg)
		if err != nil {
			return nil, err
		}
		values = append(values, parsed...)
	}
	return mergeOptionalCounters(status, values), nil
}

// Close releases the netlink socket.
func (p *Provider) Close() error {
	if p == nil || p.client == nil {
		return nil
	}
	return p.client.Close()
}

func encodePortQuery(devIndex, port uint32) []byte {
	return concat(putU32(attrDevIndex, devIndex), putU32(attrPortIndex, port))
}

func concat(chunks ...[]byte) []byte {
	n := 0
	for _, c := range chunks {
		n += len(c)
	}
	out := make([]byte, 0, n)
	for _, c := range chunks {
		out = append(out, c...)
	}
	return out
}

func isUnsupportedNetlink(err error) bool {
	return errors.Is(err, syscall.EOPNOTSUPP) || errors.Is(err, syscall.ENOTSUP)
}

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

// ErrQPUnsupported is returned when STAT_GET for QP counters is not supported.
// The cached sentinel is the bare value; the first failure wraps it.
var ErrQPUnsupported = errors.New("QP counters unsupported")

// Provider reads RDMA hardware counters via NETLINK_RDMA GET/DUMP.
// It never issues STAT_SET, STAT_DEL, bind, or unbind.
type Provider struct {
	client queryClient

	mu            sync.Mutex
	devices       map[string]uint32
	unsupported   bool
	unsupportedQP bool
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

// QPMode returns the port-level QP counter bind mode.
func (p *Provider) QPMode(ctx context.Context, device string, port int) (QPMode, error) {
	if err := ctx.Err(); err != nil {
		return QPMode{}, err
	}
	index, err := p.deviceIndex(device, port)
	if err != nil {
		return QPMode{}, err
	}
	p.mu.Lock()
	if p.unsupportedQP {
		p.mu.Unlock()
		return QPMode{}, ErrQPUnsupported
	}
	p.mu.Unlock()

	payload := encodeQPModeQuery(index, uint32(port))
	msgs, err := p.client.Execute(ctx, nlType(cmdStatGet), nlmFRequest|nlmFAck, payload)
	if err != nil {
		if isUnsupportedNetlink(err) {
			p.mu.Lock()
			p.unsupportedQP = true
			p.mu.Unlock()
			return QPMode{}, fmt.Errorf("%w: %w", ErrQPUnsupported, err)
		}
		return QPMode{}, fmt.Errorf("get QP counter mode for %s/%d: %w", device, port, err)
	}
	var last QPMode
	for _, msg := range msgs {
		mode, err := parseQPMode(msg)
		if err != nil {
			return QPMode{}, err
		}
		last = mode
	}
	return last, nil
}

// QPSets dumps live bound QP counter sets. LQPN lists are not retained.
// The dump is bounded by defaultQPDumpBudget.
func (p *Provider) QPSets(ctx context.Context, device string, port int) ([]QPSet, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	index, err := p.deviceIndex(device, port)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	if p.unsupportedQP {
		p.mu.Unlock()
		return nil, ErrQPUnsupported
	}
	p.mu.Unlock()

	payload := encodeQPDumpQuery(index, uint32(port))
	dumpCtx := withDumpBudget(ctx, defaultQPDumpBudget())
	msgs, err := p.client.Execute(dumpCtx, nlType(cmdStatGet), nlmFRequest|nlmFAck|nlmFDump, payload)
	if err != nil {
		if isUnsupportedNetlink(err) {
			p.mu.Lock()
			p.unsupportedQP = true
			p.mu.Unlock()
			return nil, fmt.Errorf("%w: %w", ErrQPUnsupported, err)
		}
		return nil, fmt.Errorf("dump QP counters for %s/%d: %w", device, port, err)
	}
	return parseQPDump(msgs)
}

func (p *Provider) deviceIndex(device string, port int) (uint32, error) {
	if port <= 0 {
		return 0, fmt.Errorf("invalid rdma port %d", port)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	index, ok := p.devices[device]
	if !ok {
		return 0, fmt.Errorf("rdma device %s not found", device)
	}
	return index, nil
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

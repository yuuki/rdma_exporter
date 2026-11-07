//go:build linux

package rdmanl

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

// fuzzStream joins datagrams into one fuzz input, each behind a 2-byte
// little-endian length.
func fuzzStream(datagrams ...[]byte) []byte {
	var out []byte
	for _, d := range datagrams {
		out = binary.LittleEndian.AppendUint16(out, uint16(len(d)))
		out = append(out, d...)
	}
	return out
}

// FuzzReplyStream feeds arbitrary datagrams to the reply state machine of one
// request, as readReplies would after each recv, and checks what Execute
// promises its callers: only data messages of this request are returned, in
// order; a dump completes only at its own NLMSG_DONE and never past its
// budget; an interruption is reported only for this request's messages.
func FuzzReplyStream(f *testing.F) {
	const seq = 7
	doit := uint16(nlmFRequest)
	doitAck := uint16(nlmFRequest | nlmFAck)
	dump := uint16(nlmFRequest | nlmFAck | nlmFDump)
	f.Add(doit, uint16(0), uint16(0), fuzzStream(encodeInjected(seq, []injectedMsg{injData(0, "a")})))
	f.Add(doitAck, uint16(0), uint16(0), fuzzStream(
		encodeInjected(seq, []injectedMsg{injData(-1, "stale"), injData(0, "a")}),
		encodeInjected(seq, []injectedMsg{injData(0, "b"), injErr(0, 0)}),
	))
	f.Add(dump, uint16(0), uint16(0), fuzzStream(
		encodeInjected(seq, []injectedMsg{injData(0, "a"), injErr(0, 0), injData(1, "foreign")}),
		encodeInjected(seq, []injectedMsg{injData(0, "b"), injDone(0, nil)}),
	))
	f.Add(dump, uint16(64), uint16(2), fuzzStream(
		encodeInjected(seq, []injectedMsg{injData(0, "a"), injData(0, "b")}),
		encodeInjected(seq, []injectedMsg{injData(0, "c"), injDone(0, errnoPayload(0))}),
	))
	f.Add(dump, uint16(0), uint16(0), fuzzStream(encodeInjected(seq, []injectedMsg{
		{typ: nlType(cmdStatGet), flags: nlmFDumpIntr, data: []byte("x")},
		{typ: nlmsgOverrun},
	})))
	f.Add(doitAck, uint16(0), uint16(0), fuzzStream(encodeInjected(seq, []injectedMsg{injErr(0, int32(unix.EOPNOTSUPP))})))

	f.Fuzz(func(t *testing.T, flags, maxBytes, maxMsgs uint16, stream []byte) {
		ctx := context.Background()
		budget := DumpBudget{MaxBytes: int(maxBytes), MaxMsgs: int(maxMsgs)}
		if budget != (DumpBudget{}) {
			ctx = withDumpBudget(ctx, budget)
		}
		s := newReplyStream(ctx, seq, flags)

		for len(stream) >= 2 {
			n := min(int(binary.LittleEndian.Uint16(stream)), len(stream)-2)
			datagram := stream[2 : 2+n]
			stream = stream[2+n:]

			msgs, err := parseNlMsgs(datagram)
			if err != nil {
				return // readReplies fails the request
			}
			if err := s.checkBudget(len(datagram), msgs); err != nil {
				if !errors.Is(err, ErrDumpOverflow) || !s.dump {
					t.Fatalf("budget check of a request with flags %#x: %v", flags, err)
				}
				return
			}
			// consume handles messages in order and stops at completion, so
			// feeding them one at a time shows which message decided.
			for _, m := range msgs {
				before := len(s.out)
				done, err := s.consume([]nlMsg{m})
				if m.seq != seq {
					if done || err != nil || len(s.out) != before {
						t.Fatalf("message of another request (seq %d) changed the stream: done=%v err=%v", m.seq, done, err)
					}
					continue
				}
				if len(s.out) > before {
					if len(s.out) != before+1 || !isDataMsg(m.typ) || !bytes.Equal(s.out[before], m.data) {
						t.Fatalf("message type %d was returned as data %q", m.typ, s.out[before:])
					}
				}
				if err != nil {
					if errors.Is(err, ErrDumpInterrupted) && m.typ != nlmsgOverrun && m.flags&nlmFDumpIntr == 0 {
						t.Fatalf("message type %d flags %#x reported as an interrupted dump", m.typ, m.flags)
					}
					return
				}
				if done {
					if s.dump && m.typ != unix.NLMSG_DONE {
						t.Fatalf("dump completed at message type %d", m.typ)
					}
					return
				}
			}
			if s.dump && budget.MaxMsgs > 0 && len(s.out) > budget.MaxMsgs {
				t.Fatalf("dump accepted %d messages over a budget of %d", len(s.out), budget.MaxMsgs)
			}
			if s.dump && budget.MaxBytes > 0 && s.nBytes > budget.MaxBytes {
				t.Fatalf("dump accepted %d bytes over a budget of %d", s.nBytes, budget.MaxBytes)
			}
		}
	})
}

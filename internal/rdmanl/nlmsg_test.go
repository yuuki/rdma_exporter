package rdmanl

import (
	"encoding/binary"
	"testing"
)

func TestParseNlMsgs_DataAndAck(t *testing.T) {
	t.Parallel()

	data := make([]byte, nlmsgHdrLen+4)
	binary.LittleEndian.PutUint32(data[0:4], uint32(nlmsgHdrLen+4))
	binary.LittleEndian.PutUint16(data[4:6], nlType(cmdStatGetStatus))
	binary.LittleEndian.PutUint32(data[8:12], 7)
	binary.LittleEndian.PutUint32(data[nlmsgHdrLen:], 0x11223344)

	ack := make([]byte, nlmsgHdrLen+4)
	binary.LittleEndian.PutUint32(ack[0:4], uint32(nlmsgHdrLen+4))
	binary.LittleEndian.PutUint16(ack[4:6], nlmsgError)
	binary.LittleEndian.PutUint32(ack[8:12], 7)
	// errno 0 = ACK

	got, err := parseNlMsgs(append(data, ack...))
	if err != nil {
		t.Fatalf("parseNlMsgs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2", len(got))
	}
	if got[0].typ != nlType(cmdStatGetStatus) || got[0].seq != 7 {
		t.Fatalf("data msg: %+v", got[0])
	}
	if got[1].typ != nlmsgError {
		t.Fatalf("ack type %d", got[1].typ)
	}
	code, err := nlErrorCode(got[1].data)
	if err != nil || code != 0 {
		t.Fatalf("ack errno=%d err=%v", code, err)
	}
}

func TestNlErrorCode_NegativeErrno(t *testing.T) {
	t.Parallel()

	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], uint32(^uint32(2)+1)) // -2 as int32 (ENOENT)
	code, err := nlErrorCode(b[:])
	if err != nil {
		t.Fatalf("nlErrorCode: %v", err)
	}
	if code != 2 {
		t.Fatalf("got errno %d, want 2", code)
	}
}

func TestParseNlMsgs_Truncated(t *testing.T) {
	t.Parallel()

	if _, err := parseNlMsgs([]byte{1, 2, 3}); err == nil {
		t.Fatal("expected truncated header error")
	}
}

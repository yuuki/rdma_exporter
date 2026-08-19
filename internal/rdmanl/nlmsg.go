package rdmanl

import (
	"encoding/binary"
	"fmt"
)

const (
	nlmsgHdrLen  = 16
	nlmsgError   = 2
	nlmsgDone    = 3
	nlmsgOverrun = 4
	nlmFDumpIntr = 0x10
)

func parseNlMsgs(buf []byte) ([]nlMsg, error) {
	var msgs []nlMsg
	off := 0
	for off < len(buf) {
		if len(buf)-off < nlmsgHdrLen {
			return nil, fmt.Errorf("truncated netlink message header")
		}
		nlen := int(binary.LittleEndian.Uint32(buf[off : off+4]))
		if nlen < nlmsgHdrLen || off+nlen > len(buf) {
			return nil, fmt.Errorf("invalid netlink message length %d", nlen)
		}
		msgs = append(msgs, nlMsg{
			typ:   binary.LittleEndian.Uint16(buf[off+4 : off+6]),
			flags: binary.LittleEndian.Uint16(buf[off+6 : off+8]),
			seq:   binary.LittleEndian.Uint32(buf[off+8 : off+12]),
			data:  append([]byte(nil), buf[off+nlmsgHdrLen:off+nlen]...),
		})
		off += nlaAlign(nlen)
	}
	return msgs, nil
}

func nlErrorCode(data []byte) (int, error) {
	if len(data) < 4 {
		return 0, fmt.Errorf("truncated netlink error message")
	}
	code := int32(binary.LittleEndian.Uint32(data[:4]))
	if code < 0 {
		code = -code
	}
	return int(code), nil
}

type nlMsg struct {
	typ   uint16
	flags uint16
	seq   uint32
	data  []byte
}

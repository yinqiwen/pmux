package pmux

import (
	"encoding/binary"
	"fmt"
	"io"
)

const (
	FrameProtoVersion = byte(1)
	HeaderLenV1       = 10
)

const (
	flagSYN byte = iota
	flagFIN
	flagData
	flagWindowUpdate

	flagHandshake = byte(100)
)

type FrameHeader []byte

func (h FrameHeader) Version() uint8 {
	return h[0]
}

func (h FrameHeader) Flags() uint8 {
	return uint8(h[1])
}

func (h FrameHeader) StreamID() uint32 {
	return binary.BigEndian.Uint32(h[2:6])
}

func (h FrameHeader) Length() uint32 {
	return binary.BigEndian.Uint32(h[6:10])
}

func (h FrameHeader) String() string {
	return fmt.Sprintf("Vsn:%d  Flags:%d StreamID:%d Length:%d",
		h.Version(), h.Flags(), h.StreamID(), h.Length())
}

func (h FrameHeader) encode(flags byte, streamID uint32, length uint32) {
	h[0] = FrameProtoVersion
	h[1] = flags
	binary.BigEndian.PutUint32(h[2:6], streamID)
	binary.BigEndian.PutUint32(h[6:10], length)
}

func newFrameHeader(flags byte, streamID uint32, length uint32) FrameHeader {
	fh := make(FrameHeader, 10)
	fh.encode(flags, streamID, length)
	return fh
}

type Frame struct {
	Header FrameHeader
	Body   []byte
}

func (h *Frame) Version() byte {
	return h.Header[0]
}

func (h *Frame) Flag() byte {
	return h.Header[1]
}

func (h *Frame) Length() uint32 {
	return uint32(h.Header[2]) | uint32(h.Header[3])<<8 | uint32(h.Header[4])<<16
}

func (h *Frame) StreamID() uint32 {
	return binary.LittleEndian.Uint32(h.Header[5:])
}

func (h *Frame) String() string {
	return fmt.Sprintf("Version:%d Flag:%d StreamID:%d Length:%d",
		h.Version(), h.Flag(), h.StreamID(), h.Length())
}

func recvFrame(reader io.Reader) (*Frame, error) {
	hbuf := make(FrameHeader, HeaderLenV1)
	_, err := io.ReadAtLeast(reader, hbuf, len(hbuf))
	if nil != err {
		return nil, err
	}
	if hbuf[0] != FrameProtoVersion {
		return nil, ErrInvalidVersion
	}
	frame := &Frame{}
	frame.Header = hbuf
	if hbuf.Flags() == flagData {
		dataLen := hbuf.Length()
		buf := make([]byte, dataLen)
		_, err = io.ReadAtLeast(reader, buf, len(buf))
		if nil != err {
			return nil, err
		}
		frame.Body = buf
	}
	return frame, nil
}

type handshakeFrameBody struct {
	CryptoMethod uint16
	IV           []byte
}

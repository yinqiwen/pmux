package pmux

import (
	"encoding/binary"
	"fmt"
	"io"
)

const (
	FrameProtoVersion = byte(1)
	HeaderLenV1       = 6
)

const (
	flagSYN byte = iota
	flagFIN
	flagData
	flagWindowUpdate
	flagPing
	flagPingACK

	flagHandshake = byte(100)
)

type FrameHeader []byte

func (h FrameHeader) Version() uint8 {
	return h[0]
}

func (h FrameHeader) Flags() uint8 {
	return h[1]
}

func (h FrameHeader) StreamID() uint32 {
	return binary.BigEndian.Uint32(h[2:6])
}

// func (h FrameHeader) Length() uint32 {
// 	return binary.BigEndian.Uint32(h[6:10])
// }

func (h FrameHeader) String() string {
	return fmt.Sprintf("Version:%d  Flags:%d StreamID:%d",
		h.Version(), h.Flags(), h.StreamID())
}

func (h FrameHeader) encode(flags byte, streamID uint32) {
	//binary.BigEndian.PutUint32(h[6:10], length)
	h[0] = FrameProtoVersion
	h[1] = flags
	binary.BigEndian.PutUint32(h[2:6], streamID)
}

func newFrameHeader(flags byte, streamID uint32) FrameHeader {
	fh := make(FrameHeader, HeaderLenV1)
	fh.encode(flags, streamID)
	return fh
}

type Frame struct {
	Header FrameHeader
	Body   []byte
}

func (f *Frame) Length() uint32 {
	if f.Header.Flags() == flagData {
		return uint32(len(f.Body))
	}
	return binary.BigEndian.Uint32(f.Body)
}

func (f *Frame) SetLength(v uint32) {
	if f.Header.Flags() != flagData {
		f.Body = make([]byte, 4)
		binary.BigEndian.PutUint32(f.Body, v)
	}
}

func writeFrame(wr io.Writer, frame *Frame, ctx *CryptoContext) error {
	buf := []byte(frame.Header)
	if len(frame.Body) > 0 {
		buf = append(buf, frame.Body...)
	}
	var err error
	buf, err = ctx.encodeData(buf)
	if nil != err {
		return err
	}
	length := ctx.encodeLength(uint32(len(buf)))
	//log.Printf("[Send]Write len:%d %d", len(buf), length)
	//log.Printf("[Send]Write frame %d %d %d %d %d", len(buf), frame.Header.Flags(), frame.Header.StreamID(), len(frame.Body), ctx.encryptCounter)
	binary.Write(wr, binary.BigEndian, length)
	_, err = wr.Write(buf)
	ctx.incEncryptCounter()
	return err
}

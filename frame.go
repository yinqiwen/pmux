package pmux

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
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

type Frame []byte

func (h Frame) Header() FrameHeader {
	return FrameHeader(h[0:HeaderLenV1])
}
func (h Frame) Body() []byte {
	return h[HeaderLenV1:]
}
func (f Frame) Length() uint32 {
	if f.Header().Flags() == flagData {
		return uint32(len(f) - HeaderLenV1)
	}
	return binary.BigEndian.Uint32(f.Body())
}

func newFrame(flags byte, streamID, length uint32, data []byte) Frame {
	flen := HeaderLenV1
	if nil != data {
		flen += len(data)
	} else {
		if length > 0 {
			flen += 4
		}
	}
	//fr := make(Frame, flen)
	fr := Frame(getBytesFromPool(flen))
	fr.Header().encode(flags, streamID)
	if nil != data {
		copy(fr.Body(), data)
	} else {
		if length > 0 {
			binary.BigEndian.PutUint32(fr.Body(), length)
		}
	}
	return fr
}

// type Frame struct {
// 	Header  FrameHeader
// 	Body    []byte
// 	Content []byte
// }

// func (f *Frame) Length() uint32 {
// 	if f.Header.Flags() == flagData {
// 		return uint32(len(f.Body))
// 	}
// 	return binary.BigEndian.Uint32(f.Body)
// }

// func (f *Frame) SetLength(v uint32) {
// 	if f.Header.Flags() != flagData {
// 		f.Body = make([]byte, 4)
// 		binary.BigEndian.PutUint32(f.Body, v)
// 	}
// }

// func newFrame(flags byte, streamID uint32, data []byte) *Frame {
// 	fr := &Frame
// 	fr.Content = make([]byte, HeaderLenV1+len(data))
// }

func encodeFrameToBuffers(buffers net.Buffers, lenbuf []byte, frame Frame, ctx *CryptoContext) (net.Buffers, error) {
	if len(frame) == 0 {
		return buffers, nil
	}
	buf := []byte(frame)
	var err error
	buf, err = ctx.encodeData(buf)
	if nil != err {
		return buffers, err
	}
	length := ctx.encodeLength(uint32(len(buf)))
	binary.BigEndian.PutUint32(lenbuf, length)
	buffers = append(buffers, lenbuf)
	buffers = append(buffers, buf)
	//log.Printf("[Send]Write len:%d %d", len(buf), length)
	//log.Printf("[Send]Write frame %d %d %d %d %d", len(buf), frame.Header().Flags(), frame.Header().StreamID(), len(frame.Body()), ctx.encryptCounter)
	//binary.Write(wr, binary.BigEndian, length)
	//_, err = wr.Write(buf)
	ctx.incEncryptCounter()
	return buffers, nil
}

func writeFrame(wr io.Writer, frame Frame, ctx *CryptoContext) error {
	if len(frame) == 0 {
		return nil
	}
	buf := []byte(frame)
	var err error
	buf, err = ctx.encodeData(buf)
	if nil != err {
		return err
	}
	length := ctx.encodeLength(uint32(len(buf)))
	//log.Printf("[Send]Write len:%d %d", len(buf), length)
	//log.Printf("[Send]Write frame %d %d %d %d %d", len(buf), frame.Header().Flags(), frame.Header().StreamID(), len(frame.Body()), ctx.encryptCounter)
	binary.Write(wr, binary.BigEndian, length)
	_, err = wr.Write(buf)
	ctx.incEncryptCounter()
	return err
}

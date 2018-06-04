package pmux

import "io"

type FrameData struct {
	fr   Frame
	data []byte
}

func newFrameData(v Frame) *FrameData {
	return &FrameData{
		fr:   v,
		data: v.Body(),
	}
}

type FrameDataBuffer []*FrameData

func (buf *FrameDataBuffer) Take() *FrameData {
	if len(*buf) == 0 {
		return nil
	}
	ret := (*buf)[0]
	*buf = (*buf)[1:]
	return ret
}

func (buf *FrameDataBuffer) Read(p []byte) (int, error) {
	if len(*buf) == 0 {
		return 0, io.EOF
	}
	total := 0
	for len(*buf) > 0 {
		b := *buf
		min := len(p[total:])
		if min > len(b[0].data) {
			min = len(b[0].data)
		}
		copy(p[total:total+min], b[0].data[0:min])
		b[0].data = b[0].data[min:]
		total += min
		if len(b[0].data) == 0 {
			putBytesToPool(b[0].fr)
			*buf = b[1:]
		}
		if total == len(p) {
			return total, nil
		}
	}
	return total, io.EOF
}

func (buf *FrameDataBuffer) Write(p Frame) (int, error) {
	*buf = append(*buf, newFrameData(p))
	return len(p), nil
}

func (buf *FrameDataBuffer) Len() int {
	return len(*buf)
}

type ByteSliceBuffer [][]byte

func (buf *ByteSliceBuffer) Take() []byte {
	if len(*buf) == 0 {
		return nil
	}
	ret := (*buf)[0]
	*buf = (*buf)[1:]
	return ret
}

func (buf *ByteSliceBuffer) Read(p []byte) (int, error) {
	if len(*buf) == 0 {
		return 0, io.EOF
	}
	total := 0
	for len(*buf) > 0 {
		b := *buf
		min := len(p[total:])
		if min > len(b[0]) {
			min = len(b[0])
		}
		copy(p[total:total+min], b[0][0:min])
		b[0] = b[0][min:]
		total += min
		if len(b[0]) == 0 {
			*buf = b[1:]
		}
		if total == len(p) {
			return total, nil
		}
	}
	return total, io.EOF
}

func (buf *ByteSliceBuffer) Write(p []byte) (int, error) {
	*buf = append(*buf, p)
	return len(p), nil
}

func (buf *ByteSliceBuffer) Len() int {
	return len(*buf)
}

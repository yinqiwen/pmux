package pmux

import "io"

// type ByteSliceBuffer struct {
// 	buf [][]byte
// }

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

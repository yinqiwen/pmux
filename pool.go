package pmux

import (
	"sync"
)

const minBytesUnitSize = 256

var bytesPools []*sync.Pool

func init() {
	bytesLength := minBytesUnitSize
	for bytesLength <= 256*1024 {
		vlen := bytesLength
		pool := &sync.Pool{
			New: func() interface{} {
				//fmt.Printf("####alloc %d\n", vlen)
				return make([]byte, vlen)
			},
		}
		bytesLength += minBytesUnitSize
		bytesPools = append(bytesPools, pool)
	}
}

func getIdxForSize(size int) int {
	idx := size / minBytesUnitSize
	if size%minBytesUnitSize == 0 {
		idx = idx - 1
	}
	if idx >= len(bytesPools) {
		return -1
	}
	return idx
}

func getBytesFromPool(size int) []byte {
	//return make([]byte, size)
	//fmt.Printf("####try alloc %d\n", size)
	idx := getIdxForSize(size)
	if idx < 0 {
		return make([]byte, size)
	}
	return bytesPools[idx].Get().([]byte)[0:size]
}

func putBytesToPool(buf []byte) {
	idx := getIdxForSize(cap(buf))
	if idx < 0 {
		return
	}
	bytesPools[idx].Put(buf[:cap(buf)])
}

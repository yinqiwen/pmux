package pmux

import (
	"fmt"
)

var (
	// ErrInvalidVersion means we received a frame with an
	// invalid version
	ErrInvalidVersion = fmt.Errorf("invalid protocol version")

	// ErrInvalidMsgType means we received a frame with an
	// invalid message type
	ErrInvalidMsgType = fmt.Errorf("invalid msg type")

	// ErrSessionShutdown is used if there is a shutdown during
	// an operation
	ErrSessionShutdown = fmt.Errorf("session shutdown")

	// ErrStreamsExhausted is returned if we have no more
	// stream ids to issue
	ErrStreamsExhausted = fmt.Errorf("streams exhausted")

	// ErrDuplicateStream is used if a duplicate stream is
	// opened inbound
	ErrDuplicateStream = fmt.Errorf("duplicate stream initiated")

	// ErrReceiveWindowExceeded indicates the window was exceeded
	ErrRecvWindowExceeded = fmt.Errorf("recv window exceeded")

	// ErrTimeout is used when we reach an IO deadline
	ErrTimeout = fmt.Errorf("i/o deadline reached")

	// ErrStreamClosed is returned when using a closed stream
	ErrStreamClosed = fmt.Errorf("stream closed")

	// ErrStreamClosed is returned when using a closed stream
	ErrStreamAbort = fmt.Errorf("stream abort")

	// ErrUnexpectedFlag is set when we get an unexpected flag
	ErrUnexpectedFlag = fmt.Errorf("unexpected flag")

	// ErrRemoteGoAway is used when we get a go away from the other side
	ErrRemoteGoAway = fmt.Errorf("remote end is not accepting connections")

	// ErrConnectionReset is sent if a stream is reset. This can happen
	// if the backlog is exceeded, or if there was a remote GoAway.
	ErrConnectionReset = fmt.Errorf("connection reset")

	// ErrConnectionWriteTimeout indicates that we hit the "safety valve"
	// timeout writing to the underlying stream connection.
	ErrConnectionWriteTimeout = fmt.Errorf("connection write timeout")

	// ErrKeepAliveTimeout is sent if a missed keepalive caused the stream close
	ErrKeepAliveTimeout = fmt.Errorf("keepalive timeout")

	ErrSessionHandshakeMissing = fmt.Errorf("session handshake missing")

	ErrInvalidCipherMethod = fmt.Errorf("invalid cipher method")

	ErrToolargeDataFrame = fmt.Errorf("too large data frame")
)

const (
	// initialStreamWindow is the initial stream window size
	initialStreamWindow uint32 = 512 * 1024
	maxDataPacketSize   uint32 = 1024 * 1024
)

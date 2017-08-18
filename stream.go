package pmux

import (
	"bytes"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

type streamState int

const (
	streamEstablished streamState = iota
	streamClosed
	//streamReset
)

type Stream struct {
	id      uint32
	session *Session

	//recvWindow uint32
	sendWindow  uint32
	deltaWindow uint32

	state     streamState
	stateLock sync.Mutex

	recvBuf  *bytes.Buffer
	recvLock sync.Mutex

	//controlErr     chan error
	//controlHdrLock sync.Mutex

	sendErr   chan error
	sendLock  sync.Mutex
	sendReady chan struct{}

	recvNotifyCh chan struct{}
	sendNotifyCh chan struct{}

	readDeadline  time.Time
	writeDeadline time.Time
}

// newStream is used to construct a new stream within
// a given session for an ID
func newStream(session *Session, id uint32) *Stream {
	s := &Stream{
		id:      id,
		session: session,
		state:   streamEstablished,
		sendErr: make(chan error, 1),
		//recvWindow:   initialStreamWindow,
		sendWindow:   initialStreamWindow,
		recvNotifyCh: make(chan struct{}, 1),
		sendNotifyCh: make(chan struct{}, 1),
	}
	return s
}

// ID returns the unique stream ID.
func (s *Stream) ID() uint32 {
	return s.id
}

// Read implements net.Conn
func (s *Stream) Read(b []byte) (n int, err error) {
START:
	s.stateLock.Lock()
	if s.state != streamEstablished {
		return 0, io.EOF
	}
	s.stateLock.Unlock()

	s.recvLock.Lock()
	if s.recvBuf == nil || s.recvBuf.Len() == 0 {
		s.recvLock.Unlock()
		goto WAIT
	}

	// Read any bytes
	n, _ = s.recvBuf.Read(b)
	atomic.AddUint32(&s.deltaWindow, uint32(n))
	s.recvLock.Unlock()
	s.updateRemoteSendWindow()

	return n, err
WAIT:
	var timeout <-chan time.Time
	var timer *time.Timer
	if !s.readDeadline.IsZero() {
		delay := s.readDeadline.Sub(time.Now())
		timer = time.NewTimer(delay)
		timeout = timer.C
	}
	select {
	case <-s.recvNotifyCh:
		if timer != nil {
			timer.Stop()
		}
		goto START
	case <-timeout:
		return 0, ErrTimeout
	}
}

func (s *Stream) updateRemoteSendWindow() error {
	max := s.session.config.MaxStreamWindowSize
	delta := atomic.LoadUint32(&s.deltaWindow)

	// Check if we can omit the update
	if delta < (max / 2) {
		return nil
	}

	// Update our window
	//atomic.AddUint32(&s.recvWindow, delta)
	atomic.StoreUint32(&s.deltaWindow, 0)
	if err := s.session.writeFrame(newFrameHeader(flagData, s.id, delta), nil); err != nil {
		return err
	}
	return nil
}

// incrSendWindow updates the size of our send window
func (s *Stream) incrSendWindow(frame *Frame) error {
	// Increase window, unblock a sender
	atomic.AddUint32(&s.sendWindow, frame.Header.Length())
	asyncNotify(s.sendNotifyCh)
	return nil
}

func (s *Stream) offerData(data []byte) error {
	s.recvLock.Lock()
	if nil == s.recvBuf || s.recvBuf.Len() == 0 {
		s.recvBuf = &bytes.Buffer{}
	}
	s.recvBuf.Write(data)
	s.recvLock.Unlock()
	asyncNotify(s.recvNotifyCh)
	return nil
}

func (s *Stream) Write(p []byte) (int, error) {
	s.sendLock.Lock()
	defer s.sendLock.Unlock()
	total := 0
	for total < len(p) {
		n, err := s.write(p[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// write is used to write to the stream, may return on
// a short write.
func (s *Stream) write(b []byte) (n int, err error) {
	var max uint32
START:
	s.stateLock.Lock()
	if s.state != streamEstablished {
		return 0, ErrStreamClosed
	}
	s.stateLock.Unlock()

	// If there is no data available, block
	window := atomic.LoadUint32(&s.sendWindow)
	if window == 0 {
		goto WAIT
	}

	// Send up to our send window
	max = min(window, uint32(len(b)))

	// Send the header
	//s.sendHdr.encode(flagData, s.id, max)
	if err := s.session.writeFrame(newFrameHeader(flagData, s.id, max), b[:max]); err != nil {
		return 0, err
	}

	// Reduce our send window
	atomic.AddUint32(&s.sendWindow, ^uint32(max-1))

	// Unlock
	return int(max), err

WAIT:
	var timeout <-chan time.Time
	if !s.writeDeadline.IsZero() {
		delay := s.writeDeadline.Sub(time.Now())
		timeout = time.After(delay)
	}
	select {
	case <-s.sendNotifyCh:
		goto START
	case <-timeout:
		return 0, ErrTimeout
	}
	return 0, nil
}

// notifyWaiting notifies all the waiting channels
func (s *Stream) notifyWaiting() {
	asyncNotify(s.recvNotifyCh)
	asyncNotify(s.sendNotifyCh)
}

// Close is used to close the stream
func (s *Stream) Close() error {
	if s.state != streamEstablished {
		return nil
	}
	s.forceClose()
	s.sendClose()
	s.session.removeStream(s.id)
	return nil
}

// sendClose is used to send a FIN
func (s *Stream) sendClose() error {
	if err := s.session.closeRemoteStream(s.id); err != nil {
		return err
	}
	return nil
}

// forceClose is used for when the session is exiting
func (s *Stream) forceClose() {
	s.stateLock.Lock()
	s.state = streamClosed
	s.stateLock.Unlock()
	s.notifyWaiting()
}

// SetDeadline sets the read and write deadlines
func (s *Stream) SetDeadline(t time.Time) error {
	if err := s.SetReadDeadline(t); err != nil {
		return err
	}
	if err := s.SetWriteDeadline(t); err != nil {
		return err
	}
	return nil
}

// SetReadDeadline sets the deadline for future Read calls.
func (s *Stream) SetReadDeadline(t time.Time) error {
	s.readDeadline = t
	return nil
}

// SetWriteDeadline sets the deadline for future Write calls
func (s *Stream) SetWriteDeadline(t time.Time) error {
	s.writeDeadline = t
	return nil
}

// Shrink is used to compact the amount of buffers utilized
// This is useful when using Yamux in a connection pool to reduce
// the idle memory utilization.
func (s *Stream) Shrink() {
	s.recvLock.Lock()
	if s.recvBuf != nil && s.recvBuf.Len() == 0 {
		s.recvBuf = nil
	}
	s.recvLock.Unlock()
}

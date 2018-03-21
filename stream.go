package pmux

import (
	"io"
	"log"
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

	//recvBuf  *bytes.Buffer
	recvBuf  ByteSliceBuffer
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

	IOCallback IOCallback
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

func (s *Stream) WriteTo(w io.Writer) (n int64, err error) {
	total := int64(0)
START:
	s.recvLock.Lock()
	if s.recvBuf.Len() == 0 {
		s.recvLock.Unlock()
		if s.state != streamEstablished {
			return total, io.EOF
		}
		goto WAIT
	}
	s.recvLock.Unlock()
	for {
		s.recvLock.Lock()
		b := s.recvBuf.Take()
		s.recvLock.Unlock()
		if len(b) > 0 {
			// Read any bytes
			n := len(b)
			atomic.AddUint32(&s.deltaWindow, uint32(n))
			_, err := w.Write(b)
			if s.IOCallback != nil {
				s.IOCallback.OnIO(true)
			}
			if nil != err {
				return total, err
			}
		} else {
			break
		}
	}
	s.updateRemoteSendWindow()
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
		return total, ErrTimeout
	}
}

// Read implements net.Conn
func (s *Stream) Read(b []byte) (n int, err error) {
START:
	s.recvLock.Lock()
	if s.recvBuf.Len() == 0 {
		s.recvLock.Unlock()
		if s.state != streamEstablished {
			return 0, io.EOF
		}
		goto WAIT
	}

	// Read any bytes
	n, _ = s.recvBuf.Read(b)
	atomic.AddUint32(&s.deltaWindow, uint32(n))
	s.recvLock.Unlock()
	s.updateRemoteSendWindow()
	if s.IOCallback != nil {
		s.IOCallback.OnIO(true)
	}
	return n, nil
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
	//max := s.session.config.MaxStreamWindowSize
	delta := atomic.LoadUint32(&s.deltaWindow)

	// Check if we can omit the update
	if delta < s.session.config.StreamMinRefresh {
		return nil
	}

	// Update our window
	//atomic.AddUint32(&s.recvWindow, delta)
	atomic.StoreUint32(&s.deltaWindow, 0)
	if err := s.session.updateWindow(s.id, delta); err != nil {
		return err
	}
	//log.Printf("####[%d]deltaWindow %d", s.ID(), delta)
	return nil
}

// incrSendWindow updates the size of our send window
func (s *Stream) incrSendWindow(frame Frame) error {
	// Increase window, unblock a sender
	atomic.AddUint32(&s.sendWindow, frame.Length())
	asyncNotify(s.sendNotifyCh)
	//log.Printf("####[%d]update send window to %d", s.ID(), s.sendWindow)
	return nil
}

func (s *Stream) offerData(data []byte) error {
	s.recvLock.Lock()
	s.recvBuf.Write(data)
	s.recvLock.Unlock()
	asyncNotify(s.recvNotifyCh)
	//log.Printf("####%s %d stream", string(data), len(data))
	return nil
}

func (s *Stream) Write(p []byte) (int, error) {
	// s.sendLock.Lock()
	// defer s.sendLock.Unlock()
	total := 0
	for total < len(p) {
		//log.Printf("[Stream]Write data %d %d", total, len(p))
		n, err := s.write(p[total:])
		if s.IOCallback != nil {
			s.IOCallback.OnIO(false)
		}
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
		log.Printf("[%d]send window is ZERO", s.ID())
		goto WAIT
	}

	// Send up to our send window
	max = min(window, uint32(len(b)))

	// Send the header
	//s.sendHdr.encode(flagData, s.id, max)
	if err := s.session.writeFrameNowait(newFrame(flagData, s.id, 0, b[:max])); err != nil {
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
	s.sendClose()
	s.forceClose(true)
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
func (s *Stream) forceClose(remove bool) {
	s.stateLock.Lock()
	s.state = streamClosed
	s.stateLock.Unlock()
	s.notifyWaiting()
	if remove {
		s.session.removeStream(s.id)
	}
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

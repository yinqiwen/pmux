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
	streamAccepting
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
	recvBuf  FrameDataBuffer
	recvLock sync.Mutex

	//controlErr     chan error
	//controlHdrLock sync.Mutex

	sendErr   chan error
	sendLock  sync.Mutex
	sendReady chan struct{}

	recvNotifyCh chan struct{}
	sendNotifyCh chan struct{}

	initTime      time.Time
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
		recvNotifyCh: make(chan struct{}, 3),
		sendNotifyCh: make(chan struct{}, 3),
		initTime:     time.Now(),
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
		if nil != b {
			// Read any bytes
			n := len(b.data)
			atomic.AddUint32(&s.deltaWindow, uint32(n))
			s.updateRemoteSendWindow()
			_, err := w.Write(b.data)
			putBytesToPool(b.fr)
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
	s.recvLock.Unlock()
	atomic.AddUint32(&s.deltaWindow, uint32(n))
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

func (s *Stream) offerData(fr Frame) error {
	s.recvLock.Lock()
	s.recvBuf.Write(fr)
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

func (s *Stream) ReadFrom(r io.Reader) (n int64, err error) {
	bufUnitSize := 4096
	bufSize := uint32(bufUnitSize * 2)
	var timeout <-chan time.Time
	for {
		s.stateLock.Lock()
		if s.state != streamEstablished {
			s.stateLock.Unlock()
			//log.Printf("[%d]close stream at state:%d", s.ID(), s.state)
			return n, ErrStreamClosed
		}
		s.stateLock.Unlock()
		// If there is no data available, block
		window := atomic.LoadUint32(&s.sendWindow)
		if window == 0 {
			log.Printf("[%d]send window is ZERO", s.ID())
			var timeout <-chan time.Time
			if !s.writeDeadline.IsZero() {
				delay := s.writeDeadline.Sub(time.Now())
				timeout = time.After(delay)
			}
			select {
			case <-s.sendNotifyCh:
				continue
			case <-timeout:
				return n, ErrTimeout
			}
		}
		// Send up to our send window
		max := min(window, bufSize)
		frameLen := int(max) + HeaderLenV1 + 4
		kunit := frameLen / bufUnitSize
		if frameLen%bufUnitSize > 0 {
			kunit++
		}
		requestDataLen := kunit * bufUnitSize
		fr := LenFrame(getBytesFromPool(requestDataLen)[0:frameLen])
		fr.Frame().Header().encode(flagData, s.ID())
		rn, rerr := r.Read(fr.Frame().Body())
		n += int64(rn)
		if rn > 0 {
			fr = fr[0:(4 + HeaderLenV1 + rn)]
			if err := s.session.writeFrameNowait(fr, timeout); err != nil {
				//putBytesToPool(fr)
				return n, err
			}
			if s.IOCallback != nil {
				s.IOCallback.OnIO(false)
			}
			// Reduce our send window
			atomic.AddUint32(&s.sendWindow, ^uint32(rn-1))
			if rn >= int(bufSize) && bufSize < 128*1024 {
				bufSize *= 2
			} else if rn < int(bufSize/2) {
				bufSize /= 2
			}
		} else {
			putBytesToPool(fr)
		}
		if nil != rerr {
			return n, rerr
		}
	}
}

// write is used to write to the stream, may return on
// a short write.
func (s *Stream) write(b []byte) (n int, err error) {
	var max uint32
	var timeout <-chan time.Time
START:
	s.stateLock.Lock()
	if s.state != streamEstablished {
		s.stateLock.Unlock()
		return 0, ErrStreamClosed
	}
	s.stateLock.Unlock()
	if !s.writeDeadline.IsZero() {
		delay := s.writeDeadline.Sub(time.Now())
		timeout = time.After(delay)
	}

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
	if err := s.session.writeFrameNowait(newLenFrame(flagData, s.id, 0, b[:max]), timeout); err != nil {
		return 0, err
	}

	// Reduce our send window
	atomic.AddUint32(&s.sendWindow, ^uint32(max-1))

	// Unlock
	return int(max), err

WAIT:
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
	s.sendClose(false)
	s.forceClose(true)
	return nil
}

// Sync Close  stream
func (s *Stream) SyncClose() error {
	if s.state != streamEstablished {
		return nil
	}
	s.sendClose(true)
	s.forceClose(true)

	return nil
}

// sendClose is used to send a FIN
func (s *Stream) sendClose(sync bool) error {
	return s.session.closeRemoteStream(s.id, sync)
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

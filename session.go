package pmux

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// sendReady is used to either mark a stream as ready
// or to directly send a header
type sendReady struct {
	F   LenFrame
	Err chan error
}

type Session struct {
	nextStreamID uint32
	config       *Config
	conn         io.ReadWriteCloser
	connReader   *bufio.Reader
	connWriter   io.Writer
	// bufRead is a buffered reader
	//bufRead  *bufio.Reader
	//streams    map[uint32]*Stream
	//streamLock sync.Mutex
	streams        sync.Map
	streamsCounter int32
	acceptCh       chan *Stream
	sendCh         chan sendReady
	pingCh         chan struct{}

	shutdown     int32
	shutdownErr  error
	shutdownCh   chan struct{}
	shutdownLock sync.Mutex
	initCh       chan struct{}

	handshakeDone bool
	acceptable    bool

	cryptoContext *CryptoContext
	lastRecvTime  time.Time
	sendingNum    int32

	lenbuf [4]byte
}

func (s *Session) IsShutdown() bool {
	return atomic.LoadInt32(&s.shutdown) > 0
}

// keepalive is a long running goroutine that periodically does
// a ping to keep the connection alive.
func (s *Session) keepalive() {
	for !s.IsShutdown() {
		select {
		case <-time.After(s.config.KeepAliveInterval):
			_, err := s.Ping()
			if err != nil {
				log.Printf("[ERR] pmux: keepalive failed: %v", err)
				if err == ErrTimeout {
					s.Close()
					return
				}
			}
		case <-s.shutdownCh:
			return
		}
	}
}

// Ping is used to measure the RTT response time
func (s *Session) Ping() (time.Duration, error) {
	// Send the ping request
	pingTimeout := time.After(s.config.PingTimeout)
	err := s.writeFrameNowait(newLenFrame(flagPing, 0, 0, nil), pingTimeout)
	if nil != err {
		return 0, err
	}

	// Wait for a response
	start := time.Now()
	select {
	case <-s.pingCh:
	case <-pingTimeout:
		if time.Now().Sub(s.lastRecvTime) >= s.config.PingTimeout {
			return 0, ErrTimeout
		}
		return s.config.PingTimeout, nil
	case <-s.shutdownCh:
		return 0, ErrSessionShutdown
	}

	// Compute the RTT
	return time.Now().Sub(start), nil
}

func (s *Session) closeRemoteStream(id uint32, sync bool) error {
	var timeout <-chan time.Time
	var err error
	if sync {
		err = s.writeFrame(newLenFrame(flagFIN, id, 0, nil), timeout)
	} else {
		err = s.writeFrameNowait(newLenFrame(flagFIN, id, 0, nil), timeout)
	}
	if nil != err {
		log.Printf("[WARN] pmux: failed to close remote: %v", err)
	}
	return err
}

func (s *Session) doWriteFrame(frame LenFrame, noWait bool, timeout <-chan time.Time) error {
	atomic.AddInt32(&s.sendingNum, 1)
	if s.IsShutdown() {
		atomic.AddInt32(&s.sendingNum, -1)
		putBytesToPool(frame)
		return ErrSessionShutdown
	}
	defer atomic.AddInt32(&s.sendingNum, -1)
	ready := sendReady{F: frame, Err: nil}
	if !noWait {
		ready.Err = make(chan error, 1)
	}

	select {
	case s.sendCh <- ready:
	case <-s.shutdownCh:
		putBytesToPool(frame)
		return ErrSessionShutdown
	case <-timeout:
		putBytesToPool(frame)
		return ErrConnectionWriteTimeout
	}
	if !noWait {
		select {
		case err := <-ready.Err:
			return err
		case <-s.shutdownCh:
			return ErrSessionShutdown
		case <-timeout:
			return ErrConnectionWriteTimeout
		}
	}
	return nil
}

func (s *Session) writeFrame(frame LenFrame, timeout <-chan time.Time) error {
	return s.doWriteFrame(frame, false, timeout)
}

func (s *Session) writeFrameNowait(frame LenFrame, timeout <-chan time.Time) error {
	return s.doWriteFrame(frame, true, timeout)
}

func (s *Session) updateWindow(sid uint32, delta uint32) error {
	var timeout <-chan time.Time
	frame := newLenFrame(flagWindowUpdate, sid, delta, nil)
	return s.writeFrameNowait(frame, timeout)
}

func (s *Session) incomingStream(id uint32) (*Stream, error) {
	//s.streamLock.Lock()
	ss := newStream(s, id)
	if _, loaded := s.streams.LoadOrStore(id, ss); loaded {
		log.Printf("[ERR]: duplicate stream declared")
		s.closeRemoteStream(id, false)
		return nil, ErrDuplicateStream
	}
	atomic.AddInt32(&s.streamsCounter, 1)
	return ss, nil
}

func (s *Session) getStream(sid uint32) *Stream {
	//s.streamLock.Lock()
	stream, exist := s.streams.Load(sid)
	//s.streamLock.Unlock()
	if exist {
		return stream.(*Stream)
	}
	return nil
}

func (s *Session) removeStream(sid uint32) {
	//s.streamLock.Lock()
	//delete(s.streams, sid)
	//s.streamLock.Unlock()
	s.streams.Delete(sid)
	atomic.AddInt32(&s.streamsCounter, -1)
}

func (s *Session) exitErr(err error) {
	s.shutdownLock.Lock()
	if s.shutdownErr == nil {
		s.shutdownErr = err
	}
	s.shutdownLock.Unlock()
	s.Close()
}

func (s *Session) recv() {
	if err := s.recvLoop(); err != nil {
		s.exitErr(err)
	}
}

func (s *Session) resetCryptoContext(method string, iv uint64, wait bool) error {
	if wait {
		var timeout <-chan time.Time
		s.writeFrame(nil, timeout)
	}
	ctx, err := NewCryptoContext(method, s.config.CipherKey, iv)
	if nil != err {
		return err
	}
	//ctx.encryptCounter = ctx.decryptCounter =
	s.cryptoContext = ctx
	return nil
}

func (s *Session) ResetCryptoContext(method string, iv uint64) error {
	return s.resetCryptoContext(method, iv, true)
}

func (s *Session) recvFrame(reader io.Reader) (Frame, error) {
	//lenbuf := make([]byte, 4)
	_, err := io.ReadAtLeast(reader, s.lenbuf[:], len(s.lenbuf))
	if nil != err {
		return nil, err
	}
	ctx := s.cryptoContext
	length := binary.BigEndian.Uint32(s.lenbuf[:])
	length = ctx.decodeLength(length)
	//log.Printf("[Recv]Read len:%d %d %d", length, binary.BigEndian.Uint32(lenbuf), ctx.decryptCounter)
	if length > maxDataPacketSize {
		return nil, ErrToolargeDataFrame
	}
	//buf := make([]byte, length)
	buf := getBytesFromPool(int(length))
	_, err = io.ReadAtLeast(reader, buf, len(buf))
	if nil != err {
		return nil, err
	}
	buf, err = ctx.decodeData(buf)
	if nil != err {
		return nil, err
	}

	frame := Frame(buf)
	// frame := &Frame{}
	// frame.Header = FrameHeader(buf[0:HeaderLenV1])
	// frame.Body = buf[HeaderLenV1:]
	//log.Printf("[Recv]Read frame %d %d %d %d %d", length, frame.Header().Flags(), frame.Header().StreamID(), len(frame.Body()), ctx.decryptCounter)
	ctx.incDecryptCounter()
	if frame.Header().Version() != FrameProtoVersion {
		return nil, fmt.Errorf("Invalid proto version:%d with data len:%d, flag:%d", frame.Header().Version(), length, frame.Header().Flags())
	}

	return frame, nil
}

func (s *Session) recvLoop() error {
	for !s.IsShutdown() {
		// Read the frame
		var frame Frame
		var err error
		if frame, err = s.recvFrame(s.connReader); err != nil {
			if err != io.EOF && !strings.Contains(err.Error(), "closed") && !strings.Contains(err.Error(), "reset by peer") {
				log.Printf("[ERROR]: Failed to read frame: %v while decrypt counter %d ", err, s.cryptoContext.decryptCounter)
			}
			return err
		}
		s.lastRecvTime = time.Now()
		//log.Printf("####Recv %d", frame.Header.Flags())
		// Switch on the type
		switch frame.Header().Flags() {
		case flagData:
			err = s.handleData(frame)
		case flagSYN:
			err = s.handleSYN(frame)
			putBytesToPool(frame)
		case flagFIN:
			err = s.handleFIN(frame)
			putBytesToPool(frame)
		case flagWindowUpdate:
			err = s.handleWindowUpdate(frame)
			putBytesToPool(frame)
		case flagPing:
			var timeout <-chan time.Time
			s.writeFrameNowait(newLenFrame(flagPingACK, frame.Header().StreamID(), 0, nil), timeout)
			putBytesToPool(frame)
		case flagPingACK:
			asyncNotify(s.pingCh)
			//fmt.Printf("####Recv flagPingACK\n")
			putBytesToPool(frame)
		default:
			return ErrInvalidMsgType

		}
	}
	return ErrSessionShutdown
}

func (s *Session) handleWindowUpdate(frame Frame) error {
	stream := s.getStream(frame.Header().StreamID())
	if nil != stream {
		stream.incrSendWindow(frame)
	} else {
		s.closeRemoteStream(frame.Header().StreamID(), false)
	}
	return nil
}

func (s *Session) handleData(frame Frame) error {
	stream := s.getStream(frame.Header().StreamID())
	if nil != stream {
		return stream.offerData(frame)
	} else {
		s.closeRemoteStream(frame.Header().StreamID(), false)
		putBytesToPool(frame)
	}
	return nil
}

func (s *Session) handleSYN(frame Frame) error {
	stream, err := s.incomingStream(frame.Header().StreamID())
	if nil == err {
		if !s.acceptable {
			log.Printf("[WARN] pmux: close incoming stream since current session is not acceptable.")
			stream.Close()
			return nil
		}
		stream.state = streamAccepting
		select {
		case s.acceptCh <- stream:
			return nil
		default:
			// Backlog exceeded! RST the stream
			log.Printf("[WARN] pmux: backlog exceeded:%d, forcing connection reset", len(s.acceptCh))
			stream.Close()
		}
		return nil
	}
	return nil
}

func (s *Session) handleFIN(frame Frame) error {
	stream := s.getStream(frame.Header().StreamID())
	if nil != stream {
		//log.Printf("[%d]passive close stream", stream.ID())
		stream.forceClose(true)
	}
	return nil
}

// send is a long running goroutine that sends data
func (s *Session) send() {
	readFrames := func() ([]sendReady, error) {
		var frs []sendReady
		for len(s.sendCh) > 0 {
			frame := <-s.sendCh
			frs = append(frs, frame)
		}
		if len(frs) == 0 {
			select {
			case frame := <-s.sendCh:
				frs = append(frs, frame)
			case <-s.shutdownCh:
				return frs, ErrSessionShutdown
			}
		}
		return frs, nil
	}
	for !s.IsShutdown() {
		frs, err := readFrames()
		var wbuffers net.Buffers
		if nil == err {
			for _, frame := range frs {
				wbuffers, err = encodeFrameToBuffers(wbuffers, frame.F, s.cryptoContext)
				//err = writeFrame(s.connWriter, frame.F, s.cryptoContext)
				//putBytesToPool(frame.F)
				if nil != err {
					break
				}
			}
		}
		if len(wbuffers) > 0 {
			_, err = wbuffers.WriteTo(s.connWriter)
		}
		for _, frame := range frs {
			putBytesToPool(frame.F)
			if nil != frame.Err {
				asyncSendErr(frame.Err, err)
			}
		}
		if err != nil {
			if err != ErrSessionShutdown {
				log.Printf("[ERR] pmux: Failed to write frames: %v", err)
			}
			s.exitErr(err)
			return
		}
	}
}

func (s *Session) NumStreams() int {
	return int(s.streamsCounter)
}

// Close is used to close the session and all streams.
// Attempts to send a GoAway before closing the connection.
func (s *Session) Close() error {
	s.shutdownLock.Lock()
	defer s.shutdownLock.Unlock()

	if s.IsShutdown() {
		return nil
	}
	atomic.StoreInt32(&s.shutdown, 1)
	if s.shutdownErr == nil {
		s.shutdownErr = ErrSessionShutdown
	}
	asyncNotify(s.shutdownCh)
	select {
	case s.acceptCh <- nil:
	default:
	}
	close(s.shutdownCh)
	s.conn.Close()

	//s.streamLock.Lock()
	//defer s.streamLock.Unlock()
	s.streams.Range(func(key, value interface{}) bool {
		stream := value.(*Stream)
		stream.forceClose(true)
		return true
	})
	for len(s.sendCh) > 0 {
		frame := <-s.sendCh
		putBytesToPool(frame.F)
	}
	RecycleBufReaderToPool(s.connReader)
	for atomic.LoadInt32(&s.sendingNum) > 0 {
		time.Sleep(10 * time.Nanosecond)
	}
	close(s.sendCh)
	return nil
}

// AcceptStream is used to block until the next available stream
// is ready to be accepted.
func (s *Session) AcceptStream() (*Stream, error) {
	if s.IsShutdown() {
		return nil, ErrSessionShutdown
	}
	s.acceptable = true
	select {
	case stream := <-s.acceptCh:
		if nil == stream {
			return nil, ErrSessionShutdown
		}
		stream.state = streamEstablished
		return stream, nil
	case <-s.shutdownCh:
		return nil, ErrSessionShutdown
	}
}

// IsClosed does a safe check to see if we have shutdown
func (s *Session) IsClosed() bool {
	return s.IsShutdown()
}

// OpenStream is used to create a new stream
func (s *Session) OpenStream() (*Stream, error) {
	if s.IsClosed() {
		return nil, ErrSessionShutdown
	}

GET_ID:
	// Get an ID, and check for stream exhaustion
	id := atomic.LoadUint32(&s.nextStreamID)
	if id >= math.MaxUint32-1 {
		return nil, ErrStreamsExhausted
	}
	if !atomic.CompareAndSwapUint32(&s.nextStreamID, id, id+2) {
		goto GET_ID
	}

	// Register the stream
	stream := newStream(s, id)
	s.streams.Store(id, stream)
	atomic.AddInt32(&s.streamsCounter, 1)
	var timeout <-chan time.Time
	err := s.writeFrame(newLenFrame(flagSYN, id, 0, nil), timeout)
	if nil != err {
		return nil, err
	}
	return stream, nil
}

func newSession(config *Config, conn io.ReadWriteCloser, client bool) *Session {
	s := &Session{
		config: config,
		// logger:     log.New(config.LogOutput, "", log.LstdFlags),
		conn:       conn,
		connReader: NewBufReaderFromPool(conn),
		connWriter: conn,
		// pings:      make(map[uint32]chan struct{}),
		//streams: make(map[uint32]*Stream),
		// inflight:   make(map[uint32]struct{}),
		// synCh:      make(chan struct{}, config.AcceptBacklog),
		acceptCh: make(chan *Stream, 8),
		sendCh:   make(chan sendReady, config.WriteQueueLimit),
		// recvDoneCh: make(chan struct{}),
		shutdownCh: make(chan struct{}),
		pingCh:     make(chan struct{}),
	}
	//default cipher

	err := s.resetCryptoContext(s.config.CipherMethod, s.config.CipherInitialCounter, false)
	if nil != err {
		panic(err)
	}

	if config.EnableCompress {
		//s.connReader = snappy.NewReader(s.connReader)
		//s.connWriter = snappy.NewWriter(s.connWriter)
	}
	if client {
		s.nextStreamID = 1
	} else {
		s.nextStreamID = 2
	}
	go s.recv()
	go s.send()
	if config.EnableKeepAlive {
		go s.keepalive()
	}
	return s
}

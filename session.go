package pmux

import (
	"bufio"
	"bytes"
	"io"
	"log"
	"math"
	"strings"
	"sync"
	"sync/atomic"
)

type Session struct {
	nextStreamID uint32
	config       *Config
	conn         io.ReadWriteCloser
	connReader   io.Reader
	connWriter   io.Writer
	// bufRead is a buffered reader
	//bufRead  *bufio.Reader
	streams    map[uint32]*Stream
	streamLock sync.Mutex
	acceptCh   chan *Stream
	sendCh     chan *Frame

	shutdown     bool
	shutdownErr  error
	shutdownCh   chan struct{}
	shutdownLock sync.Mutex

	handshakeDone bool

	cryptoContext *CryptoContext
}

func (s *Session) closeRemoteStream(id uint32) error {
	err := s.writeFrameHeaderData(newFrameHeader(flagFIN, id), nil)
	if nil != err {
		log.Printf("[WARN] pmux: failed to close remote: %v", err)
	}
	return err
}

func (s *Session) writeFrameHeaderData(header FrameHeader, body []byte) error {
	frame := &Frame{header, body}
	return s.writeFrame(frame)
}

func (s *Session) writeFrame(frame *Frame) error {
	select {
	case s.sendCh <- frame:
		return nil
	case <-s.shutdownCh:
		return ErrSessionShutdown
	}
}
func (s *Session) updateWindow(sid uint32, delta uint32) error {
	frame := &Frame{Header: newFrameHeader(flagWindowUpdate, sid)}
	return s.writeFrame(frame)
}

func (s *Session) incomingStream(id uint32) (*Stream, error) {
	s.streamLock.Lock()
	if _, ok := s.streams[id]; ok {
		s.streamLock.Unlock()
		log.Printf("[ERR]: duplicate stream declared")
		s.closeRemoteStream(id)
		return nil, ErrDuplicateStream
	}
	ss := newStream(s, id)
	s.streams[id] = ss
	s.streamLock.Unlock()
	return ss, nil
}

func (s *Session) getStream(sid uint32) *Stream {
	s.streamLock.Lock()
	stream, exist := s.streams[sid]
	s.streamLock.Unlock()
	if exist {
		return stream
	}
	return nil
}

func (s *Session) removeStream(sid uint32) {
	s.streamLock.Lock()
	delete(s.streams, sid)
	s.streamLock.Unlock()
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

func (s *Session) ResetCryptoContext(method string) error {
	ctx, err := NewCryptoContext(method, s.config.CipherKey)
	if nil != err {
		return err
	}
	s.cryptoContext = ctx
	return nil
}

func (s *Session) recvLoop() error {
	for !s.shutdown {
		// Read the frame
		var frame *Frame
		var err error
		if frame, err = recvFrame(s.connReader, s.cryptoContext); err != nil {
			if err != io.EOF && !strings.Contains(err.Error(), "closed") && !strings.Contains(err.Error(), "reset by peer") {
				log.Printf("[ERROR]: Failed to read frame: %v", err)
			}
			return err
		}
		//log.Printf("####Recv %d", frame.Header.Flags())
		// Switch on the type
		switch frame.Header.Flags() {
		case flagData:
			err = s.handleData(frame)
		case flagSYN:
			err = s.handleSYN(frame)
		case flagFIN:
			err = s.handleFIN(frame)
		case flagHandshake:
			err = s.handleHandshake(frame)
		default:
			return ErrInvalidMsgType

		}
	}
	return ErrSessionShutdown
}

func (s *Session) handleHandshake(frame *Frame) error {
	s.handshakeDone = true
	s.connReader = bufio.NewReader(s.connReader)
	//s.connReader = &cipher.StreamReader{S: s.cipherStream, R: s.connReader}
	//s.connWriter = &cipher.StreamWriter{S: s.cipherStream, W: s.connWriter}
	return nil
}

func (s *Session) handleData(frame *Frame) error {
	stream := s.getStream(frame.Header.StreamID())
	if nil != stream {
		return stream.offerData(frame.Body)
	} else {
		s.closeRemoteStream(frame.Header.StreamID())
	}
	return nil
}

func (s *Session) handleSYN(frame *Frame) error {
	stream, err := s.incomingStream(frame.Header.StreamID())
	if nil == err {
		select {
		case s.acceptCh <- stream:
			return nil
		default:
			// Backlog exceeded! RST the stream
			log.Printf("[WARN] pmux: backlog exceeded, forcing connection reset")
			s.removeStream(frame.Header.StreamID())
			s.closeRemoteStream(frame.Header.StreamID())
		}
		return nil
	}
	return nil
}

func (s *Session) handleFIN(frame *Frame) error {
	stream := s.getStream(frame.Header.StreamID())
	if nil != stream {
		stream.Close()
		s.removeStream(frame.Header.StreamID())
	}
	return nil
}

// send is a long running goroutine that sends data
func (s *Session) send() {
	readFrames := func() ([]*Frame, error) {
		var frs []*Frame
		for len(s.sendCh) > 0 {
			frame := <-s.sendCh
			if nil != frame {
				frs = append(frs, frame)
			} else {
				return frs, ErrSessionShutdown
			}
		}
		if len(frs) == 0 {
			select {
			case frame := <-s.sendCh:
				if nil != frame {
					frs = append(frs, frame)
				} else {
					return frs, ErrSessionShutdown
				}
			case <-s.shutdownCh:
				return frs, ErrSessionShutdown
			}
		}
		return frs, nil
	}

	for !s.shutdown {
		frs, err := readFrames()
		if nil == err {
			var buffer bytes.Buffer
			for _, frame := range frs {
				//log.Printf("###Write %d", frame.Header.Flags())
				err = writeFrame(&buffer, frame, s.cryptoContext)
				if nil != err {
					break
				}
			}
			if nil == err {
				log.Printf("###Write %d frames", len(frs))
				_, err = io.Copy(s.connWriter, &buffer)
			}
		}
		if err != nil {
			log.Printf("[ERR] pmux: Failed to write frames: %v", err)
			s.exitErr(err)
			return
		}
	}
}

// Close is used to close the session and all streams.
// Attempts to send a GoAway before closing the connection.
func (s *Session) Close() error {
	s.shutdownLock.Lock()
	defer s.shutdownLock.Unlock()

	if s.shutdown {
		return nil
	}
	s.shutdown = true
	if s.shutdownErr == nil {
		s.shutdownErr = ErrSessionShutdown
	}
	close(s.shutdownCh)
	s.conn.Close()

	s.streamLock.Lock()
	defer s.streamLock.Unlock()
	for _, stream := range s.streams {
		stream.forceClose()
	}
	return nil
}

// AcceptStream is used to block until the next available stream
// is ready to be accepted.
func (s *Session) AcceptStream() (*Stream, error) {
	select {
	case stream := <-s.acceptCh:
		return stream, nil
	case <-s.shutdownCh:
		return nil, ErrSessionShutdown
	}
}

// NumStreams returns the number of currently open streams
func (s *Session) NumStreams() int {
	s.streamLock.Lock()
	num := len(s.streams)
	s.streamLock.Unlock()
	return num
}

// IsClosed does a safe check to see if we have shutdown
func (s *Session) IsClosed() bool {
	return s.shutdown
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
	s.streamLock.Lock()
	s.streams[id] = stream
	s.streamLock.Unlock()

	err := s.writeFrameHeaderData(newFrameHeader(flagSYN, id), nil)
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
		connReader: conn,
		connWriter: conn,
		// pings:      make(map[uint32]chan struct{}),
		streams: make(map[uint32]*Stream),
		// inflight:   make(map[uint32]struct{}),
		// synCh:      make(chan struct{}, config.AcceptBacklog),
		acceptCh: make(chan *Stream, 5),
		sendCh:   make(chan *Frame, 64),
		// recvDoneCh: make(chan struct{}),
		shutdownCh: make(chan struct{}),
	}
	//default cipher
	ctx, err := NewCryptoContext("chacha20poly1305", s.config.CipherKey)
	if nil != err {
		panic(err)
	}
	s.cryptoContext = ctx

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
	// if config.EnableKeepAlive {
	// 	go s.keepalive()
	// }
	return s
}

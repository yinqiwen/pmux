package pmux

import "time"

// Config is used to tune the Yamux session
type Config struct {
	// AcceptBacklog is used to limit how many streams may be
	// waiting an accept.
	AcceptBacklog int
	// WriteBacklog is used to limit how many write events may be
	// waiting to execute.
	WriteQueueLimit int

	// EnableKeepalive is used to do a period keep alive
	// messages using a ping.
	EnableKeepAlive bool

	// KeepAliveInterval is how often to perform the keep alive
	KeepAliveInterval time.Duration

	// ConnectionWriteTimeout is meant to be a "safety valve" timeout after
	// we which will suspect a problem with the underlying connection and
	// close it. This is only applied to writes, where's there's generally
	// an expectation that things will move along quickly.
	ConnectionWriteTimeout time.Duration
	PingTimeout            time.Duration

	// MaxStreamWindowSize is used to control the maximum
	// window size that we allow for a stream.
	MaxStreamWindowSize uint32
	StreamMinRefresh    uint32

	EnableCompress bool

	CipherMethod         string
	CipherKey            []byte
	CipherInitialCounter uint64
}

// DefaultConfig is used to return a default configuration
func DefaultConfig() *Config {
	return &Config{
		AcceptBacklog:          64,
		WriteQueueLimit:        64,
		EnableKeepAlive:        true,
		KeepAliveInterval:      30 * time.Second,
		ConnectionWriteTimeout: 10 * time.Second,
		PingTimeout:            10 * time.Second,
		MaxStreamWindowSize:    initialStreamWindow,
		StreamMinRefresh:       uint32(32 << 10),
		EnableCompress:         false,
		CipherKey:              []byte("1231232134325423534265aadasdasfasfdsdasgdfs"),
	}
}

package fleetlink

import (
	"context"
	"time"
)

// Config carries every ADR-0114 §4/§8 constant. DefaultConfig is binding;
// tests shrink the timings, never production code.
type Config struct {
	PingInterval   time.Duration // app-level ping each side (§4: 5 s)
	PeerTimeout    time.Duration // no frame for this long → peer gone (§4: 12 s)
	WriteTimeout   time.Duration // a peer that can't take a write this long is dropped (§8: 10 s)
	ReadLimit      int64         // per inbound message (§8: 256 KiB); also the cap on what we send
	QueueMessages  int           // outbound queue, messages (§8: 64)
	QueueBytes     int           // outbound queue, bytes (§8: 1 MiB)
	MaxInFlight    int           // requests in flight each way (§8: 8)
	MaxConns       int           // links per main till (§1: 32)
	RequestTimeout time.Duration // deadline for a request that brings none, and for inbound handlers
	RetryAfter     time.Duration // Retry-After on the §1 cap's 503
	MaxReports     int           // latest-report store bound (one per till; §8)
}

// DefaultConfig returns ADR-0114's values.
func DefaultConfig() Config {
	return Config{
		PingInterval:   5 * time.Second,
		PeerTimeout:    12 * time.Second,
		WriteTimeout:   10 * time.Second,
		ReadLimit:      256 << 10,
		QueueMessages:  64,
		QueueBytes:     1 << 20,
		MaxInFlight:    8,
		MaxConns:       32,
		RequestTimeout: 5 * time.Second,
		RetryAfter:     30 * time.Second,
		MaxReports:     256,
	}
}

// CloseCode is why a link was closed; ws.go maps it onto the WebSocket
// close status (4xxx are ours, RFC 6455 §7.4.2 private range).
type CloseCode int

const (
	CloseNormal   CloseCode = 1000
	CloseShutdown CloseCode = 1001 // going away: this till is stopping
	CloseProtocol CloseCode = 1002 // non-text frame or similar
	CloseTryAgain CloseCode = 1013 // connection cap reached after the upgrade
	CloseReplaced CloseCode = 4001 // a newer link from the same till id
	CloseRevoked  CloseCode = 4003 // the till was unpaired
	CloseGone     CloseCode = 4008 // no frame within PeerTimeout
	CloseSlow     CloseCode = 4009 // a write did not complete within WriteTimeout

	// closeAfterBye is never on the wire: the peer said bye and is closing
	// its side, so there is no one to run a close handshake with.
	closeAfterBye CloseCode = -1
)

// Conn is the only thing Peer needs from a transport: whole messages in,
// whole messages out, and a coded close. Read and Write must be safe to
// call concurrently with each other (one reader, one writer) and with
// Close.
type Conn interface {
	Read(ctx context.Context) ([]byte, error)
	Write(ctx context.Context, msg []byte) error
	Close(code CloseCode, reason string)
}

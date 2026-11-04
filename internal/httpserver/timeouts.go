// Package httpserver builds the http.Server the till and its recovery
// (safe-mode) screen listen with, so both share one set of connection
// timeouts (ut-docs#5337).
package httpserver

import (
	"net/http"
	"time"
)

const (
	// ReadHeaderTimeout bounds how long a client may take to send its request
	// headers: the slowloris defence for a till listening on the LAN. LAN
	// header blocks are a few KB; 10s tolerates a Pi on poor wifi and matches
	// lantls's first-byte timeout. net/http also uses it as the TLS handshake
	// timeout (tlsHandshakeTimeout), so a half-finished handshake is cut too.
	ReadHeaderTimeout = 10 * time.Second

	// IdleTimeout closes a keep-alive connection that has sat idle between
	// requests. It exceeds http.DefaultTransport's 90s idle reuse window, so
	// a Go peer (replica till, cloud link) drops an idle connection before the
	// till does; browsers and the mobile apps retry a socket closed under
	// them. It never applies to a request that is in flight.
	IdleTimeout = 120 * time.Second

	// MaxHeaderBytes caps request line + headers (net/http answers 431 over
	// it). LAN headers are a few KB (session cookie, bearer, HX-Current-URL);
	// the net/http default is 1 MiB per connection.
	MaxHeaderBytes = 64 << 10

	// BodyReadGrace is how long a request body may take before any rate
	// applies (ut-docs#5338).
	BodyReadGrace = 15 * time.Second

	// BodyMinRate is the slowest sustained body upload accepted (64 kbit/s):
	// far under any LAN/Wi-Fi link, so a 10 MB catalogue import gets
	// 15s + 1280s, yet a trickler is cut at the grace.
	BodyMinRate = 8 << 10 // bytes per second

	// WriteGrace is how long one response write may block before any rate
	// applies (ut-docs#5429).
	WriteGrace = 15 * time.Second

	// WriteMinRate is the slowest sustained download accepted (64 kbit/s),
	// the same floor as BodyMinRate.
	WriteMinRate = 8 << 10 // bytes per second

	// writeChunk bounds one Write or sendfile slice so a stalled peer is cut
	// within WriteGrace + writeChunk/WriteMinRate (15s + 32s) rather than after
	// the whole body.
	writeChunk = 256 << 10
)

// limits are the tunable bounds behind New; tests shorten them via newWith.
type limits struct {
	hdr, idle             time.Duration
	bodyGrace             time.Duration
	bodyRate              int64
	writeGrace            time.Duration
	writeRate, writeChunk int64
}

// New returns a server for h with ReadHeaderTimeout, IdleTimeout and
// MaxHeaderBytes set, and h wrapped so a request body must arrive at
// BodyMinRate after BodyReadGrace (boundBodies, ut-docs#5338) and a response
// must be read at WriteMinRate after WriteGrace per write (boundWrites,
// ut-docs#5429).
//
// ReadTimeout and WriteTimeout are deliberately left zero: a flat ReadTimeout
// would cap 10MB catalogue uploads, backup/restore and plugin upload, and a
// flat WriteTimeout would sever SSE streams, the replica websocket and the
// 25s shell long-poll (pages.ShellPollMaxWait). The min-rate bounds above
// replace them: they start at the first byte moved, re-arm on every write
// and flush, and are cleared by net/http at hijack, so those long-held
// routes are untouched while a peer that stops reading or sending is cut.
func New(h http.Handler) *http.Server {
	return newWith(h, limits{
		hdr: ReadHeaderTimeout, idle: IdleTimeout,
		bodyGrace: BodyReadGrace, bodyRate: BodyMinRate,
		writeGrace: WriteGrace, writeRate: WriteMinRate, writeChunk: writeChunk,
	})
}

// newWith lets tests shorten the timeouts; production uses New.
func newWith(h http.Handler, l limits) *http.Server {
	return &http.Server{
		Handler:           boundWrites(boundBodies(h, l.bodyGrace, l.bodyRate), l.writeGrace, l.writeRate, l.writeChunk),
		ReadHeaderTimeout: l.hdr,
		IdleTimeout:       l.idle,
		MaxHeaderBytes:    MaxHeaderBytes,
	}
}

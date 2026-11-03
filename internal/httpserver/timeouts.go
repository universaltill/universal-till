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
)

// New returns a server for h with ReadHeaderTimeout and IdleTimeout set.
//
// ReadTimeout and WriteTimeout are deliberately left zero: with ReadTimeout
// unset net/http clears the read deadline once headers are in, so bodies
// (10MB catalogue uploads, backup/restore, plugin upload) are not capped, and
// a WriteTimeout would sever SSE streams, the replica websocket and the 25s
// shell long-poll (pages.ShellPollMaxWait).
func New(h http.Handler) *http.Server {
	return newWith(h, ReadHeaderTimeout, IdleTimeout)
}

// newWith lets tests shorten the timeouts; production uses New.
func newWith(h http.Handler, hdr, idle time.Duration) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: hdr,
		IdleTimeout:       idle,
	}
}

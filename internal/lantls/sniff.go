package lantls

import (
	"bufio"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

// FirstByteTimeout is how long a new connection may stay silent before it
// is handed to the HTTP server as plain HTTP, exactly as every connection
// was before TLS (a browser's speculative pre-connect sends nothing at
// first).
const FirstByteTimeout = 10 * time.Second

// tlsRecordHandshake is the first byte of every TLS ClientHello (record
// type 22). No HTTP request line starts with it.
const tlsRecordHandshake = 0x16

// Listen wraps ln so that a connection whose first byte is a TLS handshake
// is served over TLS with cfg, and every other connection as plain HTTP —
// both on the same port (ADR-0114 §7).
//
// The first byte is read off the accept path, one goroutine per connection
// (the HTTP server spends one per connection anyway), so a slow or silent
// client never holds up anyone else's Accept. A TLS connection is returned
// as a *tls.Conn, so net/http runs the handshake and sets Request.TLS.
func Listen(ln net.Listener, cfg *tls.Config) net.Listener {
	return newSniffListener(ln, cfg, FirstByteTimeout)
}

type sniffListener struct {
	net.Listener
	cfg       *tls.Config
	firstByte time.Duration

	conns     chan net.Conn
	errs      chan error
	done      chan struct{}
	closeOnce sync.Once
}

func newSniffListener(ln net.Listener, cfg *tls.Config, firstByte time.Duration) *sniffListener {
	s := &sniffListener{
		Listener:  ln,
		cfg:       cfg,
		firstByte: firstByte,
		conns:     make(chan net.Conn),
		errs:      make(chan error, 1),
		done:      make(chan struct{}),
	}
	go s.acceptLoop()
	return s
}

func (s *sniffListener) acceptLoop() {
	for {
		c, err := s.Listener.Accept()
		if err != nil {
			select {
			case s.errs <- err:
			case <-s.done:
				return
			}
			// A temporary error (EMFILE, ECONNABORTED) is passed on so
			// net/http backs off as it always has; then keep accepting.
			// Anything else ends the listener, as it ends Serve.
			if isTemporary(err) {
				continue
			}
			return
		}
		go s.sniff(c)
	}
}

func (s *sniffListener) sniff(c net.Conn) {
	// Close interrupts a Peek still waiting for the first byte, so Close
	// doesn't leave sockets open for up to firstByte afterwards.
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-s.done:
			_ = c.SetReadDeadline(time.Now())
		case <-stop:
		}
	}()
	br := bufio.NewReaderSize(c, 1)
	_ = c.SetReadDeadline(time.Now().Add(s.firstByte))
	b, err := br.Peek(1)
	close(stop)
	<-stopped
	if s.closed() {
		c.Close()
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	var out net.Conn = &peekedConn{Conn: c, r: br}
	switch {
	case err == nil && b[0] == tlsRecordHandshake:
		out = tls.Server(out, s.cfg)
	case err != nil && !isTimeout(err):
		// Closed or reset before sending anything: nothing to serve.
		c.Close()
		return
	}
	select {
	case s.conns <- out:
	case <-s.done:
		c.Close()
	}
}

func (s *sniffListener) closed() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func isTemporary(err error) bool {
	var te interface{ Temporary() bool }
	return errors.As(err, &te) && te.Temporary()
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// Accept returns the next sniffed connection.
func (s *sniffListener) Accept() (net.Conn, error) {
	// Never hand Serve a new conn once Close (Shutdown) has begun; select
	// alone would pick at random when both are ready.
	if s.closed() {
		return nil, net.ErrClosed
	}
	select {
	case c := <-s.conns:
		return c, nil
	case err := <-s.errs:
		return nil, err
	case <-s.done:
		return nil, net.ErrClosed
	}
}

// Close stops accepting; connections still being sniffed are closed.
func (s *sniffListener) Close() error {
	err := net.ErrClosed
	s.closeOnce.Do(func() {
		close(s.done)
		err = s.Listener.Close()
	})
	return err
}

// peekedConn replays the sniffed byte before reading the socket again.
//
// It forwards CloseWrite and ReadFrom from the underlying *net.TCPConn,
// which embedding the net.Conn interface would hide: net/http half-closes
// with CloseWrite before closing a conn whose request body it didn't read
// (an early 401/413), so the client gets the response instead of a reset,
// and serves files via ReadFrom (sendfile). Plain HTTP must behave exactly
// as it did before the sniff (ut-docs#2736 review).
type peekedConn struct {
	net.Conn
	r *bufio.Reader
}

func (p *peekedConn) Read(b []byte) (int, error) { return p.r.Read(b) }

func (p *peekedConn) CloseWrite() error {
	if cw, ok := p.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

func (p *peekedConn) ReadFrom(r io.Reader) (int64, error) {
	if rf, ok := p.Conn.(io.ReaderFrom); ok {
		return rf.ReadFrom(r)
	}
	return io.Copy(p.Conn, r)
}

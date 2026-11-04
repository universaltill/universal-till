package httpserver

import (
	"io"
	"net/http"
	"time"
)

// boundBodies gives every request that carries a body a read deadline that
// grows with the bytes actually received: grace + n/rate. A client that
// stalls or trickles is cut; a steady upload, however large, completes.
//
// Why it must be outermost and cover every route (ut-docs#5338): when a
// handler answers without reading the body, net/http drains up to 256 KiB of
// it with no deadline (ReadTimeout is zero), holding the goroutine and fd and
// delaying the response. A 401/403/404 on a stalled POST hit that as much as
// an exempt handler did.
//
// Requests without a body (ContentLength == 0) are untouched, so SSE, the
// replica websocket and the shell long-poll never get a read deadline.
func boundBodies(next http.Handler, grace time.Duration, rate int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength == 0 || r.Body == nil || r.Body == http.NoBody {
			next.ServeHTTP(w, r)
			return
		}
		rc := http.NewResponseController(w)
		start := time.Now()
		// Arms the bound even if nobody reads (the post-handler drain). Not
		// cleared when the handler returns; net/http re-arms its own header
		// deadline for the next keep-alive request.
		if err := rc.SetReadDeadline(start.Add(grace)); err != nil {
			next.ServeHTTP(w, r)
			return
		}
		r.Body = &rateBody{rc: rc, body: r.Body, start: start, grace: grace, rate: rate}
		next.ServeHTTP(w, r)
	})
}

// rateBody re-arms the read deadline before each Read until the body ends.
// After the first error (io.EOF included) it never re-arms: net/http clears
// the deadline itself at EOF, and re-arming then would let its background
// read time out and cancel r.Context() under a handler still working.
type rateBody struct {
	rc    *http.ResponseController
	body  io.ReadCloser
	start time.Time
	grace time.Duration
	rate  int64
	n     int64
	done  bool
}

func (b *rateBody) Read(p []byte) (int, error) {
	if !b.done {
		d := b.start.Add(b.grace + time.Duration(b.n)*time.Second/time.Duration(b.rate))
		_ = b.rc.SetReadDeadline(d)
	}
	n, err := b.body.Read(p)
	b.n += int64(n)
	if err != nil {
		b.done = true
	}
	return n, err
}

func (b *rateBody) Close() error { return b.body.Close() }

package fleetlink

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"
)

// wsConn adapts a coder/websocket connection to Conn — the one file that
// knows the transport (ADR-0114 §2: a move to SSE+POST or the cloud
// transport replaces this, not the semantics).
type wsConn struct {
	c       *websocket.Conn
	dialled bool // this side dialled (an additional till)
}

var errNonText = errors.New("fleetlink: non-text frame")

func (w wsConn) Read(ctx context.Context) ([]byte, error) {
	typ, b, err := w.c.Read(ctx)
	if err != nil {
		return nil, err
	}
	if typ != websocket.MessageText {
		return nil, errNonText
	}
	return b, nil
}

func (w wsConn) Write(ctx context.Context, msg []byte) error {
	return w.c.Write(ctx, websocket.MessageText, msg)
}

func (w wsConn) Close(code CloseCode, reason string) {
	if code == CloseSlow || code == closeAfterBye || code == CloseShutdown || (code == CloseGone && w.dialled) {
		// The peer is not reading (slow), already leaving (its bye), or was
		// just told we are (our bye): a close handshake would only hold
		// shutdown or a goroutine up to 5 s waiting on it. A dialling till
		// also skips it for a silent main till (gone): it wants to redial
		// now, not after a handshake nobody will answer.
		_ = w.c.CloseNow()
		return
	}
	_ = w.c.Close(websocket.StatusCode(code), reason)
}

// DialError is a link upgrade the main till refused with an HTTP status
// (the request reached it, so this is not a transport failure).
type DialError struct {
	Status     int
	RetryAfter time.Duration // from Retry-After on a 503; 0 when absent
}

func (e *DialError) Error() string {
	return fmt.Sprintf("fleetlink: link refused: HTTP %d", e.Status)
}

// dial opens the replica side of a link: the bearer rides in the
// Authorization header of the upgrade request, never in the URL. ctx
// bounds the handshake only.
func dial(ctx context.Context, url, bearer string, readLimit int64) (Conn, error) {
	hc := &http.Client{
		// The bearer must reach the main till only; a redirect is never
		// followed (Go would drop the header across hosts, but a
		// same-host redirect would not).
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	c, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPClient:      hc,
		HTTPHeader:      http.Header{"Authorization": {"Bearer " + bearer}},
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		if resp != nil && resp.StatusCode != http.StatusSwitchingProtocols {
			de := &DialError{Status: resp.StatusCode}
			if s, perr := strconv.Atoi(resp.Header.Get("Retry-After")); perr == nil && s > 0 {
				de.RetryAfter = time.Duration(min(s, 3600)) * time.Second
			}
			return nil, de
		}
		return nil, err
	}
	c.SetReadLimit(readLimit)
	return wsConn{c: c, dialled: true}, nil
}

// remoteCloseCode is the close code the other side sent, or 0 when the
// connection ended without a close frame.
func remoteCloseCode(err error) CloseCode {
	if s := websocket.CloseStatus(err); s != -1 {
		return CloseCode(s)
	}
	return 0
}

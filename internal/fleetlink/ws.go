package fleetlink

import (
	"context"
	"errors"

	"github.com/coder/websocket"
)

// wsConn adapts a coder/websocket connection to Conn — the one file that
// knows the transport (ADR-0114 §2: a move to SSE+POST or the cloud
// transport replaces this, not the semantics).
type wsConn struct{ c *websocket.Conn }

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
	if code == CloseSlow || code == closeAfterBye || code == CloseShutdown {
		// The peer is not reading (slow), already leaving (its bye), or was
		// just told we are (our bye): a close handshake would only hold
		// shutdown or a goroutine up to 5 s waiting on it.
		_ = w.c.CloseNow()
		return
	}
	_ = w.c.Close(websocket.StatusCode(code), reason)
}

package fleetlink

import (
	"context"
	"encoding/json"
	"net/http"
)

// ReaderSink hears the phone's one-way frames. ok is false when the frame
// failed the strict decode; r.IdempotencyKey is then whatever key could be
// read from it (possibly empty).
type ReaderSink interface {
	ReaderResult(p *Peer, r ReaderResult, ok bool)
}

// HandleReader registers the handler for a request type a reader sends.
func (h *Hub) HandleReader(typ string, fn RequestHandler) {}

// SetReaderSink installs the receiver of reader.result frames.
func (h *Hub) SetReaderSink(s ReaderSink) {}

// ServeReader upgrades an authenticated reader's request and runs its link.
func (h *Hub) ServeReader(w http.ResponseWriter, r *http.Request, readerID string) {
	http.Error(w, "not implemented", http.StatusServiceUnavailable)
}

// ServeReaderConn runs one reader link over any transport.
func (h *Hub) ServeReaderConn(readerID string, conn Conn) {
	conn.Close(CloseShutdown, "not implemented")
}

// DisconnectReader closes readerID's link at once.
func (h *Hub) DisconnectReader(readerID string) {}

// ReaderPeer is readerID's live link, or nil.
func (h *Hub) ReaderPeer(readerID string) *Peer { return nil }

// ReaderState is readerID's latest reported state; ok is false when it has
// no live link.
func (h *Hub) ReaderState(readerID string) (ReaderStatus, bool) { return ReaderStatus{}, false }

// ReaderCount is the number of live reader links.
func (h *Hub) ReaderCount() int { return 0 }

// Closed is closed once the link is going down.
func (p *Peer) Closed() <-chan struct{} { return nil }

// Notify queues a one-way frame.
func (p *Peer) Notify(typ string, payload any) error { return errNotImplemented }

// ID is the id this link is registered under.
func (p *Peer) ID() string { return p.tillID }

var _ = context.Background
var _ json.RawMessage

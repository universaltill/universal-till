package fleetlink

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"
)

// ut-docs#6538 R1b (ADR-0198 §6.3/§6.5, design-batch2 §4): the reader peer
// class of the hub. A paired phone card reader links to the same listener as
// a till, but it lives in its own registry with its own handler table and its
// own vocabulary: a till's handlers (sat.*) are unreachable from a reader and
// a reader's frames from a till, nothing about the shop is ever sent to it,
// and both classes count against the one MaxConns cap (ADR-0114 §1).

// ReaderSink hears the phone's one-way frames. ok is false when the frame
// failed the strict decode; r.IdempotencyKey is then whatever key could be
// read from it (possibly empty).
type ReaderSink interface {
	ReaderResult(p *Peer, r ReaderResult, ok bool)
}

// readerHost is the reader class's peerHost.
type readerHost struct{ h *Hub }

func (rh readerHost) helloFor(_ context.Context, readerID string) any {
	return ReaderHello{Role: "main", ReaderProtocol: ReaderProtocol, ReaderID: readerID}
}

func (rh readerHost) onFrame() (func(string), time.Duration) {
	every := rh.h.opts.OnFrameEvery
	if every <= 0 {
		every = 30 * time.Second
	}
	return rh.h.opts.OnReaderFrame, every
}

func (rh readerHost) handler(typ string) RequestHandler { return rh.h.readerHandler(typ) }

func (rh readerHost) gotHello(*Peer, Hello, json.RawMessage) {}

func (rh readerHost) oneWay(typ string) bool {
	return typ == TypeReaderResult || typ == TypeReaderStatus
}

// gotMessage takes the phone's one-way frames. Everything else the shared
// dispatch hands over (report, sync, fleet, pairing, cloud_checkin) is a till
// vocabulary a reader has no business in: ignored.
func (rh readerHost) gotMessage(p *Peer, env Envelope) {
	switch env.Type {
	case TypeReaderStatus:
		if st, err := DecodeReaderStatus(env.Payload); err == nil {
			rh.h.storeReaderStatus(p, st)
		}
	case TypeReaderResult:
		rh.h.mu.Lock()
		sink := rh.h.readerSink
		rh.h.mu.Unlock()
		if sink == nil {
			return
		}
		if r, err := DecodeReaderResult(env.Payload); err == nil {
			sink.ReaderResult(p, r, true)
		} else {
			sink.ReaderResult(p, ReaderResult{IdempotencyKey: readerResultKey(env.Payload)}, false)
		}
	}
}

// HandleReader registers the handler for a request type a reader sends.
// Types without one are answered unknown_type.
func (h *Hub) HandleReader(typ string, fn RequestHandler) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.readerHandlers[typ] = fn
}

func (h *Hub) readerHandler(typ string) RequestHandler {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.readerHandlers[typ]
}

// SetReaderSink installs the receiver of reader.result frames.
func (h *Hub) SetReaderSink(s ReaderSink) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.readerSink = s
}

func (h *Hub) storeReaderStatus(p *Peer, st ReaderStatus) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.readers[p.tillID] == p {
		h.readerStatus[p.tillID] = st
	}
}

// total is the number of live links of both classes (callers hold h.mu).
func (h *Hub) total() int { return len(h.peers) + len(h.readers) }

func (h *Hub) admitReader(readerID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return false
	}
	_, exists := h.readers[readerID]
	return exists || h.total() < h.cfg.MaxConns
}

// ServeReader upgrades an already-authenticated reader's request (the caller
// ran syncReader) and runs its link until it closes. Over the shared cap, or
// after Close, it answers 503 with Retry-After instead of upgrading.
func (h *Hub) ServeReader(w http.ResponseWriter, r *http.Request, readerID string) {
	if !h.admitReader(readerID) {
		w.Header().Set("Retry-After", strconv.Itoa(int(h.cfg.RetryAfter/time.Second)))
		http.Error(w, "too many links", http.StatusServiceUnavailable)
		return
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return // Accept already wrote the HTTP error
	}
	c.SetReadLimit(h.cfg.ReadLimit)
	h.ServeReaderConn(readerID, wsConn{c: c})
}

// ServeReaderConn runs one reader link over any transport until it closes. A
// link for a reader id that already has one replaces it.
func (h *Hub) ServeReaderConn(readerID string, conn Conn) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		conn.Close(CloseShutdown, "shutting down")
		return
	}
	old := h.readers[readerID]
	if old == nil && h.total() >= h.cfg.MaxConns {
		h.mu.Unlock()
		conn.Close(CloseTryAgain, "too many links")
		return
	}
	p := newPeer(readerHost{h}, h.cfg, "r", readerID, conn)
	h.readers[readerID] = p
	delete(h.readerStatus, readerID)
	h.wg.Add(1)
	h.mu.Unlock()
	defer h.wg.Done()

	if old != nil {
		old.shutdown(CloseReplaced, "replaced by a newer link", "")
	}
	p.run()

	h.mu.Lock()
	if h.readers[readerID] == p {
		delete(h.readers, readerID)
		delete(h.readerStatus, readerID)
	}
	h.mu.Unlock()
}

// DisconnectReader closes readerID's link at once (revoked, removed).
func (h *Hub) DisconnectReader(readerID string) {
	h.mu.Lock()
	p := h.readers[readerID]
	delete(h.readers, readerID)
	delete(h.readerStatus, readerID)
	h.mu.Unlock()
	if p != nil {
		p.shutdown(CloseRevoked, "reader revoked", "")
	}
}

// ReaderPeer is readerID's live link, or nil.
func (h *Hub) ReaderPeer(readerID string) *Peer {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.readers[readerID]
}

// ReaderState is readerID's latest reported state; ok is false when it has no
// live link. A linked reader that has not reported yet has an empty State.
func (h *Hub) ReaderState(readerID string) (ReaderStatus, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.readers[readerID] == nil {
		return ReaderStatus{}, false
	}
	return h.readerStatus[readerID], true
}

// ReaderCount is the number of live reader links.
func (h *Hub) ReaderCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.readers)
}

// Closed is closed once the link is going down.
func (p *Peer) Closed() <-chan struct{} { return p.stop }

// Notify queues a one-way frame. ErrBusy on a full queue, never a wait.
func (p *Peer) Notify(typ string, payload any) error { return p.notify(typ, payload) }

// ID is the id this link is registered under.
func (p *Peer) ID() string { return p.tillID }

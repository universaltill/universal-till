package fleetlink

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// The joining till's pending link, main-till side (ADR-0114 §2 and its
// 2026-11-03 amendment, ut-docs#5297 slice B1).
//
// A till that has asked to join (ADR-0033) and holds no bearer yet opens
// GET /api/sync/link with its pair-request id and secret in headers. The
// link carries one thing: the request's own outcome, so the joining till can
// fetch its token now instead of at its next 15 s poll. It is a nudge, never
// a carry — no token, name, code or other request's data is ever in scope
// here: a link knows its own id and a status string.
//
// It is deliberately not a Peer: a Peer sends `hello` first (till id,
// version, cursors, TLS pin — never for an unauthenticated peer) and answers
// unknown types instead of closing. Here nothing is sent but pings, one
// `pairing` frame, and a closing `bye`; anything unexpected inbound closes.

// Pending-link constants.
const (
	// HeaderPairRequestID and HeaderPairRequestSecret carry ADR-0033's
	// request id and request_secret on the upgrade request. The secret is
	// never in a URL.
	HeaderPairRequestID     = "X-UT-Pair-Request-Id"
	HeaderPairRequestSecret = "X-UT-Pair-Request-Secret"

	// PendingReadLimit bounds one inbound message on a pending link: the
	// peer is unauthenticated until approved, and only ever sends ping, pong
	// or bye.
	PendingReadLimit = 4 << 10
	// MaxPendingLinks caps live pending links (outside ADR-0114 §1's 32).
	MaxPendingLinks = 8

	// maxPendingIDLen bounds an inbound ping id echoed back as reply_to.
	maxPendingIDLen = 64
)

// Pairing statuses a pending link reports (PairingPayload.Status).
const (
	PairingApproved = "approved"
	PairingDenied   = "denied"
	PairingExpired  = "expired"
)

// PendingHub holds the main till's pending links, at most one per
// pair-request id and MaxPendingLinks in all.
type PendingHub struct {
	cfg    Config
	mu     sync.Mutex
	links  map[string]*pendingLink // pair-request id → its one link
	closed bool
	wg     sync.WaitGroup
}

// NewPendingHub returns an empty hub using cfg's PingInterval, PeerTimeout,
// WriteTimeout and RetryAfter.
func NewPendingHub(cfg Config) *PendingHub {
	return &PendingHub{cfg: cfg, links: map[string]*pendingLink{}}
}

func (h *PendingHub) admit(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return false
	}
	_, exists := h.links[id]
	return exists || len(h.links) < MaxPendingLinks
}

// Serve upgrades an ALREADY-VERIFIED request (the caller checked TLS, the
// per-source limit, and the id and secret against the stored commitment).
// Over MaxPendingLinks for a new id, or after Close, it answers 503 with
// Retry-After instead of upgrading. See ServeConn for expiresAt and recheck.
func (h *PendingHub) Serve(w http.ResponseWriter, r *http.Request, id string, expiresAt time.Time, recheck func() string) {
	if !h.admit(id) {
		w.Header().Set("Retry-After", strconv.Itoa(int(h.cfg.RetryAfter/time.Second)))
		http.Error(w, "too many pending links", http.StatusServiceUnavailable)
		return
	}
	// Accept's default origin check stays on: a browser cannot set the
	// pairing headers anyway.
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return // Accept already wrote the HTTP error
	}
	c.SetReadLimit(PendingReadLimit)
	h.ServeConn(id, wsConn{c: c}, expiresAt, recheck)
}

// ServeConn runs one pending link over any transport until it closes. A link
// for an id that already has one replaces it (CloseReplaced). expiresAt arms
// the link's own expiry: it sends "expired" and closes then (zero: never).
// After registering, recheck is called once and a non-empty result is
// resolved at once, which closes the race where the manager approved or
// denied between the caller's verification and the registration.
func (h *PendingHub) ServeConn(id string, conn Conn, expiresAt time.Time, recheck func() string) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		conn.Close(CloseShutdown, "shutting down")
		return
	}
	old := h.links[id]
	if old == nil && len(h.links) >= MaxPendingLinks {
		h.mu.Unlock()
		conn.Close(CloseTryAgain, "too many pending links")
		return
	}
	l := newPendingLink(h, id, conn, expiresAt)
	h.links[id] = l
	h.wg.Add(2) // this handler and the writer
	h.mu.Unlock()
	defer h.wg.Done()

	if old != nil {
		old.stopWith(CloseReplaced, "replaced by a newer link", "")
	}
	go l.writeLoop()
	if recheck != nil {
		if st := recheck(); st != "" {
			l.setStatus(st)
		}
	}
	l.readLoop()
	l.stopWith(CloseNormal, "link ended", "")
	<-l.done

	h.mu.Lock()
	if h.links[id] == l {
		delete(h.links, id)
	}
	h.mu.Unlock()
}

// Resolve records a terminal status for id's link (the first one wins) and
// wakes its writer, which sends one `pairing` frame and closes. O(1), never
// blocks; no link for id is a no-op.
func (h *PendingHub) Resolve(id, status string) {
	h.mu.Lock()
	l := h.links[id]
	h.mu.Unlock()
	if l != nil {
		l.setStatus(status)
	}
}

// Close says bye ("shutdown") on every pending link, closes them, refuses new
// ones and waits for every link goroutine to finish.
func (h *PendingHub) Close() {
	h.mu.Lock()
	h.closed = true
	links := make([]*pendingLink, 0, len(h.links))
	for _, l := range h.links {
		links = append(links, l)
	}
	h.mu.Unlock()
	for _, l := range links {
		l.stopWith(CloseShutdown, "shutting down", ByeShutdown)
	}
	h.wg.Wait()
}

// pendingLink is one connection: the handler goroutine reads, writeLoop is
// the only writer and the only closer. State is O(1): one status slot, one
// pong slot, no queue.
type pendingLink struct {
	h         *PendingHub
	id        string
	conn      Conn
	expiresAt time.Time

	ctx    context.Context
	cancel context.CancelFunc

	status  atomic.Pointer[string]
	pongFor atomic.Pointer[string]
	lastIn  atomic.Int64 // unix nano of the last valid inbound frame
	wake    chan struct{}
	seq     atomic.Uint64

	stop       chan struct{}
	stopOnce   sync.Once
	stopCode   CloseCode
	stopReason string
	stopBye    string

	done chan struct{} // closed when the writer has closed the conn
}

func newPendingLink(h *PendingHub, id string, conn Conn, expiresAt time.Time) *pendingLink {
	ctx, cancel := context.WithCancel(context.Background())
	l := &pendingLink{h: h, id: id, conn: conn, expiresAt: expiresAt, ctx: ctx, cancel: cancel,
		wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{})}
	l.lastIn.Store(time.Now().UnixNano())
	return l
}

func (l *pendingLink) poke() {
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

func (l *pendingLink) setStatus(st string) {
	s := st
	if l.status.CompareAndSwap(nil, &s) {
		l.poke()
	}
}

// stopWith ends the link; the first call's code wins.
func (l *pendingLink) stopWith(code CloseCode, reason, bye string) {
	l.stopOnce.Do(func() {
		l.stopCode, l.stopReason, l.stopBye = code, reason, bye
		close(l.stop)
	})
}

func (l *pendingLink) nextID() string { return "pl" + strconv.FormatUint(l.seq.Add(1), 10) }

func (l *pendingLink) readLoop() {
	for {
		b, err := l.conn.Read(l.ctx)
		if err != nil {
			if err == errNonText {
				l.stopWith(CloseProtocol, "text frames only", "")
			}
			return
		}
		env, err := Decode(b)
		if err != nil {
			l.stopWith(CloseProtocol, "bad frame", "")
			return
		}
		l.lastIn.Store(time.Now().UnixNano())
		switch env.Type {
		case TypePing:
			if len(env.ID) > maxPendingIDLen {
				l.stopWith(CloseProtocol, "bad frame", "")
				return
			}
			id := env.ID
			l.pongFor.Store(&id)
			l.poke()
		case TypePong:
		case TypeBye:
			l.stopWith(closeAfterBye, "bye", "")
			return
		default:
			// hello, pairing, sync, anything unknown, a reply: a pending
			// link speaks no other language.
			l.stopWith(CloseProtocol, "unexpected frame", "")
			return
		}
	}
}

// write sends one frame within WriteTimeout; a peer that cannot take it is
// dropped (CloseSlow).
func (l *pendingLink) write(typ, replyTo string, payload any) bool {
	m, err := newMessage(l.nextID(), typ, replyTo, payload)
	if err != nil {
		return false
	}
	b, err := Encode(m)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(l.ctx, l.h.cfg.WriteTimeout)
	defer cancel()
	if l.conn.Write(ctx, b) != nil {
		l.conn.Close(CloseSlow, "write timeout")
		return false
	}
	return true
}

func (l *pendingLink) writeLoop() {
	defer l.h.wg.Done()
	defer close(l.done)
	defer l.cancel()

	ping := time.NewTicker(l.h.cfg.PingInterval)
	defer ping.Stop()
	var expire <-chan time.Time
	if !l.expiresAt.IsZero() {
		t := time.NewTimer(time.Until(l.expiresAt))
		defer t.Stop()
		expire = t.C
	}
	for {
		select {
		case <-l.stop:
			if l.stopBye != "" {
				ctx, cancel := context.WithTimeout(l.ctx, min(l.h.cfg.WriteTimeout, time.Second))
				if m, err := newMessage(l.nextID(), TypeBye, "", ByePayload{Reason: l.stopBye}); err == nil {
					if b, err := Encode(m); err == nil {
						_ = l.conn.Write(ctx, b)
					}
				}
				cancel()
			}
			l.conn.Close(l.stopCode, l.stopReason)
			return
		case <-l.wake:
			if id := l.pongFor.Swap(nil); id != nil && !l.write(TypePong, *id, nil) {
				return
			}
			if st := l.status.Load(); st != nil {
				if l.write(TypePairing, "", PairingPayload{Status: *st}) {
					l.conn.Close(CloseNormal, "resolved")
				}
				return
			}
		case <-expire:
			expire = nil
			l.setStatus(PairingExpired)
		case <-ping.C:
			if time.Since(time.Unix(0, l.lastIn.Load())) > l.h.cfg.PeerTimeout {
				l.conn.Close(CloseGone, "no frame within timeout")
				return
			}
			if !l.write(TypePing, "", nil) {
				return
			}
		}
	}
}

package fleetlink

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// RequestHandler answers one inbound request from tillID. Return a
// *RemoteError to choose the error code the peer sees; any other error is
// "internal". It must honour ctx (RequestTimeout).
type RequestHandler func(ctx context.Context, tillID string, payload json.RawMessage) (any, error)

// HubOptions configures a Hub.
type HubOptions struct {
	Config
	// Hello builds the main till's hello for a link authenticated as
	// tillID. Nil sends a bare {"role":"main"}.
	Hello func(ctx context.Context, tillID string) Hello
	// OnFrame, when set, is called on the reader goroutine for an inbound
	// frame at most once per OnFrameEvery (default 30 s) per link — the
	// main till uses it to keep tills.last_seen_at fresh while a replica is
	// linked instead of polling.
	OnFrame      func(tillID string)
	OnFrameEvery time.Duration
}

// Hub is the main till's side of every link: at most one Peer per paired
// till id, at most MaxConns in total.
type Hub struct {
	opts HubOptions
	cfg  Config

	mu       sync.Mutex
	peers    map[string]*Peer
	reports  map[string]Report
	handlers map[string]RequestHandler
	closed   bool
	wg       sync.WaitGroup
}

// NewHub builds a Hub; zero Config fields take ADR-0114's defaults.
func NewHub(opts HubOptions) *Hub {
	opts.Config = withDefaults(opts.Config)
	return &Hub{
		opts:     opts,
		cfg:      opts.Config,
		peers:    map[string]*Peer{},
		reports:  map[string]Report{},
		handlers: map[string]RequestHandler{},
	}
}

func withDefaults(c Config) Config {
	d := DefaultConfig()
	if c.PingInterval <= 0 {
		c.PingInterval = d.PingInterval
	}
	if c.PeerTimeout <= 0 {
		c.PeerTimeout = d.PeerTimeout
	}
	if c.WriteTimeout <= 0 {
		c.WriteTimeout = d.WriteTimeout
	}
	if c.ReadLimit <= 0 {
		c.ReadLimit = d.ReadLimit
	}
	if c.QueueMessages <= 0 {
		c.QueueMessages = d.QueueMessages
	}
	if c.QueueBytes <= 0 {
		c.QueueBytes = d.QueueBytes
	}
	if c.MaxInFlight <= 0 {
		c.MaxInFlight = d.MaxInFlight
	}
	if c.MaxConns <= 0 {
		c.MaxConns = d.MaxConns
	}
	if c.RequestTimeout <= 0 {
		c.RequestTimeout = d.RequestTimeout
	}
	if c.RetryAfter <= 0 {
		c.RetryAfter = d.RetryAfter
	}
	if c.MaxReports <= 0 {
		c.MaxReports = d.MaxReports
	}
	return c
}

// Hub's peerHost side.

func (h *Hub) onFrame() (func(string), time.Duration) {
	every := h.opts.OnFrameEvery
	if every <= 0 {
		every = 30 * time.Second
	}
	return h.opts.OnFrame, every
}

func (h *Hub) helloFor(ctx context.Context, tillID string) Hello {
	hello := Hello{Role: "main"}
	if f := h.opts.Hello; f != nil {
		hello = f(ctx, tillID)
	}
	hello.PeerTillID = tillID
	return hello
}

func (h *Hub) gotHello(*Peer, Hello) {}

func (h *Hub) gotMessage(p *Peer, env Envelope) {
	if env.Type != TypeReport {
		return // sync/fleet/pairing are main → peer only; a peer sending them is ignored
	}
	if r, ok := decodeReport(env.Payload); ok {
		h.storeReportFrom(p, r)
	}
}

// Handle registers the handler for inbound requests of type typ (e.g. the
// satellite card's sat.start). Types without a handler are answered with
// an unknown_type error.
func (h *Hub) Handle(typ string, fn RequestHandler) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.handlers[typ] = fn
}

func (h *Hub) handler(typ string) RequestHandler {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.handlers[typ]
}

// admit reports whether a link for tillID may be opened now: the hub is
// open, and either tillID already has a link (it will be replaced) or
// there is room under MaxConns.
func (h *Hub) admit(tillID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return false
	}
	_, exists := h.peers[tillID]
	return exists || len(h.peers) < h.cfg.MaxConns
}

// Serve upgrades an already-authenticated request (the caller ran syncTill)
// to a WebSocket and runs the link until it closes. Over the cap (or after
// Close) it answers 503 with Retry-After instead of upgrading.
func (h *Hub) Serve(w http.ResponseWriter, r *http.Request, tillID string) {
	if !h.admit(tillID) {
		w.Header().Set("Retry-After", strconv.Itoa(int(h.cfg.RetryAfter/time.Second)))
		http.Error(w, "too many till links", http.StatusServiceUnavailable)
		return
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return // Accept already wrote the HTTP error
	}
	c.SetReadLimit(h.cfg.ReadLimit)
	h.ServeConn(tillID, wsConn{c: c})
}

// ServeConn runs one link over any transport until it closes. A link for
// a till id that already has one replaces it (§1).
func (h *Hub) ServeConn(tillID string, conn Conn) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		conn.Close(CloseShutdown, "shutting down")
		return
	}
	old := h.peers[tillID]
	if old == nil && len(h.peers) >= h.cfg.MaxConns {
		h.mu.Unlock()
		conn.Close(CloseTryAgain, "too many till links")
		return
	}
	p := newPeer(h, h.cfg, "m", tillID, conn)
	h.peers[tillID] = p
	h.wg.Add(1)
	h.mu.Unlock()
	defer h.wg.Done()

	if old != nil {
		old.shutdown(CloseReplaced, "replaced by a newer link", "")
	}
	p.run()

	h.mu.Lock()
	if h.peers[tillID] == p {
		delete(h.peers, tillID)
	}
	h.mu.Unlock()
}

// Nudge marks scopes dirty on every link. Non-blocking and O(1) per peer:
// a slow peer accumulates flags, never frames (§8).
func (h *Hub) Nudge(scopes ...Scope) {
	var mask uint32
	for _, s := range scopes {
		mask |= scopeBit(s)
	}
	if mask == 0 {
		return
	}
	h.mu.Lock()
	peers := make([]*Peer, 0, len(h.peers))
	for _, p := range h.peers {
		peers = append(peers, p)
	}
	h.mu.Unlock()
	for _, p := range peers {
		p.markDirty(mask)
	}
}

// Disconnect closes tillID's link and forgets its report — called when the
// till is revoked (§1).
func (h *Hub) Disconnect(tillID string) {
	h.mu.Lock()
	p := h.peers[tillID]
	delete(h.peers, tillID)
	delete(h.reports, tillID)
	h.mu.Unlock()
	if p != nil {
		p.shutdown(CloseRevoked, "till revoked", "")
	}
}

// Peer returns tillID's live link, or nil.
func (h *Hub) Peer(tillID string) *Peer {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.peers[tillID]
}

// Len is the number of live links.
func (h *Hub) Len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.peers)
}

// Report returns tillID's latest report.
func (h *Hub) Report(tillID string) (Report, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r, ok := h.reports[tillID]
	return r, ok
}

// storeReportFrom keeps a report only while p is still tillID's live link,
// so a revoked or replaced link can't write after it was forgotten.
func (h *Hub) storeReportFrom(p *Peer, r Report) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.peers[p.tillID] != p {
		return
	}
	h.storeReportLocked(p.tillID, r)
}

func (h *Hub) storeReportLocked(tillID string, r Report) {
	r.ReceivedAt = time.Now()
	if _, ok := h.reports[tillID]; !ok && len(h.reports) >= h.cfg.MaxReports {
		var oldest string
		var at time.Time
		for id, rep := range h.reports {
			if oldest == "" || rep.ReceivedAt.Before(at) {
				oldest, at = id, rep.ReceivedAt
			}
		}
		delete(h.reports, oldest)
	}
	h.reports[tillID] = r
}

// Close says bye ("shutdown") on every link, closes them, refuses new
// ones and waits for every link goroutine to finish.
func (h *Hub) Close() {
	h.mu.Lock()
	h.closed = true
	peers := make([]*Peer, 0, len(h.peers))
	for _, p := range h.peers {
		peers = append(peers, p)
	}
	h.mu.Unlock()
	for _, p := range peers {
		p.shutdown(CloseShutdown, "shutting down", ByeShutdown)
	}
	h.wg.Wait()
}

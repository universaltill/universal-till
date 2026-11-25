package readerlink

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/logging"
)

// The additional till's half of the relay (ut-docs#6538 phase 2). It reuses
// the whole Service (pending, link_lost, busy, cancel, mismatch, attention) and
// swaps only the peer it talks to: a relayPeer that speaks to the main over
// the till's own pinned link instead of a phone's link. Nothing here is
// persisted: no schema, no leftovers.

const (
	// DirectoryTTL is how long a reader.list answer may be used. Beyond it the
	// directory is empty: a stale "ready" must not offer a phone that left.
	DirectoryTTL = 15 * time.Second
	// maxRelayPeers bounds the keys the client remembers for inbound frames.
	maxRelayPeers = 64
	// ensureRetryAfter spaces the refresh attempts of Ensure.
	ensureRetryAfter = 2 * time.Second
	relayPeerTTL     = 10 * time.Minute
)

// RelayConn is the till's open link to its main (a *fleetlink.Peer).
type RelayConn interface {
	Request(ctx context.Context, typ string, payload any) (json.RawMessage, error)
	Notify(typ string, payload any) error
	Closed() <-chan struct{}
	// VerifiedPin is the pin of the certificate verified on this link ("" =
	// plain HTTP: relay is refused, the frame carries a PaymentIntent secret).
	VerifiedPin() string
	// Hello is the main's hello.
	Hello() (fleetlink.Hello, bool)
}

// ResultSink takes the main's forwarded verdict (the Service).
type ResultSink interface {
	ReaderResultFrom(p PhonePeer, r fleetlink.ReaderResult, ok bool)
}

// RelayClient is the additional till's view of the main's phone readers:
// a bounded, short-lived directory plus the relayPeer factory. It implements
// PhoneLink.
type RelayClient struct {
	link func() RelayConn
	now  func() time.Time

	mu      sync.Mutex
	sink    ResultSink
	rows    []fleetlink.RelayReader // at most MaxRelayReaders
	fetched time.Time
	tried   time.Time             // the last Ensure attempt: a failing main is not hammered on every page render
	peers   map[string]*relayPeer // by idempotency key, bounded
	order   []string
}

// NewRelayClient builds one. link returns the till's open link to its main, or
// nil while there is none.
func NewRelayClient(link func() RelayConn, now func() time.Time) *RelayClient {
	if now == nil {
		now = time.Now
	}
	return &RelayClient{link: link, now: now, peers: map[string]*relayPeer{}}
}

// Bind sets the Service that takes forwarded verdicts.
func (rc *RelayClient) Bind(s ResultSink) {
	rc.mu.Lock()
	rc.sink = s
	rc.mu.Unlock()
}

// conn is the link, only when it may carry a relay: up, pinned TLS, and the
// main advertises reader_relay (an older main, or phone pairing off, offers
// none: fail closed).
func (rc *RelayClient) conn() RelayConn {
	c := rc.link()
	if c == nil || c.VerifiedPin() == "" {
		return nil
	}
	if h, ok := c.Hello(); !ok || !h.ReaderRelay {
		return nil
	}
	return c
}

// Offered reports whether this till can relay at all right now.
func (rc *RelayClient) Offered() bool { return rc.conn() != nil }

// Refresh asks the main for its phone list (reader.list) and keeps it.
func (rc *RelayClient) Refresh(ctx context.Context) error {
	c := rc.conn()
	if c == nil {
		rc.mu.Lock()
		rc.rows, rc.fetched = nil, time.Time{}
		rc.mu.Unlock()
		return nil
	}
	raw, err := c.Request(ctx, fleetlink.TypeReaderList, struct{}{})
	if err != nil {
		var re *fleetlink.RemoteError
		if errors.As(err, &re) { // refused: nothing is offered
			rc.mu.Lock()
			rc.rows, rc.fetched = nil, time.Time{}
			rc.mu.Unlock()
		}
		return err
	}
	l, err := fleetlink.DecodeReaderListReply(raw)
	if err != nil {
		return err
	}
	rc.mu.Lock()
	rc.rows, rc.fetched = l.Readers, rc.now()
	rc.mu.Unlock()
	return nil
}

// Ensure refreshes when the directory is older than maxAge (bounded by ctx).
func (rc *RelayClient) Ensure(ctx context.Context, maxAge time.Duration) {
	rc.mu.Lock()
	now := rc.now()
	fresh := !rc.fetched.IsZero() && now.Sub(rc.fetched) < maxAge
	recent := !rc.tried.IsZero() && now.Sub(rc.tried) < ensureRetryAfter
	if !fresh && !recent {
		rc.tried = now
	}
	rc.mu.Unlock()
	if fresh || recent || rc.conn() == nil {
		return
	}
	if err := rc.Refresh(ctx); err != nil {
		logging.L().Debugf("readerlink: reader.list: %v", err)
	}
}

// Snapshot is the phones the main reported, as readers whose ID is their ref.
// Empty when the link cannot relay or the directory is older than DirectoryTTL.
func (rc *RelayClient) Snapshot() []Reader {
	if rc.conn() == nil {
		return nil
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if rc.fetched.IsZero() || rc.now().Sub(rc.fetched) >= DirectoryTTL {
		return nil
	}
	out := make([]Reader, 0, len(rc.rows))
	for _, r := range rc.rows {
		out = append(out, Reader{ID: r.Ref, Ref: r.Ref, Name: r.Name, Provider: r.Provider, Active: true})
	}
	return out
}

// States is the directory's presence by ref (the same freshness rule).
func (rc *RelayClient) States() map[string]string {
	out := map[string]string{}
	if rc.conn() == nil {
		return out
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if rc.fetched.IsZero() || rc.now().Sub(rc.fetched) >= DirectoryTTL {
		return out
	}
	for _, r := range rc.rows {
		out[r.Ref] = r.State
	}
	return out
}

// ByRef and ByID are Config lookups (a relayed reader's id is its ref).
func (rc *RelayClient) ByRef(_ context.Context, ref string) (Reader, bool) {
	for _, r := range rc.Snapshot() {
		if r.Ref == ref {
			return r, true
		}
	}
	return Reader{}, false
}

// ByID is ByRef: the relayed reader's id is its ref.
func (rc *RelayClient) ByID(ctx context.Context, id string) (Reader, bool) { return rc.ByRef(ctx, id) }

// ReaderState implements PhoneLink.
func (rc *RelayClient) ReaderState(id string) (fleetlink.ReaderStatus, bool) {
	st, ok := rc.States()[id]
	if !ok {
		return fleetlink.ReaderStatus{}, false
	}
	return fleetlink.ReaderStatus{State: st}, true
}

// ReaderPeer implements PhoneLink: one relayPeer per collect.
func (rc *RelayClient) ReaderPeer(id string) PhonePeer {
	c := rc.conn()
	if c == nil {
		return nil
	}
	if _, ok := rc.States()[id]; !ok {
		return nil
	}
	return &relayPeer{rc: rc, conn: c, closed: make(chan struct{})}
}

func (rc *RelayClient) register(p *relayPeer) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	now := rc.now()
	kept := rc.order[:0]
	for _, k := range rc.order {
		if q := rc.peers[k]; q != nil && now.Sub(q.at) < relayPeerTTL {
			kept = append(kept, k)
		} else {
			delete(rc.peers, k)
		}
	}
	rc.order = kept
	for len(rc.order) >= maxRelayPeers {
		delete(rc.peers, rc.order[0])
		rc.order = rc.order[1:]
	}
	p.at = now
	rc.peers[p.key] = p
	rc.order = append(rc.order, p.key)
}

func (rc *RelayClient) peerFor(key string, c RelayConn) *relayPeer {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if p := rc.peers[key]; p != nil && p.conn == c {
		return p
	}
	return nil
}

// OnResult takes the main's reader.relay.result (a fleetlink.ClientOptions
// callback: it must not block). A verdict for a key this till did not relay,
// or from another link, is dropped.
func (rc *RelayClient) OnResult(c RelayConn, r fleetlink.ReaderResult, ok bool) {
	rc.mu.Lock()
	sink := rc.sink
	rc.mu.Unlock()
	if p := rc.peerFor(r.IdempotencyKey, c); p != nil && sink != nil {
		sink.ReaderResultFrom(p, r, ok)
	}
}

// OnLost takes the main's reader.relay.lost: the phone is gone, which the
// Service treats exactly as a dropped phone link (pending, never declined).
func (rc *RelayClient) OnLost(c RelayConn, l fleetlink.ReaderRelayLost) {
	if p := rc.peerFor(l.IdempotencyKey, c); p != nil {
		p.shut()
	}
}

// relayPeer is the PhonePeer of one relayed collect.
type relayPeer struct {
	rc   *RelayClient
	conn RelayConn
	key  string
	at   time.Time

	closed chan struct{}
	once   sync.Once
}

func (p *relayPeer) shut() { p.once.Do(func() { close(p.closed) }) }

// Closed fires when the till's link to the main drops, or the main says the
// phone is gone.
func (p *relayPeer) Closed() <-chan struct{} { return p.closed }

// Request maps reader.collect to reader.relay. The relay is one request: the
// main's reply is {"accepted":true} or an error frame.
func (p *relayPeer) Request(ctx context.Context, typ string, payload any) (json.RawMessage, error) {
	if typ != fleetlink.TypeReaderCollect {
		return nil, errors.New("readerlink: relay carries collect only")
	}
	c, ok := payload.(fleetlink.ReaderCollect)
	if !ok {
		return nil, errors.New("readerlink: relay payload")
	}
	p.key = c.IdempotencyKey
	p.rc.register(p)
	go func() {
		defer logging.RecoverAndLog("readerlink.relayPeer")
		wait := time.Duration(c.TimeoutMs)*time.Millisecond + 30*time.Second
		t := time.NewTimer(wait)
		defer t.Stop()
		select {
		case <-p.conn.Closed():
			p.shut()
		case <-p.closed:
		case <-t.C:
		}
	}()
	raw, err := p.conn.Request(ctx, fleetlink.TypeReaderRelay, fleetlink.ReaderRelay{
		ReaderRef: c.ReaderID, Provider: c.Provider, AmountMinor: c.AmountMinor, Currency: c.Currency,
		IdempotencyKey: c.IdempotencyKey, TimeoutMs: c.TimeoutMs, ClientSecret: c.ClientSecret, LocationID: c.LocationID,
	})
	if err != nil {
		var re *fleetlink.RemoteError
		if errors.As(err, &re) {
			p.shut() // refused: nothing started, nothing more to watch
		}
	}
	return raw, err
}

// Notify maps reader.cancel to reader.relay.cancel.
func (p *relayPeer) Notify(typ string, payload any) error {
	if typ != fleetlink.TypeReaderCancel {
		return errors.New("readerlink: relay carries cancel only")
	}
	c, ok := payload.(fleetlink.ReaderCancel)
	if !ok {
		return errors.New("readerlink: relay payload")
	}
	return p.conn.Notify(fleetlink.TypeReaderRelayCancel, c)
}

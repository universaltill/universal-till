package fleetlink

import (
	"context"
	"encoding/json"
	"time"
)

// Session is one dialled link that speaks another protocol's message set
// over this package's envelope, transport, heartbeat, bounded queue and
// single-writer peer loop — the till's cloud link (ADR-0117 §1/§4: "same
// envelope, hello, close codes and bounds language" as ADR-0114, reused,
// not forked). The protocol's meaning lives with the caller: it builds its
// own hello payload, reads the far side's hello raw, and names the one-way
// types it wants delivered. ping/pong/bye/error are handled here, exactly
// as on the LAN link; any other undeclared type is answered unknown_type.
type Session struct {
	p     *Peer
	h     SessionHandlers
	types map[string]bool
}

// SessionHandlers is a Session's protocol half. OnHello and OnMessage run
// on the link's reader goroutine and must not block.
type SessionHandlers struct {
	// Hello builds this side's hello payload (the first frame).
	Hello func(ctx context.Context) any
	// OnHello is given the far side's hello payload.
	OnHello func(raw json.RawMessage)
	// OneWay lists the notification types delivered to OnMessage.
	OneWay    []string
	OnMessage func(env Envelope)
}

// SessionEnd says how a Session ended. Valid once Run has returned.
type SessionEnd struct {
	// RemoteCode is the close code the far side sent; 0 when the
	// connection ended without a close frame (a reset, a timeout, a bye).
	RemoteCode CloseCode
	// PeerBye is the reason in the far side's bye, if it said one.
	PeerBye string
	// TimedOut: no frame arrived within PeerTimeout (the heartbeat).
	TimedOut bool
}

// DialSession opens a Session to url with bearer in the Authorization
// header (never the URL; no Origin header; redirects never followed). A
// refused upgrade is a *DialError. ctx bounds the handshake only; Run
// drives the link.
func DialSession(ctx context.Context, url, bearer string, cfg Config, h SessionHandlers) (*Session, error) {
	cfg = withDefaults(cfg)
	conn, err := dial(ctx, url, bearer, cfg.ReadLimit)
	if err != nil {
		return nil, err
	}
	s := &Session{h: h, types: make(map[string]bool, len(h.OneWay))}
	for _, t := range h.OneWay {
		s.types[t] = true
	}
	s.p = newPeer(s, cfg, "t", "cloud", conn)
	return s, nil
}

// Run drives the link until it closes. Call it once.
func (s *Session) Run() { s.p.run() }

// Notify queues a one-way frame. ErrBusy on a full queue, ErrTooLarge over
// the read limit, ErrClosed once the link is gone — never a wait.
func (s *Session) Notify(typ string, payload any) error { return s.p.notify(typ, payload) }

// Close ends the link, saying bye with reason first when it is non-empty.
// Safe to call more than once, from any goroutine.
func (s *Session) Close(reason string) { s.p.shutdown(CloseShutdown, "closing", reason) }

// End reports how the link ended; call it after Run returns.
func (s *Session) End() SessionEnd {
	return SessionEnd{
		RemoteCode: remoteCloseCode(s.p.readErr),
		PeerBye:    s.p.peerBye,
		TimedOut:   s.p.closeCode == CloseGone,
	}
}

// peerHost.

func (s *Session) helloFor(ctx context.Context, _ string) any {
	if s.h.Hello == nil {
		return nil
	}
	return s.h.Hello(ctx)
}

func (s *Session) onFrame() (func(string), time.Duration) { return nil, 0 }

func (s *Session) handler(string) RequestHandler { return nil }

func (s *Session) gotHello(_ *Peer, _ Hello, raw json.RawMessage) {
	if s.h.OnHello != nil {
		s.h.OnHello(raw)
	}
}

func (s *Session) oneWay(typ string) bool { return s.types[typ] }

func (s *Session) gotMessage(_ *Peer, env Envelope) {
	if s.types[env.Type] && s.h.OnMessage != nil {
		s.h.OnMessage(env)
	}
}

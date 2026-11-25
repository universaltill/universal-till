package readerlink

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/logging"
)

// The main till's half of the relay (ut-docs#6538 phase 2, ADR-0198 amendment
// 2026-11-25). An additional till creates the PaymentIntent with its OWN
// credential and asks the main, over its pinned link, to forward the collect
// to a paired phone. The main creates no PaymentIntent, calls no broker
// operation and records no sale: it authorises, bounds, serialises (ONE lock
// per reader, shared with its own CollectRemote) and forwards. The ringing
// till confirms with the broker under its own credential; the phone's verdict
// is advice.

// RelayConfig wires the relay. Everything is a callback so the package stays
// free of pages/data wiring.
type RelayConfig struct {
	// TillRole is the stored role of a joined, non-revoked till ("" or
	// ok=false: unknown or revoked).
	TillRole func(ctx context.Context, tillID string) (role string, ok bool)
	// TillPeer is the live till link (the hub's TillPeer).
	TillPeer func(tillID string) *fleetlink.Peer
	// PairingOn is card_readers.phone_pairing.
	PairingOn func(ctx context.Context) bool
	// Currency is the shop currency (ISO code).
	Currency func(ctx context.Context) string
	// Readers lists the active paired phones.
	Readers func(ctx context.Context) []Reader
	// Audit receives one row per relay (never the secret).
	Audit func(RelayAudit)
	// PerMinute bounds relays per till (default 10).
	PerMinute int
}

// RelayAudit is one audit row: what was asked, of whom, with what outcome.
// It has no field for the client secret by construction.
type RelayAudit struct {
	TillID      string
	ReaderID    string
	Key         string
	AmountMinor int64
	Currency    string
	Outcome     string // an error code, a result status, or phone_link_lost / reader_revoked / till_gone / timeout
}

const (
	defaultRelayPerMinute = 10
	relayGrace            = 10 * time.Second // the main's lock outlives the till's window by this
)

func remote(code string) error { return &fleetlink.RemoteError{Code: code} }

// RelayOffered reports whether the main advertises relay in its hello: only
// while phone pairing is on.
func (s *Service) RelayOffered(ctx context.Context) bool {
	return s.cfg.Relay != nil && s.cfg.Relay.PairingOn != nil && s.cfg.Relay.PairingOn(ctx)
}

// relayCaller checks the calling till: a joined, non-revoked till with the
// stored role additional, linked over TLS (a relay frame carries a
// PaymentIntent secret). Satellites and kiosks stay out (ADR-0198 s6.10).
func (s *Service) relayCaller(ctx context.Context, tillID string) (*fleetlink.Peer, error) {
	rc := s.cfg.Relay
	if rc == nil {
		return nil, remote(fleetlink.CodeUnknownType)
	}
	role, ok := rc.TillRole(ctx, tillID)
	if !ok || role != data.TillRoleAdditional {
		return nil, remote(fleetlink.CodeRelayForbidden)
	}
	p := rc.TillPeer(tillID)
	if p == nil || !p.Secure() {
		return nil, remote(fleetlink.CodeRelayForbidden)
	}
	return p, nil
}

// ListHandler answers reader.list (a fleetlink.RequestHandler on the till
// vocabulary): the shop's paired phones with their presence. No ids, hashes
// or addresses leave the main.
func (s *Service) ListHandler(ctx context.Context, tillID string, _ json.RawMessage) (any, error) {
	if _, err := s.relayCaller(ctx, tillID); err != nil {
		return nil, err
	}
	if !s.RelayOffered(ctx) {
		return nil, remote(fleetlink.CodeReaderNotReady)
	}
	out := fleetlink.ReaderListReply{Readers: []fleetlink.RelayReader{}}
	for _, rd := range s.cfg.Relay.Readers(ctx) {
		if !rd.Active || rd.Ref == "" {
			continue
		}
		if len(out.Readers) >= fleetlink.MaxRelayReaders {
			break
		}
		state := fleetlink.ReaderUnavailable
		if st, linked := s.state(rd.ID); linked {
			switch st.State {
			case fleetlink.ReaderReady, fleetlink.ReaderBusy, fleetlink.ReaderNeedsSetup:
				state = st.State
			}
		}
		out.Readers = append(out.Readers, fleetlink.RelayReader{Ref: rd.Ref, Name: rd.Name, Provider: rd.Provider, State: state})
	}
	return out, nil
}

// RelayHandler answers reader.relay. Every check runs before anything is
// sent to a phone; each failure is an error frame, nothing started.
func (s *Service) RelayHandler(ctx context.Context, tillID string, raw json.RawMessage) (any, error) {
	tillPeer, err := s.relayCaller(ctx, tillID)
	if err != nil {
		return nil, err
	}
	rc := s.cfg.Relay
	if !s.RelayOffered(ctx) {
		return nil, remote(fleetlink.CodeReaderNotReady)
	}
	req, err := fleetlink.DecodeReaderRelay(raw)
	if err != nil || !strings.EqualFold(req.Currency, rc.Currency(ctx)) {
		return nil, remote(fleetlink.CodeRelayBadRequest) // nothing decoded: no key to audit
	}
	audit := RelayAudit{TillID: tillID, Key: req.IdempotencyKey, AmountMinor: req.AmountMinor, Currency: req.Currency}
	refuse := func(code string) (any, error) {
		audit.Outcome = code
		s.auditRelay(audit)
		return nil, &fleetlink.RemoteError{Code: code, Retryable: code == fleetlink.CodeReaderRateLimited}
	}
	if !s.allowRelay(tillID) {
		return refuse(fleetlink.CodeReaderRateLimited)
	}
	rd, ok := s.cfg.ByRef(ctx, req.ReaderRef)
	if !ok || !rd.Active {
		return refuse(fleetlink.CodeRelayUnknownReader)
	}
	audit.ReaderID = rd.ID
	peer := s.phone(rd.ID)
	st, linked := s.state(rd.ID)
	if peer == nil || !linked {
		return refuse(fleetlink.CodeReaderNotReady)
	}
	switch st.State {
	case fleetlink.ReaderReady:
	case fleetlink.ReaderBusy:
		return refuse(fleetlink.CodeReaderBusy)
	default:
		return refuse(fleetlink.CodeReaderNotReady)
	}
	if rd.Provider != req.Provider {
		return refuse(fleetlink.CodeRelayBadRequest)
	}

	window := time.Duration(req.TimeoutMs) * time.Millisecond
	c := &collect{key: req.IdempotencyKey, readerID: rd.ID, name: rd.Name, peer: peer, relayTo: tillPeer, tillID: tillID,
		deadline: s.cfg.Now().Add(window + relayGrace), started: s.cfg.Now(), phase: PhaseSent,
		results: make(chan phoneResult, 1), cancel: make(chan struct{}), sent: true}
	// ONE lock per reader, shared with the main's own collects; one relay in
	// flight per till.
	if !s.begin(c) {
		return refuse(fleetlink.CodeReaderBusy)
	}
	frame := fleetlink.ReaderCollect{ReaderID: req.ReaderRef, Provider: req.Provider, AmountMinor: req.AmountMinor,
		Currency: req.Currency, IdempotencyKey: req.IdempotencyKey, TimeoutMs: req.TimeoutMs,
		ClientSecret: req.ClientSecret, LocationID: req.LocationID}
	ackCtx, cancelAck := context.WithTimeout(ctx, s.cfg.AckTimeout)
	rawAck, err := peer.Request(ackCtx, fleetlink.TypeReaderCollect, frame)
	cancelAck()
	ackLost := false
	if err != nil {
		var re *fleetlink.RemoteError
		switch {
		case errors.As(err, &re):
			s.end(c)
			code := fleetlink.CodeReaderNotReady
			if re.Code == fleetlink.CodeReaderBusy {
				code = fleetlink.CodeReaderBusy
			}
			return refuse(code)
		case errors.Is(err, fleetlink.ErrBusy):
			s.end(c)
			return refuse(fleetlink.CodeReaderNotReady) // the frame never left the queue
		default:
			// Timeout, or the link closed while we waited: the phone may have
			// started. Not a refusal; the till is told its phone was lost.
			ackLost = true
		}
	} else {
		var ack fleetlink.ReaderCollectAck
		if json.Unmarshal(rawAck, &ack) != nil || !ack.Accepted {
			s.end(c)
			return refuse(fleetlink.CodeReaderBusy)
		}
	}
	s.setPhase(c, PhaseOnPhone)
	go s.relayWatch(c, rd.ID, ackLost, audit)
	return fleetlink.ReaderCollectAck{Accepted: true}, nil
}

// relayWatch runs until the phone's verdict was forwarded, a link dropped or
// the window ended; it then frees the lock. It never invents a verdict: a
// vanished phone is "lost", which the ringing till turns into pending.
func (s *Service) relayWatch(c *collect, readerID string, ackLost bool, audit RelayAudit) {
	defer logging.RecoverAndLog("readerlink.relayWatch")
	defer s.end(c)
	finish := func(outcome string) {
		audit.Outcome = outcome
		s.auditRelay(audit)
	}
	lost := func() {
		cause := fleetlink.RelayCausePhoneLinkLost
		if rp, ok := c.peer.(interface{ Revoked() bool }); ok && rp.Revoked() {
			cause = fleetlink.RelayCauseReaderRevoked
		}
		_ = c.relayTo.Notify(fleetlink.TypeReaderRelayLost, fleetlink.ReaderRelayLost{IdempotencyKey: c.key, Cause: cause})
		finish(cause)
	}
	forward := func(m phoneResult) {
		r := m.r
		if !m.ok {
			r = fleetlink.ReaderResult{IdempotencyKey: c.key, Status: fleetlink.ResultPending, Reason: "phone_pending"}
		}
		if err := c.relayTo.Notify(fleetlink.TypeReaderRelayResult, r); err != nil {
			logging.L().Warnf("readerlink: relay result for reader %s not queued: %v", readerID, err)
		}
		finish(r.Status)
	}
	if ackLost {
		_ = c.peer.Notify(fleetlink.TypeReaderCancel, fleetlink.ReaderCancel{IdempotencyKey: c.key})
		lost()
		return
	}
	timer := time.NewTimer(time.Until(c.deadline))
	defer timer.Stop()
	cancelCh := c.cancel
	for {
		select {
		case m := <-c.results:
			forward(m)
			return
		case <-c.peer.Closed():
			select { // a verdict that beat the close wins
			case m := <-c.results:
				forward(m)
			default:
				lost()
			}
			return
		case <-c.relayTo.Closed():
			// The ringing till went away: stop the phone (best effort; the SDK
			// ignores it past the point of no return). The till reconciles
			// through the broker.
			_ = c.peer.Notify(fleetlink.TypeReaderCancel, fleetlink.ReaderCancel{IdempotencyKey: c.key})
			finish("till_gone")
			return
		case <-cancelCh:
			cancelCh = nil
			_ = c.peer.Notify(fleetlink.TypeReaderCancel, fleetlink.ReaderCancel{IdempotencyKey: c.key})
		case <-timer.C:
			_ = c.peer.Notify(fleetlink.TypeReaderCancel, fleetlink.ReaderCancel{IdempotencyKey: c.key})
			finish("timeout")
			return
		}
	}
}

// RelayCancel implements fleetlink.RelaySink: the ringing till (and only it)
// stops its own relayed collect.
func (s *Service) RelayCancel(tillID string, cc fleetlink.ReaderCancel) {
	s.mu.Lock()
	c := s.byKey[cc.IdempotencyKey]
	s.mu.Unlock()
	if c == nil || c.relayTo == nil || c.tillID != tillID {
		return
	}
	c.once.Do(func() { close(c.cancel) })
}

func (s *Service) auditRelay(a RelayAudit) {
	if s.cfg.Relay != nil && s.cfg.Relay.Audit != nil {
		s.cfg.Relay.Audit(a)
	}
}

// allowRelay is the per-till rate limit (bounded: one slice per linked till,
// pruned on every call).
func (s *Service) allowRelay(tillID string) bool {
	limit := s.cfg.Relay.PerMinute
	if limit <= 0 {
		limit = defaultRelayPerMinute
	}
	now := s.cfg.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.relayRate == nil {
		s.relayRate = map[string][]time.Time{}
	}
	kept := s.relayRate[tillID][:0]
	for _, at := range s.relayRate[tillID] {
		if now.Sub(at) < time.Minute {
			kept = append(kept, at)
		}
	}
	if len(kept) >= limit {
		s.relayRate[tillID] = kept
		return false
	}
	s.relayRate[tillID] = append(kept, now)
	if len(s.relayRate) > 256 { // a till id that is gone costs nothing: drop idle entries
		for id, ts := range s.relayRate {
			if len(ts) == 0 || now.Sub(ts[len(ts)-1]) >= time.Minute {
				delete(s.relayRate, id)
			}
		}
	}
	return true
}

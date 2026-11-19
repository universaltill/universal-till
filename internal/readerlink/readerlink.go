// Package readerlink is core's side of a phone used as a card reader
// (ut-docs#6538 R1b, ADR-0198 §6.4/§6.5): the main till drives, the phone only
// collects. The main creates the PaymentIntent in-process through the broker
// (CallerCore), relays a Location-scoped connection token over the pinned
// reader link, asks the phone to collect and takes its verdict as ADVICE:
// every fact that matters is re-derived from the broker by pos.ConfirmTap
// before anything is recorded (that step is the payment gate's, not this
// package's). A link that drops mid-collect is pending, never declined.
package readerlink

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/devicepay"
	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/paybroker"
)

// Reader is one active paired phone reader as the registry knows it.
type Reader struct {
	ID       string // registry row id: the hub's key for the link
	Ref      string // rd_<hex>: what the tender carries
	Name     string
	Provider string // broker provider of the reader's payment method
	Active   bool   // not removed, credential not revoked
}

// Link is the hub's reader registry.
type Link interface {
	ReaderPeer(readerID string) *fleetlink.Peer
	ReaderState(readerID string) (fleetlink.ReaderStatus, bool)
}

// Attempts reads core's attempt rows (*data.PaymentAttemptRepo).
type Attempts interface {
	Get(ctx context.Context, key string) (data.PaymentAttempt, bool, error)
}

// Broker calls the payment broker as core (paybroker.CallerCore).
type Broker func(ctx context.Context, provider, op string, body []byte) ([]byte, error)

// Config wires a Service.
type Config struct {
	Link     Link
	ByRef    func(ctx context.Context, ref string) (Reader, bool)
	ByID     func(ctx context.Context, id string) (Reader, bool)
	Attempts Attempts
	Broker   Broker
	// TokensPerMinute bounds connection tokens per reader (default 5).
	TokensPerMinute int
	// Margin is how much earlier than the till's gate the phone's window ends.
	Margin time.Duration
	// AckTimeout bounds the wait for the phone's collect reply.
	AckTimeout time.Duration
	Now        func() time.Time
}

// Phases of a remote collect, as the till shows them.
const (
	PhaseSent       = "sent"
	PhaseOnPhone    = "on_phone"
	PhaseCancelling = "cancelling"
)

// Progress is what the cashier's screen shows while a phone collects.
type Progress struct {
	Phase      string
	ReaderName string
	Deadline   time.Time
}

// Bounds (till binding rule: every in-memory structure is bounded).
const (
	maxLost       = 64               // remembered link-lost keys
	defaultTokens = 5                // connection tokens per reader per minute
	brokerTimeout = 10 * time.Second // each broker call after the till's own window ended
)

// collect is one payment in flight on one reader.
type collect struct {
	key      string
	readerID string
	name     string
	peer     *fleetlink.Peer
	deadline time.Time
	started  time.Time
	phase    string // guarded by Service.mu
	results  chan phoneResult
	cancel   chan struct{}
	once     sync.Once
}

type phoneResult struct {
	r  fleetlink.ReaderResult
	ok bool
}

// Service is the remote collector.
type Service struct {
	cfg Config

	mu       sync.Mutex
	inflight map[string]*collect // by reader id: one collect per reader
	byKey    map[string]*collect // by idempotency key, while it can still take a result
	lost     []string
	dropped  int
	tokens   map[string][]time.Time
}

// New builds a Service.
func New(cfg Config) *Service {
	if cfg.TokensPerMinute <= 0 {
		cfg.TokensPerMinute = defaultTokens
	}
	if cfg.Margin <= 0 {
		cfg.Margin = 5 * time.Second
	}
	if cfg.AckTimeout <= 0 {
		cfg.AckTimeout = 5 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{cfg: cfg, inflight: map[string]*collect{}, byKey: map[string]*collect{}, tokens: map[string][]time.Time{}}
}

func notStarted(reason string) devicepay.Result {
	return devicepay.Result{Status: devicepay.StatusDeclined, Reason: reason}
}

// startFailed: nothing was charged and no card was ever read (no secret
// reached a phone, or the phone refused before it started).
func startFailed(reason string) devicepay.Result {
	return devicepay.Result{Status: devicepay.StatusDeclined, DeclineCode: "start_failed", Reason: reason}
}

func pendingResult(reason string) devicepay.Result {
	return devicepay.Result{Status: devicepay.StatusPending, Reason: reason}
}

// CollectRemote implements devicepay.RemoteCollector: it takes req on the
// phone reader ref and answers the advisory verdict. Amount, currency, key and
// provider come from core's attempt row, never from the phone; the card facts
// of an approval are not in the answer (core's ConfirmTap gets them from the
// broker).
func (s *Service) CollectRemote(ctx context.Context, ref string, req devicepay.Request) devicepay.Result {
	if err := ctx.Err(); err != nil {
		return pendingResult(devicepay.ReasonTimeout)
	}
	rd, ok := s.cfg.ByRef(ctx, ref)
	if !ok || !rd.Active {
		return notStarted(devicepay.ReasonReaderUnreachable)
	}
	peer := s.cfg.Link.ReaderPeer(rd.ID)
	st, linked := s.cfg.Link.ReaderState(rd.ID)
	if peer == nil || !linked {
		return notStarted(devicepay.ReasonReaderUnreachable)
	}
	switch st.State {
	case fleetlink.ReaderReady:
	case fleetlink.ReaderBusy:
		return notStarted(devicepay.ReasonReaderBusy)
	default:
		return notStarted(devicepay.ReasonReaderUnreachable)
	}
	a, found, err := s.cfg.Attempts.Get(ctx, req.IdempotencyKey)
	if err != nil || !found || a.State != data.AttemptPending || a.Provider != req.Provider ||
		a.AmountMinor != req.AmountMinor || a.AmountMinor <= 0 || !strings.EqualFold(a.Currency, req.Currency) {
		return notStarted(devicepay.ReasonBadRequest)
	}

	deadline, has := ctx.Deadline()
	if !has {
		deadline = s.cfg.Now().Add(2 * time.Minute)
	}
	c := &collect{key: a.Key, readerID: rd.ID, name: rd.Name, peer: peer, deadline: deadline, started: s.cfg.Now(),
		phase: PhaseSent, results: make(chan phoneResult, 1), cancel: make(chan struct{})}
	if !s.begin(c) {
		return notStarted(devicepay.ReasonReaderBusy)
	}
	defer s.end(c)

	// The main creates the PaymentIntent, in this process, as core.
	body, _ := json.Marshal(map[string]any{"idempotency_key": a.Key, "amount_minor": a.AmountMinor, "currency": a.Currency})
	out, err := s.cfg.Broker(ctx, a.Provider, "session_create", body)
	if err != nil {
		logging.L().Warnf("readerlink: session_create for %s: %s", rd.ID, paybroker.ErrorCode(err))
		return startFailed(paybroker.ErrorCode(err))
	}
	var pi struct {
		ID         string `json:"payment_intent_id"`
		AltID      string `json:"id"`
		Secret     string `json:"client_secret"`
		LocationID string `json:"location_id"`
	}
	if json.Unmarshal(out, &pi) != nil {
		return startFailed("unreadable_answer")
	}
	if pi.ID == "" {
		pi.ID = pi.AltID
	}
	if pi.ID == "" || pi.Secret == "" {
		return startFailed("unreadable_answer")
	}
	window := time.Until(deadline) - s.cfg.Margin
	if window <= 0 || ctx.Err() != nil {
		s.cancelIntent(a, pi.ID)
		return pendingResult(devicepay.ReasonTimeout)
	}

	frame := fleetlink.ReaderCollect{
		ReaderID: ref, Provider: a.Provider, AmountMinor: a.AmountMinor, Currency: a.Currency,
		IdempotencyKey: a.Key, TimeoutMs: window.Milliseconds(), ClientSecret: pi.Secret, LocationID: pi.LocationID,
	}
	ackCtx, cancelAck := context.WithTimeout(ctx, s.cfg.AckTimeout)
	raw, err := peer.Request(ackCtx, fleetlink.TypeReaderCollect, frame)
	cancelAck()
	if err != nil {
		var re *fleetlink.RemoteError
		switch {
		case errors.As(err, &re), errors.Is(err, fleetlink.ErrBusy):
			// The phone refused, or the frame never left the queue: no card
			// was read, so the PaymentIntent is simply unused.
			s.cancelIntent(a, pi.ID)
			reason := devicepay.ReasonReaderUnreachable
			if re != nil && re.Code == fleetlink.CodeReaderBusy {
				reason = devicepay.ReasonReaderBusy
			}
			return startFailed(reason)
		default:
			// Timeout or a link that closed while we waited: the phone may
			// have started. Pending, never declined.
			s.markLost(a.Key)
			_ = peer.Notify(fleetlink.TypeReaderCancel, fleetlink.ReaderCancel{IdempotencyKey: a.Key})
			return pendingResult(devicepay.ReasonLinkLost)
		}
	}
	var ack fleetlink.ReaderCollectAck
	if json.Unmarshal(raw, &ack) != nil || !ack.Accepted {
		s.cancelIntent(a, pi.ID)
		return startFailed(devicepay.ReasonReaderBusy)
	}
	s.setPhase(c, PhaseOnPhone)

	select {
	case m := <-c.results:
		return s.settle(a, pi.ID, m)
	case <-peer.Closed():
		select { // a result that beat the close wins
		case m := <-c.results:
			return s.settle(a, pi.ID, m)
		default:
		}
		s.markLost(a.Key)
		return pendingResult(devicepay.ReasonLinkLost)
	case <-c.cancel:
		s.setPhase(c, PhaseCancelling)
		return s.stop(peer, a, pi.ID, "cancelled")
	case <-ctx.Done():
		s.setPhase(c, PhaseCancelling)
		return s.stop(peer, a, pi.ID, devicepay.ReasonTimeout)
	}
}

func (s *Service) begin(c *collect) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inflight[c.readerID] != nil {
		return false
	}
	s.inflight[c.readerID] = c
	s.byKey[c.key] = c
	return true
}

func (s *Service) end(c *collect) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inflight[c.readerID] == c {
		delete(s.inflight, c.readerID)
	}
	if s.byKey[c.key] == c {
		delete(s.byKey, c.key)
	}
}

func (s *Service) setPhase(c *collect, phase string) {
	s.mu.Lock()
	c.phase = phase
	s.mu.Unlock()
}

func (s *Service) markLost(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.lost) >= maxLost {
		s.lost = s.lost[1:]
	}
	s.lost = append(s.lost, key)
}

// brokerStatus asks the broker (as core) for the PaymentIntent's state; "" when
// it cannot be told.
func (s *Service) brokerStatus(ctx context.Context, a data.PaymentAttempt, pi string) string {
	body, _ := json.Marshal(map[string]string{"idempotency_key": a.Key, "payment_intent_id": pi})
	out, err := s.cfg.Broker(ctx, a.Provider, "session_status", body)
	if err != nil {
		return ""
	}
	var st struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(paybroker.Neutral(out), &st) != nil {
		return ""
	}
	return st.Status
}

// cancelIntent cancels the PaymentIntent; true when the broker did it.
func (s *Service) cancelIntent(a data.PaymentAttempt, pi string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), brokerTimeout)
	defer cancel()
	body, _ := json.Marshal(map[string]string{"idempotency_key": a.Key, "payment_intent_id": pi})
	_, err := s.cfg.Broker(ctx, a.Provider, "session_cancel", body)
	return err == nil
}

// stop ends a collect on the till's side (cashier cancel or the window ran
// out): tell the phone, then cancel the PaymentIntent. A cancel that finds the
// payment already succeeded is applied as approved (#6304); one that cannot
// tell is pending.
func (s *Service) stop(peer *fleetlink.Peer, a data.PaymentAttempt, pi, reason string) devicepay.Result {
	_ = peer.Notify(fleetlink.TypeReaderCancel, fleetlink.ReaderCancel{IdempotencyKey: a.Key})
	if s.cancelIntent(a, pi) {
		return devicepay.Result{Status: devicepay.StatusCancelled, Reason: reason}
	}
	ctx, cancel := context.WithTimeout(context.Background(), brokerTimeout)
	defer cancel()
	switch s.brokerStatus(ctx, a, pi) {
	case "succeeded":
		return devicepay.Result{Status: devicepay.StatusApproved, ProviderTxnID: pi}
	case "canceled", "requires_payment_method":
		return devicepay.Result{Status: devicepay.StatusCancelled, Reason: reason}
	}
	return pendingResult(reason)
}

// settle turns the phone's advice into the answer for the gate. Approval is
// advice only (core's ConfirmTap re-derives it); a decline or cancel stands
// only when the broker agrees, since a wrong "declined" double-charges.
func (s *Service) settle(a data.PaymentAttempt, pi string, m phoneResult) devicepay.Result {
	if !m.ok {
		return pendingResult(devicepay.ReasonBadNativeResult)
	}
	switch m.r.Status {
	case fleetlink.ResultApproved:
		return devicepay.Result{Status: devicepay.StatusApproved, ProviderTxnID: pi}
	case fleetlink.ResultPending:
		reason := m.r.Reason
		if reason == "" {
			reason = "phone_pending"
		}
		return pendingResult(reason)
	}
	ctx, cancel := context.WithTimeout(context.Background(), brokerTimeout)
	defer cancel()
	switch s.brokerStatus(ctx, a, pi) {
	case "succeeded":
		return devicepay.Result{Status: devicepay.StatusApproved, ProviderTxnID: pi}
	case "requires_payment_method", "canceled":
		if m.r.Status == fleetlink.ResultCancelled {
			s.cancelIntent(a, pi)
			return devicepay.Result{Status: devicepay.StatusCancelled}
		}
		return devicepay.Result{Status: devicepay.StatusDeclined, DeclineCode: m.r.DeclineCode, Reason: m.r.Reason}
	}
	return pendingResult("unconfirmed")
}

// ReaderResult implements fleetlink.ReaderSink. A result counts only for a key
// this main issued, on the link the collect was sent on, once: anything else
// is dropped and counted (never logged with content).
func (s *Service) ReaderResult(p *fleetlink.Peer, r fleetlink.ReaderResult, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.byKey[r.IdempotencyKey]
	if c == nil || c.peer != p {
		s.dropped++
		return
	}
	delete(s.byKey, r.IdempotencyKey) // one result per key
	select {
	case c.results <- phoneResult{r: r, ok: ok}:
	default:
		s.dropped++
	}
}

// CancelActive stops every collect in flight (the cashier's Cancel). It
// reports whether there was one.
func (s *Service) CancelActive() bool {
	s.mu.Lock()
	var cs []*collect
	for _, c := range s.inflight {
		cs = append(cs, c)
	}
	s.mu.Unlock()
	for _, c := range cs {
		c.once.Do(func() { close(c.cancel) })
	}
	return len(cs) > 0
}

// TokenHandler answers a phone's reader.token request (a
// fleetlink.RequestHandler): a connection token from the broker, as core, for
// a reader that is still enrolled and in a state that may hold one. The
// Location is the store's (the broker derives it), never the frame's; nothing
// from the frame is forwarded; the token is never logged.
func (s *Service) TokenHandler(ctx context.Context, readerID string, _ json.RawMessage) (any, error) {
	rd, ok := s.cfg.ByID(ctx, readerID)
	if !ok || !rd.Active {
		return nil, &fleetlink.RemoteError{Code: fleetlink.CodeReaderNotReady}
	}
	st, linked := s.cfg.Link.ReaderState(readerID)
	if !linked || (st.State != fleetlink.ReaderReady && st.State != fleetlink.ReaderBusy) {
		return nil, &fleetlink.RemoteError{Code: fleetlink.CodeReaderNotReady}
	}
	if !s.allowToken(readerID) {
		return nil, &fleetlink.RemoteError{Code: fleetlink.CodeReaderRateLimited, Retryable: true}
	}
	out, err := s.cfg.Broker(ctx, rd.Provider, "connection_token", []byte(`{}`))
	if err != nil {
		logging.L().Warnf("readerlink: connection_token for reader %s: %s", readerID, paybroker.ErrorCode(err))
		return nil, &fleetlink.RemoteError{Code: "unavailable", Retryable: true}
	}
	var t fleetlink.ReaderTokenReply
	if json.Unmarshal(out, &t) != nil || t.Secret == "" {
		return nil, &fleetlink.RemoteError{Code: "unavailable", Retryable: true}
	}
	return fleetlink.ReaderTokenReply{Secret: t.Secret, LocationID: t.LocationID}, nil
}

func (s *Service) allowToken(readerID string) bool {
	now := s.cfg.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.tokens[readerID][:0]
	for _, at := range s.tokens[readerID] {
		if now.Sub(at) < time.Minute {
			kept = append(kept, at)
		}
	}
	if len(kept) >= s.cfg.TokensPerMinute {
		s.tokens[readerID] = kept
		return false
	}
	s.tokens[readerID] = append(kept, now)
	return true
}

// Progress is the newest in-flight collect's state, for the cashier's screen.
func (s *Service) Progress() (Progress, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best *collect
	for _, c := range s.inflight {
		if best == nil || c.started.After(best.started) {
			best = c
		}
	}
	if best == nil {
		return Progress{}, false
	}
	return Progress{Phase: best.phase, ReaderName: best.name, Deadline: best.deadline}, true
}

// LostContact reports that the collect for key ended because the link dropped.
func (s *Service) LostContact(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.lost {
		if k == key {
			return true
		}
	}
	return false
}

// Dropped counts result frames ignored (unknown key, other link, settled).
func (s *Service) Dropped() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped
}

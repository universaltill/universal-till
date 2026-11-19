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
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/devicepay"
	"github.com/universaltill/universal-till/internal/fleetlink"
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

// Service is the remote collector.
type Service struct{ cfg Config }

// New builds a Service.
func New(cfg Config) *Service { return &Service{cfg: cfg} }

// CollectRemote implements devicepay.RemoteCollector.
func (s *Service) CollectRemote(ctx context.Context, ref string, req devicepay.Request) devicepay.Result {
	return devicepay.Result{}
}

// TokenHandler answers a phone's reader.token request (a fleetlink.RequestHandler).
func (s *Service) TokenHandler(ctx context.Context, readerID string, payload json.RawMessage) (any, error) {
	return nil, nil
}

// ReaderResult implements fleetlink.ReaderSink.
func (s *Service) ReaderResult(p *fleetlink.Peer, r fleetlink.ReaderResult, ok bool) {}

// Progress is the newest in-flight collect's state.
func (s *Service) Progress() (Progress, bool) { return Progress{}, false }

// LostContact reports that the collect for key ended because the link dropped.
func (s *Service) LostContact(key string) bool { return false }

// Dropped counts result frames ignored (unknown key, other link, settled).
func (s *Service) Dropped() int { return 0 }

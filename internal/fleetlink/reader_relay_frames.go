package fleetlink

import (
	"encoding/json"
	"errors"
	"regexp"
)

// ut-docs#6538 phase 2 (ADR-0198 amendment 2026-11-25): an additional till
// sends a sale to a phone card reader THROUGH its main till. The main stays
// the only hub; these frames travel only on the till<->main link, which must
// be pinned TLS (a relay frame carries a PaymentIntent client secret). The
// reader frames themselves (reader.collect etc.) are unchanged, so the
// Android and iOS readers need no protocol change. Goldens:
// testdata/reader_relay_*.json and reader_list_*.json (Go-owned).
const (
	// TypeReaderList: till -> main, a request answered with a reply of the
	// same type (ReaderListReply): the paired phones of the shop.
	TypeReaderList = "reader.list"
	// TypeReaderRelay: till -> main, a request: forward this collect to a
	// phone. The reply is {"accepted":true} (ReaderCollectAck) or an error
	// frame (busy, not_ready, unknown_reader, bad_request, forbidden,
	// rate_limited); nothing was started on an error.
	TypeReaderRelay = "reader.relay"
	// TypeReaderRelayCancel: till -> main, one-way: stop this collect.
	TypeReaderRelayCancel = "reader.relay.cancel"
	// TypeReaderRelayResult: main -> till, one-way, the phone's ReaderResult
	// verbatim. ADVISORY: the till re-derives everything from the broker.
	TypeReaderRelayResult = "reader.relay.result"
	// TypeReaderRelayLost: main -> till, one-way: the phone went away.
	TypeReaderRelayLost = "reader.relay.lost"
)

// Error codes of the relay, besides the reader ones.
const (
	CodeRelayForbidden     = "forbidden"
	CodeRelayBadRequest    = "bad_request"
	CodeRelayUnknownReader = "unknown_reader"
)

// Causes of a reader.relay.lost.
const (
	RelayCausePhoneLinkLost = "phone_link_lost"
	RelayCauseReaderRevoked = "reader_revoked"
	MaxRelayReaders         = 32
	MaxRelayAmountMinor     = 10_000_000
	MinRelayTimeoutMs       = 5_000
	MaxRelayTimeoutMs       = 180_000
	maxRelaySecretBytes     = 200
	maxRelayReaderNameBytes = 64
)

// ReaderRelay asks the main to forward one collect to a phone reader.
type ReaderRelay struct {
	ReaderRef      string `json:"reader_ref"`
	Provider       string `json:"provider"`
	AmountMinor    int64  `json:"amount_minor"`
	Currency       string `json:"currency"`
	IdempotencyKey string `json:"idempotency_key"`
	TimeoutMs      int64  `json:"timeout_ms"`
	ClientSecret   string `json:"client_secret"`
	LocationID     string `json:"location_id,omitempty"`
}

// RelayReader is one row of the shop's phone list: no ids, hashes or addresses.
type RelayReader struct {
	Ref      string `json:"ref"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	State    string `json:"state"`
}

// ReaderListReply answers reader.list.
type ReaderListReply struct {
	Readers []RelayReader `json:"readers"`
}

// ReaderRelayLost tells the ringing till its phone is gone.
type ReaderRelayLost struct {
	IdempotencyKey string `json:"idempotency_key"`
	Cause          string `json:"cause"`
}

var (
	refRE      = regexp.MustCompile(`^rd_[A-Za-z0-9]{1,64}$`)
	currencyRE = regexp.MustCompile(`^[A-Za-z]{3}$`)
	// A PaymentIntent client secret: pi_<id>_secret_<token>.
	secretRE   = regexp.MustCompile(`^pi_[A-Za-z0-9]{8,}_secret_[A-Za-z0-9]{8,}$`)
	locationRE = regexp.MustCompile(`^(tml|loc)_[A-Za-z0-9]+$`)
)

// DecodeReaderRelay reads a reader.relay payload strictly and bounds every
// field. The timeout is clamped to [MinRelayTimeoutMs, MaxRelayTimeoutMs],
// not refused. Currency equality with the shop's is the caller's check.
func DecodeReaderRelay(raw json.RawMessage) (ReaderRelay, error) {
	var r ReaderRelay
	if err := strictJSON(raw, &r); err != nil {
		return ReaderRelay{}, err
	}
	switch {
	case !refRE.MatchString(r.ReaderRef):
		return ReaderRelay{}, errors.New("bad reader ref")
	case !slugRE.MatchString(r.Provider):
		return ReaderRelay{}, errors.New("bad provider")
	case r.AmountMinor < 1 || r.AmountMinor > MaxRelayAmountMinor:
		return ReaderRelay{}, errors.New("amount out of bounds")
	case !currencyRE.MatchString(r.Currency):
		return ReaderRelay{}, errors.New("bad currency")
	case !keyRE.MatchString(r.IdempotencyKey):
		return ReaderRelay{}, errors.New("bad idempotency key")
	case len(r.ClientSecret) > maxRelaySecretBytes || !secretRE.MatchString(r.ClientSecret):
		return ReaderRelay{}, errors.New("bad client secret")
	case r.LocationID != "" && !locationRE.MatchString(r.LocationID):
		return ReaderRelay{}, errors.New("bad location")
	}
	r.TimeoutMs = min(max(r.TimeoutMs, MinRelayTimeoutMs), MaxRelayTimeoutMs)
	return r, nil
}

// DecodeReaderRelayCancel reads a reader.relay.cancel payload strictly.
func DecodeReaderRelayCancel(raw json.RawMessage) (ReaderCancel, error) {
	var c ReaderCancel
	if err := strictJSON(raw, &c); err != nil {
		return ReaderCancel{}, err
	}
	if !keyRE.MatchString(c.IdempotencyKey) {
		return ReaderCancel{}, errors.New("bad idempotency key")
	}
	return c, nil
}

// DecodeReaderRelayLost reads a reader.relay.lost payload strictly.
func DecodeReaderRelayLost(raw json.RawMessage) (ReaderRelayLost, error) {
	var l ReaderRelayLost
	if err := strictJSON(raw, &l); err != nil {
		return ReaderRelayLost{}, err
	}
	if !keyRE.MatchString(l.IdempotencyKey) {
		return ReaderRelayLost{}, errors.New("bad idempotency key")
	}
	if l.Cause != RelayCausePhoneLinkLost && l.Cause != RelayCauseReaderRevoked {
		return ReaderRelayLost{}, errors.New("unknown cause")
	}
	return l, nil
}

// DecodeReaderListReply reads a reader.list reply: at most MaxRelayReaders
// rows, a row with a bad ref/provider or an unknown state is dropped.
func DecodeReaderListReply(raw json.RawMessage) (ReaderListReply, error) {
	var l ReaderListReply
	if err := strictJSON(raw, &l); err != nil {
		return ReaderListReply{}, err
	}
	out := make([]RelayReader, 0, len(l.Readers))
	for _, r := range l.Readers {
		if len(out) >= MaxRelayReaders {
			break
		}
		switch r.State {
		case ReaderReady, ReaderBusy, ReaderUnavailable, ReaderNeedsSetup:
		default:
			continue
		}
		if !refRE.MatchString(r.Ref) || !slugRE.MatchString(r.Provider) {
			continue
		}
		r.Name = clip(r.Name, maxRelayReaderNameBytes)
		out = append(out, r)
	}
	l.Readers = out
	return l, nil
}

// RelaySink hears an additional till's one-way relay frame on the main till.
type RelaySink interface {
	RelayCancel(tillID string, c ReaderCancel)
}

// SetRelaySink installs the receiver of reader.relay.cancel frames.
func (h *Hub) SetRelaySink(s RelaySink) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.relaySink = s
}

// TillPeer is tillID's live till link (not a reader's), or nil.
func (h *Hub) TillPeer(tillID string) *Peer {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.peers[tillID]
}

// HandleRelay registers the main till's handlers for an additional till's
// reader.list and reader.relay requests (the till vocabulary: a reader bearer's
// link has its own handler table and never reaches them).
func (h *Hub) HandleRelay(list, relay RequestHandler) {
	h.Handle(TypeReaderList, list)
	h.Handle(TypeReaderRelay, relay)
}

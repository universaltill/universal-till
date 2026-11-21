package fleetlink

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
)

// ut-docs#6538 R1b (ADR-0198 §6.3/§6.5): the frames of the card reader link,
// a phone that only collects a card tap for the main till. Same envelope as
// every other frame ({"v":1,"id","type","reply_to","payload"}); the shapes
// below are frozen by internal/fleetlink/testdata/reader_*.json, which the
// Go and the Kotlin tests both read.
const (
	// TypeReaderCollect: main -> phone, a request. The phone answers with a
	// reply of the same type (ReaderCollectAck) at once, or an error frame
	// ("busy": nothing was started). The verdict comes later, unsolicited,
	// as a TypeReaderResult.
	TypeReaderCollect = "reader.collect"
	// TypeReaderCancel: main -> phone, one-way: stop collecting this key.
	TypeReaderCancel = "reader.cancel"
	// TypeReaderToken: phone -> main, a request answered with a reply of the
	// same type (ReaderTokenReply) or an error frame.
	TypeReaderToken = "reader.token"
	// TypeReaderResult: phone -> main, one-way, ADVISORY: the main confirms
	// every fact with the payment broker before it records anything.
	TypeReaderResult = "reader.result"
	// TypeReaderStatus: phone -> main, one-way, the phone's readiness.
	TypeReaderStatus = "reader.status"

	// ReaderProtocol is the reader hello's protocol level.
	ReaderProtocol = 1
)

// Reader states (ReaderStatus.State).
const (
	ReaderReady       = "ready"
	ReaderNeedsSetup  = "needs_setup"
	ReaderBusy        = "busy"
	ReaderUnavailable = "unavailable"
)

// Reader result statuses (ReaderResult.Status).
const (
	ResultApproved  = "approved"
	ResultDeclined  = "declined"
	ResultCancelled = "cancelled"
	ResultPending   = "pending"
)

// Error codes a reader link answers with, besides the generic ones.
const (
	CodeReaderBusy        = "busy"         // the phone already collects (nothing was started)
	CodeReaderRateLimited = "rate_limited" // too many connection tokens asked for
	CodeReaderNotReady    = "not_ready"    // the reader is not in a state that may get a token
)

// ReaderHello is the main till's hello to a reader: no cursors, no fleet
// target, no update policy, nothing about the shop (ADR-0198 §6.1).
type ReaderHello struct {
	Role           string `json:"role"` // "main" to the phone; "card_reader" from it
	ReaderProtocol int    `json:"reader_protocol"`
	ReaderID       string `json:"reader_id,omitempty"` // main -> phone: the id this link is authenticated as
	NextPin        string `json:"next_pin,omitempty"`  // main -> phone: pin of the key served next, during a rotation only (ut-docs#6796)
	AppVersion     string `json:"app_version,omitempty"`
	Platform       string `json:"platform,omitempty"`
}

// ReaderStatus is a phone's readiness.
type ReaderStatus struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"` // a closed slug; anything else is dropped
}

// ReaderCollect asks the phone to take one payment. Everything in it comes
// from the main's own records (the attempt row, the broker's answers).
type ReaderCollect struct {
	ReaderID       string `json:"reader_id"`
	Provider       string `json:"provider"`
	AmountMinor    int64  `json:"amount_minor"`
	Currency       string `json:"currency"`
	IdempotencyKey string `json:"idempotency_key"`
	TimeoutMs      int64  `json:"timeout_ms"`
	ClientSecret   string `json:"client_secret"`
	LocationID     string `json:"location_id,omitempty"`
}

// ReaderCollectAck is the phone's reply to a collect it accepted.
type ReaderCollectAck struct {
	Accepted bool `json:"accepted"`
}

// ReaderCancel stops one collect.
type ReaderCancel struct {
	IdempotencyKey string `json:"idempotency_key"`
}

// ReaderTokenReply is the reply to a reader.token request: a connection
// token scoped to the shop's Location.
type ReaderTokenReply struct {
	Secret     string `json:"secret"`
	LocationID string `json:"location_id,omitempty"`
}

// ReaderResult is the phone's advisory verdict. It carries no card data and
// no brand or last four: those come from the broker only (ADR-0127).
type ReaderResult struct {
	IdempotencyKey string `json:"idempotency_key"`
	Status         string `json:"status"`
	DeclineCode    string `json:"decline_code,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

var (
	keyRE  = regexp.MustCompile(`^[A-Za-z0-9_.:\-]{1,128}$`)
	slugRE = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
	// readerReasons is the closed set of status reasons; the text shown to a
	// person is keyed on them, a phone-supplied string is never shown.
	readerReasons = map[string]bool{"reader_busy": true, "reader_unreachable": true, "reader_needs_setup": true}
)

func strictJSON(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("trailing data")
	}
	return nil
}

// DecodeReaderResult reads a reader.result payload strictly: one object, no
// unknown field (so no card data, brand or last four can ride along), a known
// status, a bounded key and closed-shape codes.
func DecodeReaderResult(raw json.RawMessage) (ReaderResult, error) {
	var r ReaderResult
	if err := strictJSON(raw, &r); err != nil {
		return ReaderResult{}, err
	}
	if !keyRE.MatchString(r.IdempotencyKey) {
		return ReaderResult{}, errors.New("bad idempotency key")
	}
	switch r.Status {
	case ResultApproved, ResultDeclined, ResultCancelled, ResultPending:
	default:
		return ReaderResult{}, errors.New("unknown status")
	}
	if r.DeclineCode != "" && !slugRE.MatchString(r.DeclineCode) {
		return ReaderResult{}, errors.New("bad decline code")
	}
	if r.Reason != "" && !slugRE.MatchString(r.Reason) {
		return ReaderResult{}, errors.New("bad reason")
	}
	return r, nil
}

// DecodeReaderStatus reads a reader.status payload strictly. A reason outside
// the closed set is dropped, not refused.
func DecodeReaderStatus(raw json.RawMessage) (ReaderStatus, error) {
	var s ReaderStatus
	if err := strictJSON(raw, &s); err != nil {
		return ReaderStatus{}, err
	}
	switch s.State {
	case ReaderReady, ReaderNeedsSetup, ReaderBusy, ReaderUnavailable:
	default:
		return ReaderStatus{}, errors.New("unknown state")
	}
	if !readerReasons[s.Reason] {
		s.Reason = ""
	}
	return s, nil
}

// readerResultKey is the idempotency key a (possibly malformed) result frame
// names, "" when none can be read: used only to end that collect pending.
func readerResultKey(raw json.RawMessage) string {
	var k struct {
		Key string `json:"idempotency_key"`
	}
	if json.Unmarshal(raw, &k) != nil || !keyRE.MatchString(k.Key) {
		return ""
	}
	return k.Key
}

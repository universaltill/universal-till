package paybroker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
)

// Caller is who is asking the broker. ADR-0198 §3.2 gives each class its own
// op set: a stolen phone or a hostile plugin must not reach an op meant for
// core alone.
type Caller int

const (
	// CallerNative: the phone/tablet shell (mobile.TapToPayCloud). It runs
	// the Terminal SDK, so it gets the connection token and creates the
	// PaymentIntent, but never refunds.
	CallerNative Caller = iota + 1
	// CallerHostFn: a payment plugin through the payment_provider_call host
	// fn. Only inside a payment dispatch core bound (Binding).
	CallerHostFn
	// CallerCore: core's own confirmation after a tap (session_status).
	CallerCore
)

// matrix is ADR-0198 §3.2. onboarding_start is on nobody's row: its slot is
// reserved for the manager route (own card).
var matrix = map[string]map[Caller]bool{
	"connection_token":  {CallerNative: true, CallerCore: true},
	"onboarding_status": {CallerNative: true, CallerHostFn: true, CallerCore: true},
	"session_create":    {CallerNative: true, CallerCore: true},
	"session_status":    {CallerNative: true, CallerHostFn: true, CallerCore: true},
	"session_cancel":    {CallerNative: true, CallerHostFn: true},
	"refund":            {CallerHostFn: true},
}

var (
	// ErrNoDispatch: a plugin called the broker outside a payment dispatch
	// core bound for it (no Binding, or one of the wrong kind or provider).
	ErrNoDispatch = errors.New("paybroker: not inside a bound payment dispatch")
	// ErrNoAttempt: a session_create that does not equal core's in-flight
	// attempt (key, amount, currency), or there is none.
	ErrNoAttempt = errors.New("paybroker: no matching in-flight attempt")
	// ErrForeignPI: a plugin named a PaymentIntent or key other than the one
	// core bound to this dispatch.
	ErrForeignPI = errors.New("paybroker: payment is not the one bound to this dispatch")
	// ErrBadAmount: a refund amount outside 1..the tender's refundable max.
	ErrBadAmount = errors.New("paybroker: refund amount out of range")
	// ErrBadBody: a plugin body that is not a JSON object.
	ErrBadBody = errors.New("paybroker: unreadable request body")
)

// BindingKind says which dispatch a Binding was made for.
type BindingKind int

const (
	BindAuthorize BindingKind = iota + 1 // payment.<key>.authorize
	BindRefund                           // payment.<key>.refund
)

// Binding is made by core's payment gate for exactly one authorize or refund
// dispatch and travels on that dispatch's context. Every identifier in the
// outgoing body comes from it; the plugin contributes only a refund amount,
// checked against MaxRefundMinor.
type Binding struct {
	Kind           BindingKind
	AmountMinor    int64 // authorize: the amount core's gate is charging (tap_to_pay_request ignores the plugin's)
	Provider       string
	AttemptKey     string // authorize: the attempt's idempotency key
	PI             string // the PaymentIntent (known after create; required for cancel and refund)
	MaxRefundMinor int64  // refund: the most this tender can still give back
	Currency       string
	RefundKey      string // refund: refund:<sale>:<tender>:<n>
}

type bindingKey struct{}

// WithBinding returns ctx carrying b for one payment dispatch.
func WithBinding(ctx context.Context, b Binding) context.Context {
	return context.WithValue(ctx, bindingKey{}, b)
}

// BindingFrom returns the binding WithBinding stamped.
func BindingFrom(ctx context.Context) (Binding, bool) {
	b, ok := ctx.Value(bindingKey{}).(Binding)
	return b, ok && b.Kind != 0
}

// AttemptView is what the broker needs of core's in-flight attempt row.
type AttemptView struct {
	Key         string
	Provider    string
	AmountMinor int64
	Currency    string
	Pending     bool // the attempt is still open: not settled
}

var (
	attemptMu     sync.RWMutex
	attemptLookup func(context.Context, string) (AttemptView, bool)
)

// SetAttemptLookup installs how an attempt is read by key (internal/pages
// wires it at boot from payment_attempts); nil removes it, after which no
// native session_create is accepted.
func SetAttemptLookup(fn func(context.Context, string) (AttemptView, bool)) {
	attemptMu.Lock()
	defer attemptMu.Unlock()
	attemptLookup = fn
}

// checkCreate refuses a native session_create unless it equals an open,
// in-flight attempt core wrote before the tap: a modified shell cannot create
// a PaymentIntent for another amount or key.
func checkCreate(ctx context.Context, provider string, body []byte) error {
	var req struct {
		IdempotencyKey string `json:"idempotency_key"`
		AttemptKey     string `json:"attempt_key"`
		AmountMinor    int64  `json:"amount_minor"`
		Currency       string `json:"currency"`
	}
	if json.Unmarshal(body, &req) != nil {
		return ErrNoAttempt
	}
	key := req.IdempotencyKey
	if key != "" && req.AttemptKey != "" && key != req.AttemptKey {
		return ErrNoAttempt
	}
	if key == "" {
		key = req.AttemptKey
	}
	attemptMu.RLock()
	lookup := attemptLookup
	attemptMu.RUnlock()
	if key == "" || lookup == nil {
		return ErrNoAttempt
	}
	a, ok := lookup(ctx, key)
	if !ok || !a.Pending || a.Provider != provider || a.AmountMinor != req.AmountMinor ||
		req.AmountMinor <= 0 || !strings.EqualFold(a.Currency, req.Currency) {
		return ErrNoAttempt
	}
	return nil
}

// hostBody builds the outgoing body of a plugin's call from the binding,
// reading only the refund amount from the plugin's JSON.
func hostBody(ctx context.Context, provider, op string, plugin []byte) ([]byte, error) {
	if op == "onboarding_status" {
		return []byte(`{}`), nil // read-only readiness; nothing of the plugin's is forwarded
	}
	b, ok := BindingFrom(ctx)
	if !ok || b.Provider != provider {
		return nil, ErrNoDispatch
	}
	var req map[string]json.RawMessage
	if len(bytes.TrimSpace(plugin)) > 0 {
		if err := json.Unmarshal(plugin, &req); err != nil || req == nil {
			return nil, ErrBadBody
		}
	}
	str := func(k string) string {
		var s string
		_ = json.Unmarshal(req[k], &s)
		return s
	}
	// Naming another payment's identifiers is refused, not ignored: a plugin
	// doing that is broken or hostile, and either way nothing is sent.
	for _, k := range []string{"payment_intent_id", "id"} {
		if v := str(k); v != "" && v != b.PI {
			return nil, ErrForeignPI
		}
	}
	for _, k := range []string{"idempotency_key", "attempt_key"} {
		if v := str(k); v != "" && (b.Kind != BindAuthorize || v != b.AttemptKey) {
			return nil, ErrForeignPI
		}
	}
	out := map[string]any{}
	switch op {
	case "session_status":
		if b.AttemptKey != "" && b.Kind == BindAuthorize {
			out["idempotency_key"] = b.AttemptKey
		}
		if b.PI != "" {
			out["payment_intent_id"] = b.PI
		}
		if len(out) == 0 {
			return nil, ErrNoDispatch
		}
	case "session_cancel":
		if b.Kind != BindAuthorize || b.PI == "" {
			return nil, ErrNoDispatch
		}
		out["payment_intent_id"] = b.PI
	case "refund":
		if b.Kind != BindRefund || b.PI == "" || b.RefundKey == "" {
			return nil, ErrNoDispatch
		}
		var amount int64
		if raw, has := req["amount_minor"]; !has || json.Unmarshal(raw, &amount) != nil || amount < 1 || amount > b.MaxRefundMinor {
			return nil, ErrBadAmount
		}
		out["payment_intent_id"] = b.PI
		out["refund_key"] = b.RefundKey
		out["amount_minor"] = amount
	default:
		return nil, ErrNotAllowed
	}
	return json.Marshal(out)
}

// neutralFields are the answer fields of ADR-0198 §3.5 that pass through
// unchanged. client_secret and the connection-token secret are not among
// them, and nothing outside this list (and the two renames below) passes.
var neutralFields = map[string]bool{
	"id": true, "status": true, "amount_minor": true, "amount_received_minor": true,
	"currency": true, "idempotency_key": true, "card_brand": true, "last4": true,
	"refunded_minor": true, "last_error_code": true, "refund_id": true,
	"onboarded": true, "charges_enabled": true, "ready": true, "retry_after_s": true,
}

// Neutral maps the cloud's wire answer to the neutral envelope of ADR-0198
// §3.5 (contract stripe-tap-to-pay-v1 §3): payment_intent_id becomes id, the
// nested card{brand,last4} becomes card_brand and last4. It drops every
// secret and every unlisted field; an unreadable answer becomes {}. Already
// neutral input maps to itself.
func Neutral(resp []byte) []byte {
	var in map[string]json.RawMessage
	if json.Unmarshal(resp, &in) != nil {
		return []byte(`{}`)
	}
	out := map[string]json.RawMessage{}
	for k, v := range in {
		if neutralFields[k] {
			out[k] = v
		}
	}
	if _, has := out["id"]; !has {
		if v, ok := in["payment_intent_id"]; ok {
			out["id"] = v
		}
	}
	var card struct {
		Brand string `json:"brand"`
		Last4 string `json:"last4"`
	}
	if raw, ok := in["card"]; ok && json.Unmarshal(raw, &card) == nil {
		if b, err := json.Marshal(card.Brand); err == nil && card.Brand != "" {
			out["card_brand"] = b
		}
		if b, err := json.Marshal(card.Last4); err == nil && card.Last4 != "" {
			out["last4"] = b
		}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return []byte(`{}`)
	}
	return b
}

// FilterForWASM is the answer a payment plugin gets: the neutral envelope
// only, never a secret.
func FilterForWASM(resp []byte) []byte { return Neutral(resp) }

// ErrorCode is the stable text a plugin or the shell is given for a broker
// failure: the cloud's own error code, else http_<status>, else one of the
// local codes.
func ErrorCode(err error) string {
	var he *HTTPError
	switch {
	case errors.As(err, &he):
		if he.Code != "" {
			return he.Code
		}
		return "http_" + strconv.Itoa(he.Status)
	case errors.Is(err, ErrNotAllowed):
		return "op_not_allowed"
	case errors.Is(err, ErrNotEnrolled):
		return "not_enrolled"
	case errors.Is(err, ErrOffline):
		return "offline"
	case errors.Is(err, ErrResponseTooLarge):
		return "response_too_large"
	case errors.Is(err, ErrRequestTooLarge):
		return "request_too_large"
	case errors.Is(err, ErrInsecureEndpoint):
		return "insecure_endpoint"
	case errors.Is(err, ErrNoAttempt):
		return "no_attempt"
	case errors.Is(err, ErrNoDispatch):
		return "no_dispatch"
	case errors.Is(err, ErrForeignPI):
		return "foreign_payment"
	case errors.Is(err, ErrBadAmount):
		return "invalid_amount"
	default:
		return "failed"
	}
}

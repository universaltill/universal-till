// Package devicepay is core's neutral seam to a payment the till's own
// device takes — Tap to Pay on iPhone first (ut-docs#3049, ADR-0125 §3,
// readiness doc ut-docs#6461). The provider SDK lives in the native shell
// (Swift, because it has to); payment policy lives in the payment plugin
// (ADR-0016). This package only carries a request across and a verdict
// back, and refuses to read anything it does not recognise as a verdict.
//
// Shape follows internal/kiosk: a Go→native callback (Bridge) registered by
// the native shell while it is in the foreground (mobile.SetTapToPayBridge)
// and nil otherwise. The bridge is stored here rather than in package
// mobile because the plugin host fn (ut-docs#6455) cannot import mobile —
// mobile imports internal/app. mobile.TapToPayBridge is the gobind-visible
// mirror of Bridge, kept structurally identical so SetTapToPayBridge passes
// its argument straight through.
//
// Vocabulary (ADR-0125 §3f): approved | declined | cancelled | pending.
// Anything the till cannot be sure of is pending, never declined (#6439):
// a "declined" that was really an approval charges the customer twice.
//
// Desktop, service and Pi builds never register a bridge: Capabilities is
// empty there and Collect answers declined/not_foreground.
package devicepay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"sync"

	"github.com/universaltill/universal-till/internal/logging"
)

// Bridge is the native side. Every method is called on a Go goroutine,
// never under this package's lock; the implementation hops to its own UI
// thread where it needs to.
type Bridge interface {
	// Capabilities returns the native side's cached readiness report as
	// JSON ({"tap_to_pay":{"<provider>":ProviderCap}}). Never blocks.
	Capabilities() string
	// Collect takes one payment (Request as JSON) and blocks until it has a
	// verdict (Result as JSON). The native side honours the request's
	// deadline itself; Go also cancels when its context ends.
	Collect(requestJSON string) string
	// Cancel stops the in-flight collect with this idempotency key.
	// Idempotent; a no-op when nothing matches.
	Cancel(idempotencyKey string)
	// Setup is the manager's one-off preparation (location permission,
	// connect with Apple's terms allowed, merchant education). JSON
	// SetupResult. Reached only from a manager-gated settings route.
	Setup(provider string) string
	// ShowEducation presents Apple's "How to Tap" guide. JSON SetupResult.
	ShowEducation(provider string) string
}

// Outcome statuses (ADR-0125 §3f).
const (
	StatusApproved  = "approved"
	StatusDeclined  = "declined"
	StatusCancelled = "cancelled"
	StatusPending   = "pending"
)

// Reasons this package itself produces.
const (
	ReasonNotForeground   = "not_foreground"    // no native shell in the foreground (ADR-0125 §3e)
	ReasonBadRequest      = "bad_request"       // the request failed validation; nothing reached the native side
	ReasonTimeout         = "timeout"           // the caller's context ended first
	ReasonContextCanceled = "context_cancelled" // the caller cancelled its context
	ReasonBadNativeResult = "bad_native_result" // the native reply was not a verdict this package recognises
	ReasonForbidden       = "forbidden"         // the caller is not a manager (Resolve)
)

// ProviderStripe is the one provider with a device integration today
// (Stripe first, owner 2026-11-16).
const ProviderStripe = "stripe" // core-neutral:allow native Tap to Pay SDK is provider-specific; this is the security allow-list of providers the bridge accepts (ut-docs#3049, ADR #6453)

var supportedProviders = map[string]bool{ProviderStripe: true}

// Request is one payment for the native side. Amounts are minor units;
// money.Money conversion is the caller's (the plugin host fn's) job.
type Request struct {
	Provider       string `json:"provider"`
	AmountMinor    int64  `json:"amount_minor"`
	Currency       string `json:"currency"`
	IdempotencyKey string `json:"idempotency_key"`
	Description    string `json:"description,omitempty"`
	// DeadlineUnixMs is filled from the context's deadline by Collect.
	DeadlineUnixMs int64 `json:"deadline_unix_ms"`
}

// Result is the native verdict. It carries no card data beyond brand and
// last four: the strict decode in Collect rejects any other field.
type Result struct {
	Status        string `json:"status"`
	ProviderTxnID string `json:"provider_txn_id,omitempty"`
	ChargeID      string `json:"charge_id,omitempty"`
	CardBrand     string `json:"card_brand,omitempty"`
	Last4         string `json:"last4,omitempty"`
	DeclineCode   string `json:"decline_code,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

// ProviderCap is one provider's readiness on this device.
type ProviderCap struct {
	Ready    bool   `json:"ready"`
	State    string `json:"state"` // ready | preparing | needs_setup | unavailable
	Reason   string `json:"reason,omitempty"`
	Progress int    `json:"progress"`
}

// SetupResult is Setup's and ShowEducation's answer.
type SetupResult struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}

var (
	mu     sync.RWMutex
	bridge Bridge
)

// SetBridge registers the native side; nil un-registers it.
//
// Its one production caller is mobile.SetTapToPayBridge, in the gomobile
// library package, which the whole-program deadcode guard does not use as a
// root (same as internal/kiosk.SetBridge).
func SetBridge(b Bridge) {
	mu.Lock()
	defer mu.Unlock()
	bridge = b
}

// RegisteredBridge reports the bridge SetBridge last registered, or nil.
// Callers invoke it after this returns, never under mu: Collect blocks for
// the whole payment, and the native side re-registers on scene changes.
func RegisteredBridge() Bridge {
	mu.RLock()
	defer mu.RUnlock()
	return bridge
}

var currencyRE = regexp.MustCompile(`^[A-Za-z]{3}$`)

func valid(req Request) bool {
	return supportedProviders[req.Provider] &&
		req.AmountMinor > 0 &&
		currencyRE.MatchString(req.Currency) &&
		req.IdempotencyKey != ""
}

// Collect asks the native side to take req and waits for its verdict or
// for ctx to end, whichever is first. ctx must carry a deadline: a payment
// with no bound could hold the till's pay screen forever.
//
// When ctx ends first the native collect is cancelled and the outcome is
// pending/timeout (ADR-0125 §3d): the card may have been read already, so
// the plugin settles it through the provider (session_status), never by
// assuming a decline.
func Collect(ctx context.Context, req Request) Result {
	b := RegisteredBridge()
	if b == nil {
		return Result{Status: StatusDeclined, Reason: ReasonNotForeground}
	}
	dl, hasDeadline := ctx.Deadline()
	if !hasDeadline || !valid(req) {
		return Result{Status: StatusDeclined, Reason: ReasonBadRequest}
	}
	if err := ctx.Err(); err != nil {
		// Already over: starting the native collect now could let Cancel
		// overtake it and leave the tap sheet up for a payment nobody waits
		// for. Pending, not declined: an earlier attempt with this key may
		// have gone through.
		return Result{Status: StatusPending, Reason: endReason(err)}
	}
	req.DeadlineUnixMs = dl.UnixMilli()
	payload, err := json.Marshal(req)
	if err != nil {
		return Result{Status: StatusDeclined, Reason: ReasonBadRequest}
	}

	replies := make(chan string, 1) // buffered: a late reply never blocks the goroutine
	go func() {
		defer logging.RecoverAndLog("devicepay collect")
		replies <- b.Collect(string(payload))
	}()
	select {
	case raw := <-replies:
		return parseResult(raw)
	case <-ctx.Done():
		b.Cancel(req.IdempotencyKey)
		reason := endReason(ctx.Err())
		logging.L().Warnf("devicepay: %s collect ended by its context (%s); outcome pending", req.Provider, reason)
		return Result{Status: StatusPending, Reason: reason}
	}
}

func endReason(err error) string {
	if errors.Is(err, context.Canceled) {
		return ReasonContextCanceled
	}
	return ReasonTimeout
}

// parseResult accepts only a well-formed verdict: one JSON object, no
// fields beyond Result's, one of the four statuses, and an approval that
// names its provider transaction. Anything else is pending (#6439).
func parseResult(raw string) Result {
	var r Result
	if err := strictDecode(raw, &r); err != nil {
		logging.L().Warnf("devicepay: unreadable native result (%v); outcome pending", err)
		return Result{Status: StatusPending, Reason: ReasonBadNativeResult}
	}
	switch r.Status {
	case StatusDeclined, StatusCancelled, StatusPending:
		return r
	case StatusApproved:
		if r.ProviderTxnID != "" {
			return r
		}
	}
	logging.L().Warnf("devicepay: native result with status %q is not a verdict; outcome pending", r.Status)
	return Result{Status: StatusPending, Reason: ReasonBadNativeResult}
}

func strictDecode(raw string, v any) error {
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("trailing data after the result")
	}
	return nil
}

// capReport is the native capability JSON's envelope.
type capReport struct {
	TapToPay map[string]ProviderCap `json:"tap_to_pay"`
}

// Capabilities is the native side's readiness per provider; empty with no
// bridge (every non-iOS till, or the flag-off iOS build) or an unreadable
// report.
func Capabilities() map[string]ProviderCap {
	b := RegisteredBridge()
	if b == nil {
		return map[string]ProviderCap{}
	}
	var rep capReport
	if err := json.Unmarshal([]byte(b.Capabilities()), &rep); err != nil || rep.TapToPay == nil {
		return map[string]ProviderCap{}
	}
	return rep.TapToPay
}

// Setup runs the native one-off preparation for provider. Manager-gated by
// its caller (ut-docs#6458); Apple's terms are accepted only here.
func Setup(ctx context.Context, provider string) SetupResult {
	return setupCall(ctx, provider, func(b Bridge) string { return b.Setup(provider) })
}

// ShowEducation presents Apple's "How to Tap" guide for provider.
func ShowEducation(ctx context.Context, provider string) SetupResult {
	return setupCall(ctx, provider, func(b Bridge) string { return b.ShowEducation(provider) })
}

func setupCall(ctx context.Context, provider string, call func(Bridge) string) SetupResult {
	b := RegisteredBridge()
	if b == nil {
		return SetupResult{Reason: ReasonNotForeground}
	}
	if !supportedProviders[provider] {
		return SetupResult{Reason: ReasonBadRequest}
	}
	replies := make(chan string, 1)
	go func() {
		defer logging.RecoverAndLog("devicepay setup")
		replies <- call(b)
	}()
	select {
	case raw := <-replies:
		var r SetupResult
		if err := strictDecode(raw, &r); err != nil {
			return SetupResult{Reason: ReasonBadNativeResult}
		}
		return r
	case <-ctx.Done():
		return SetupResult{Reason: ReasonTimeout}
	}
}

// Outcomes a manager can give an unresolved payment (ut-docs#6533).
const (
	OutcomeNotCharged = "not_charged" // the provider shows no charge: the next sale may start
	OutcomeCharged    = "charged"     // the provider shows the customer was charged
)

// Resolver is the native side of "the operator settled an earlier payment
// whose outcome was unknown" (ut-docs#6533): it drops (not_charged) or keeps
// as paid (charged) the native side's unresolved-payment record for one
// idempotency key, which unblocks the next sale. It is registered separately
// from Bridge because Bridge is a gobind protocol the iOS shell already
// implements; Android registers both while in the foreground
// (mobile.SetTapToPayResolver).
type Resolver interface {
	// Resolve answers a SetupResult as JSON. outcome is OutcomeNotCharged or
	// OutcomeCharged.
	Resolve(idempotencyKey, outcome string) string
}

// Actor is who is asking; auth.User satisfies it. Declared here so this
// neutral package does not import auth (and, through it, the data layer).
type Actor interface {
	IsManager() bool
}

var resolver Resolver // guarded by mu

// SetResolver registers the native resolver; nil un-registers it. Its one
// production caller is mobile.SetTapToPayResolver.
func SetResolver(r Resolver) {
	mu.Lock()
	defer mu.Unlock()
	resolver = r
}

// Resolve tells the native side how the earlier payment for idempotencyKey
// turned out, after which another sale may start. A wrong answer can
// double-charge a customer, so only a manager is let through and a cashier
// (or no actor) is refused here, before the native side is reached, whatever
// the web layer did. The caller checks the provider's own answer first.
func Resolve(ctx context.Context, who Actor, provider, idempotencyKey, outcome string) SetupResult {
	if who == nil || !who.IsManager() {
		return SetupResult{Reason: ReasonForbidden}
	}
	mu.RLock()
	r := resolver
	mu.RUnlock()
	if r == nil {
		return SetupResult{Reason: ReasonNotForeground}
	}
	if !supportedProviders[provider] || idempotencyKey == "" || (outcome != OutcomeNotCharged && outcome != OutcomeCharged) {
		return SetupResult{Reason: ReasonBadRequest}
	}
	replies := make(chan string, 1)
	go func() {
		defer logging.RecoverAndLog("devicepay resolve")
		replies <- r.Resolve(idempotencyKey, outcome)
	}()
	select {
	case raw := <-replies:
		var res SetupResult
		if err := strictDecode(raw, &res); err != nil {
			return SetupResult{Reason: ReasonBadNativeResult}
		}
		return res
	case <-ctx.Done():
		return SetupResult{Reason: ReasonTimeout}
	}
}

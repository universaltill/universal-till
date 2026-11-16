// Package devicepay is the till's authenticated door to the cloud's Stripe
// Terminal endpoints (#3048, ADR-0125). The Android shell (and later iOS) asks
// for one of a fixed set of operations by NAME; this package builds the URL
// from the till's own enrolment (no plan gate: owner 2026-11-16, any shop that
// finished Stripe Connect onboarding qualifies; the cloud enforces that), attaches the till's ADR-0116 device bearer,
// and returns the cloud's JSON. The bearer never leaves Go: not into Kotlin,
// not into the page, not into an error message.
//
// The design's fifth endpoint (a PaymentIntent lookup the stripe_tap plugin
// uses to verify a tap server-side) is NOT here: no Go caller exists yet, and
// the contract is recorded on #3048 instead (GET .../terminal/payment-intent
// ?key=<ut_key>, direct-charge account scoped). The Go devicepay chain card
// adds it as one more entry in this package, reusing do().
package devicepay

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Error codes. Kotlin maps the Code prefix of the gomobile error message to
// the bridge's reason tokens.
const (
	CodeBadRequest        = "bad_request"
	CodeConnectIncomplete = "connect_incomplete"
	CodeOffline           = "offline"
	CodeCloudError        = "cloud_error"
)

// Error is a coded failure. Its text is "<code>" or "<code>: <detail>" and
// never contains a credential, a URL or a response body.
type Error struct {
	Code   string
	Detail string
}

func (e *Error) Error() string {
	if e.Detail == "" {
		return e.Code
	}
	return e.Code + ": " + e.Detail
}

const (
	maxRequest  = 4 << 10  // request body cap
	maxResponse = 16 << 10 // response body cap
	defaultTime = 10 * time.Second
	apiPrefix   = "/v1/payments/stripe/" // core-neutral:allow ut-cloud endpoint path (#3055); moves with the stripe_tap devicepay chain card
)

type route struct{ method, path string }

// routes is the allowlist: the only operations a shell can reach. The URL is
// never built from the caller's arguments.
var routes = map[string]route{
	"onboarding_link":  {http.MethodPost, "connect/onboarding-link"},
	"connect_status":   {http.MethodGet, "connect/status"},
	"location":         {http.MethodPost, "terminal/location"},
	"connection_token": {http.MethodPost, "terminal/connection-token"},
}

// Source is what the running till provides.
type Source interface {
	// Target returns the cloud endpoint (https, no trailing path of ours) and
	// the till's device bearer; ok=false when the till is not enrolled.
	Target(ctx context.Context) (baseURL, bearer string, ok bool)
}

// Caller makes the calls. The zero Client and Timeout mean verified-TLS
// defaults (10 s).
type Caller struct {
	Source  Source
	Client  *http.Client
	Timeout time.Duration
}

func (c *Caller) client() *http.Client {
	var cl http.Client
	if c.Client != nil {
		cl = *c.Client
	}
	// Never follow a redirect: it could carry the bearer elsewhere.
	cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &cl
}

func (c *Caller) prepare(ctx context.Context) (base, bearer string, err error) {
	if c.Source == nil {
		return "", "", &Error{Code: CodeOffline, Detail: "till not linked"}
	}
	base, bearer, ok := c.Source.Target(ctx)
	if !ok || strings.TrimSpace(bearer) == "" || strings.TrimSpace(base) == "" {
		return "", "", &Error{Code: CodeOffline, Detail: "till not linked"}
	}
	u, perr := url.Parse(base)
	if perr != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return "", "", &Error{Code: CodeCloudError, Detail: "cloud endpoint must be https"}
	}
	return strings.TrimRight(base, "/"), bearer, nil
}

// Call runs one allowlisted operation. bodyJSON is sent as-is for POSTs
// (capped) and ignored for GETs.
func (c *Caller) Call(ctx context.Context, op, bodyJSON string) (string, error) {
	r, ok := routes[op]
	if !ok {
		return "", &Error{Code: CodeBadRequest, Detail: "unknown operation"}
	}
	if len(bodyJSON) > maxRequest {
		return "", &Error{Code: CodeBadRequest, Detail: "request too large"}
	}
	base, bearer, err := c.prepare(ctx)
	if err != nil {
		return "", err
	}
	var body io.Reader
	if r.method == http.MethodPost {
		body = strings.NewReader(bodyJSON)
	}
	return c.do(ctx, r.method, base+apiPrefix+r.path, bearer, body)
}

func (c *Caller) do(ctx context.Context, method, target, bearer string, body io.Reader) (string, error) {
	t := c.Timeout
	if t <= 0 {
		t = defaultTime
	}
	ctx, cancel := context.WithTimeout(ctx, t)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return "", &Error{Code: CodeCloudError}
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client().Do(req)
	if err != nil {
		// err text can embed the URL; report only the class.
		return "", &Error{Code: CodeOffline, Detail: "cloud not reached"}
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusConflict:
		return "", &Error{Code: CodeConnectIncomplete}
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return "", &Error{Code: CodeCloudError, Detail: "HTTP " + http.StatusText(resp.StatusCode)}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return "", &Error{Code: CodeOffline, Detail: "cloud not reached"}
	}
	if len(b) > maxResponse {
		return "", &Error{Code: CodeCloudError, Detail: "response too large"}
	}
	return string(bytes.TrimSpace(b)), nil
}

var (
	defMu     sync.RWMutex
	defCaller = &Caller{}
)

// SetSource registers the running till as the Source (internal/app does this
// at boot) and returns a func that restores the previous one.
func SetSource(s Source) (restore func()) {
	defMu.Lock()
	prev := defCaller
	defCaller = &Caller{Source: s}
	defMu.Unlock()
	return func() {
		defMu.Lock()
		defCaller = prev
		defMu.Unlock()
	}
}

// Call runs op through the registered Source.
func Call(ctx context.Context, op, bodyJSON string) (string, error) {
	defMu.RLock()
	c := defCaller
	defMu.RUnlock()
	return c.Call(ctx, op, bodyJSON)
}

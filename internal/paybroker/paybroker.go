// Package paybroker is the till's neutral forwarder to ut-cloud's payment
// broker (ut-docs#3049, readiness doc ut-docs#6461 §3d, cloud side
// ut-docs#6454): POST <enrolment endpoint>/v1/payments/{provider}/{op}
// with this till's ADR-0116 device credential as the Bearer.
//
// Three callers share it, each with its own op set (Caller, ADR-0198 §3.2):
// the native shell (mobile.TapToPayCloud, in-process — never an HTTP route,
// so the 0.0.0.0-bound server exposes nothing new), the WASM host fn
// payment_provider_call (ut-docs#6455, only inside a bound payment dispatch)
// and core's own post-tap confirmation. Core never
// parses provider JSON beyond the cloud's error code, and never logs a
// body: bodies carry the Terminal connection-token secret and the
// PaymentIntent client secret. The cloud derives the store from the token,
// never from the body.
package paybroker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/netaccess"
)

// Timeout bounds one broker call end to end.
const Timeout = 15 * time.Second

// MaxResponseBytes caps a broker response; anything larger is refused.
const MaxResponseBytes = 64 << 10

// MaxRequestBytes caps a request body; anything larger is refused with no
// request.
const MaxRequestBytes = 64 << 10

var (
	// ErrNotAllowed: the provider/op pair is not on the allow-list.
	ErrNotAllowed = errors.New("paybroker: provider/op not allowed")
	// ErrNotEnrolled: this till has no cloud enrolment (endpoint + device
	// credential) to call the broker with.
	ErrNotEnrolled = errors.New("paybroker: till not enrolled with the cloud")
	// ErrOffline: the cloud could not be reached.
	ErrOffline = errors.New("paybroker: cloud unreachable")
	// ErrResponseTooLarge: the response exceeded MaxResponseBytes.
	ErrResponseTooLarge = errors.New("paybroker: response too large")
	// ErrRequestTooLarge: the request body exceeded MaxRequestBytes.
	ErrRequestTooLarge = errors.New("paybroker: request too large")
	// ErrInsecureEndpoint: the enrolment's endpoint is not https. The
	// device credential and the provider secrets in broker bodies never
	// travel in the clear; only a loopback endpoint may be plain http.
	ErrInsecureEndpoint = errors.New("paybroker: cloud endpoint is not https")
)

// HTTPError is a non-2xx broker answer. Code is the cloud's
// {"error":{"code":…}} when it sent one (e.g. not_onboarded).
type HTTPError struct {
	Status int
	Code   string
}

func (e *HTTPError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("paybroker: cloud answered %d (%s)", e.Status, e.Code)
	}
	return fmt.Sprintf("paybroker: cloud answered %d", e.Status)
}

// Target is where the broker is and who this till is.
type Target struct {
	BaseURL string // the enrolment's cloud endpoint, e.g. https://cloud.universaltill.com/api
	Bearer  string // this till's ADR-0116 device credential
}

var (
	mu       sync.RWMutex
	resolver func(context.Context) (Target, bool)

	// Default TLS verification; a redirect is answered as an HTTPError,
	// never followed with the credential.
	client = func() *http.Client {
		c := netaccess.NewClient(Timeout)
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		return c
	}()
)

// SetTargetResolver installs how the broker target is read, per call (so a
// till that enrols or rotates its credential after boot needs no restart).
// internal/pages wires it at boot from the live enrolment; nil removes it.
func SetTargetResolver(fn func(context.Context) (Target, bool)) {
	mu.Lock()
	defer mu.Unlock()
	resolver = fn
}

// Allowed reports whether caller may use provider/op (ADR-0198 §3.2). Any
// other caller, provider or op is refused with no request.
func Allowed(caller Caller, provider, op string) bool {
	return provider == "stripe" && matrix[op][caller] // core-neutral:allow broker op allow-list is a security boundary keyed by provider; unknown provider/op is refused with no request (ut-docs#3049, ADR #6453)
}

// Call forwards body to the broker's provider/op and returns its 2xx
// response body unchanged, except to a plugin (CallerHostFn), which gets
// only the neutral fields and a body core built from its dispatch's
// Binding. A nil or empty body is sent as {}.
func Call(ctx context.Context, caller Caller, provider, op string, body []byte) ([]byte, error) {
	if !Allowed(caller, provider, op) {
		return nil, ErrNotAllowed
	}
	switch {
	case caller == CallerHostFn:
		built, err := hostBody(ctx, provider, op, body)
		if err != nil {
			return nil, err
		}
		body = built
	case caller == CallerNative && op == "session_create":
		if err := checkCreate(ctx, provider, body); err != nil {
			return nil, err
		}
	}
	mu.RLock()
	resolve := resolver
	mu.RUnlock()
	if resolve == nil {
		return nil, ErrNotEnrolled
	}
	t, ok := resolve(ctx)
	if !ok || t.BaseURL == "" || t.Bearer == "" {
		return nil, ErrNotEnrolled
	}
	// https only, or plain http to this machine's loopback (local tests,
	// an on-prem cloud on the same host): the request carries the device
	// Bearer and the reply provider secrets. Inline, not a helper: Call is
	// reached only through gomobile today, which deadcode does not root
	// (scripts/ci/deadcode-baseline.txt, ut-docs#3406).
	secure := false
	if u, err := url.Parse(t.BaseURL); err == nil && u.Host != "" {
		switch u.Scheme {
		case "https":
			secure = true
		case "http":
			host := u.Hostname()
			ip := net.ParseIP(host)
			secure = host == "localhost" || (ip != nil && ip.IsLoopback())
		}
	}
	if !secure {
		logging.L().Warnf("paybroker: %s/%s refused: the cloud endpoint is not https", provider, op)
		return nil, ErrInsecureEndpoint
	}
	if len(body) > MaxRequestBytes {
		return nil, ErrRequestTooLarge
	}
	if len(body) == 0 {
		body = []byte(`{}`)
	}

	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	endpoint := strings.TrimRight(t.BaseURL, "/") + "/v1/payments/" + provider + "/" + op
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("paybroker: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+t.Bearer)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		logging.L().Warnf("paybroker: %s/%s unreachable after %s", provider, op, time.Since(start).Round(time.Millisecond))
		return nil, fmt.Errorf("%w: %s/%s", ErrOffline, provider, op)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	took := time.Since(start).Round(time.Millisecond)
	if err != nil {
		logging.L().Warnf("paybroker: %s/%s status=%d read failed after %s", provider, op, resp.StatusCode, took)
		return nil, fmt.Errorf("%w: %s/%s", ErrOffline, provider, op)
	}
	if len(out) > MaxResponseBytes {
		logging.L().Warnf("paybroker: %s/%s status=%d response over %d bytes, refused", provider, op, resp.StatusCode, MaxResponseBytes)
		return nil, ErrResponseTooLarge
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		he := &HTTPError{Status: resp.StatusCode, Code: errorCode(out)}
		logging.L().Warnf("paybroker: %s/%s status=%d code=%q in %s", provider, op, resp.StatusCode, he.Code, took)
		return nil, he
	}
	logging.L().Infof("paybroker: %s/%s status=%d in %s", provider, op, resp.StatusCode, took)
	if caller == CallerHostFn {
		return FilterForWASM(out), nil
	}
	return out, nil
}

// errorCode reads the cloud's {"error":{"code":…}}, or "" — the only field
// of a broker body core ever reads. Only a short code-shaped value is kept,
// since it reaches the log.
func errorCode(body []byte) string {
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) != nil {
		return ""
	}
	c := e.Error.Code
	if len(c) > 64 {
		return ""
	}
	for _, r := range c {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return ""
		}
	}
	return c
}

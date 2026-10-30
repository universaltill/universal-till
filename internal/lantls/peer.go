package lantls

import (
	"bytes"
	"context"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/netaccess"
)

// Pinned peer traffic (ADR-0114 §7, ut-docs#4092): once a till holds its
// main till's pin (sync.primary_cert_pin), every request it sends to the
// main till goes over TLS on the same LAN port, verified by that pin only,
// and never falls back to plain HTTP. A till with no pin keeps plain HTTP
// (ADR-0114 §11). A pin mismatch runs the re-proof hook (SetPeerReproof,
// ut-docs#4410): a valid proof adopts the new pin and the request is retried
// once; otherwise it fails like an unreachable main till.
//
// Every replica → main till client shares one transport, built by
// NewPeerClient. Call sites keep building plain http:// URLs from
// sync.primary_url; the transport upgrades the ones aimed at the main till,
// so no call site can forget to.

// PeerTarget is the main till this till follows and the pins it trusts.
type PeerTarget struct {
	// PrimaryURL is sync.primary_url.
	PrimaryURL string
	// Pin is sync.primary_cert_pin; "" = not pinned.
	Pin string
	// NextPin is the main till's announced next pin
	// (sync.primary_cert_next_pin, ut-docs#4409): during a key rotation a
	// certificate with it is accepted as well, so the main till's switch
	// costs no re-proof. Ignored without Pin.
	NextPin string
}

// PeerSource reports the main till this till follows and the pins held for
// it. An error (the settings could not be read) fails the request closed:
// an unreadable pin is never taken to mean "not pinned".
type PeerSource func(ctx context.Context) (PeerTarget, error)

var peerSource atomic.Pointer[PeerSource]

// SetPeerSource installs the process-wide source the peer transport reads
// on every request; nil means no till is pinned. It returns a func that
// restores the previous source (for tests).
func SetPeerSource(src PeerSource) (restore func()) {
	var p *PeerSource
	if src != nil {
		p = &src
	}
	prev := peerSource.Swap(p)
	return func() { peerSource.Store(prev) }
}

func peerTarget(ctx context.Context) (PeerTarget, error) {
	src := peerSource.Load()
	if src == nil {
		return PeerTarget{}, nil
	}
	t, err := (*src)(ctx)
	if err != nil {
		return PeerTarget{}, err
	}
	t.PrimaryURL, t.Pin, t.NextPin = strings.TrimSpace(t.PrimaryURL), strings.TrimSpace(t.Pin), strings.TrimSpace(t.NextPin)
	if t.Pin == "" || t.NextPin == t.Pin {
		t.NextPin = ""
	}
	return t, nil
}

// PeerReproof proves, with no bearer, that the till at primaryURL now
// serving a certificate matching neither heldPin nor the held next pin is
// still this till's main till (ADR-0114 §7, ut-docs#4410), and on success
// stores the new pin where the PeerSource reads it. nil error with the held pin unchanged means nothing
// to retry.
type PeerReproof func(ctx context.Context, primaryURL, heldPin string) error

var peerReproof atomic.Pointer[PeerReproof]

// SetPeerReproof installs the process-wide re-proof hook; nil means a pin
// mismatch just fails. It returns a func that restores the previous hook.
func SetPeerReproof(fn PeerReproof) (restore func()) {
	var p *PeerReproof
	if fn != nil {
		p = &fn
	}
	prev := peerReproof.Swap(p)
	return func() { peerReproof.Store(prev) }
}

// Bearer rotation (ut-docs#5198). The main till marks an answer with
// BearerRotateHeader when the bearer the replica just used is due to be
// swapped. The replica's side of that lives here because every request to
// the main till passes this transport, so no call site can miss the header.
const (
	// BearerRotateHeader is set by the main till on a response to a request
	// whose bearer should rotate.
	BearerRotateHeader = "X-UT-Sync-Bearer-Rotate"
	// BearerRotatePath is the main till's rotation endpoint.
	BearerRotatePath = "/api/sync/bearer/rotate"

	// bearerRotateTimeout bounds one background rotation attempt.
	bearerRotateTimeout = 20 * time.Second
)

// bearerRotateRetryEvery is the least time between two rotation attempts,
// so a main till that keeps asking (or a rotation that keeps failing) costs
// one call a minute, not one per request. A var only for tests.
var bearerRotateRetryEvery = time.Minute

// BearerRotate swaps this till's bearer for a fresh one from the main till.
// It reports failure by logging and leaving the old bearer in place.
type BearerRotate func(ctx context.Context)

var bearerRotate atomic.Pointer[BearerRotate]

// bearerRotateBusy is the single-flight guard; bearerRotateLast the start of
// the latest attempt (unix nanoseconds).
var (
	bearerRotateBusy atomic.Bool
	// bearerRotateAgain: a forced rotation was requested while one was in
	// flight (the in-flight one may predate the bearer just adopted).
	bearerRotateAgain atomic.Bool
	bearerRotateLast  atomic.Int64
)

// SetBearerRotate installs the process-wide rotation hook; nil means a
// rotation request from the main till is ignored. It returns a func that
// restores the previous hook.
func SetBearerRotate(fn BearerRotate) (restore func()) {
	var p *BearerRotate
	if fn != nil {
		p = &fn
	}
	prev := bearerRotate.Swap(p)
	bearerRotateLast.Store(0)
	return func() { bearerRotate.Store(prev) }
}

// fireBearerRotate runs the hook in the background: off the request that
// carried the header (a sale never waits for it), one at a time, and at most
// once per bearerRotateRetryEvery. A main till that does not know the
// bearer to be stale simply keeps asking; an offline till gets the header
// with its next answer.
func fireBearerRotate() { fireBearerRotateOpt(false) }

// fireBearerRotateOpt is fireBearerRotate; force skips the once-a-minute
// throttle (never the single-flight guard). A fallback adoption uses it: the
// bearer just adopted is the older one a restored main till knows, and it
// should be swapped for a fresh one at once (ut-docs#5203).
func fireBearerRotateOpt(force bool) {
	hook := bearerRotate.Load()
	if hook == nil {
		return
	}
	if !bearerRotateBusy.CompareAndSwap(false, true) {
		// A rotation is in flight; it may be using the bearer this request
		// just replaced. A forced request is not dropped: the running
		// goroutine runs the hook once more when it is done.
		if force {
			bearerRotateAgain.Store(true)
		}
		return
	}
	now := time.Now().UnixNano()
	if last := bearerRotateLast.Load(); !force && last != 0 && now-last < int64(bearerRotateRetryEvery) {
		bearerRotateBusy.Store(false)
		return
	}
	bearerRotateLast.Store(now)
	go func() {
		defer logging.RecoverAndLog("lantls.bearer_rotate")
		func() {
			defer bearerRotateBusy.Store(false)
			ctx, cancel := context.WithTimeout(context.Background(), bearerRotateTimeout)
			defer cancel()
			(*hook)(ctx)
		}()
		// Busy is released first, so a force request arriving now either
		// started its own run or left the flag for this check. Each flag
		// is consumed by exactly one extra run: no unbounded loop.
		if bearerRotateAgain.CompareAndSwap(true, false) {
			fireBearerRotateOpt(true)
		}
	}()
}

// BearerFallback is the replica's way back to the bearer it held before its
// last rotation (ut-docs#5203). A main till restored from a backup taken
// before that rotation knows only the older bearer, so it answers 401 to the
// current one. Given the bearer a request presented, it returns the previous
// bearer to try (ok=false: none, or presented is not the current bearer any
// more) and adopt, which the transport calls only after the main till has
// accepted that previous bearer. adopt reports whether it actually switched
// bearers (false: it lost a race to a concurrent adoption or a re-join), and
// only then is a prompt rotation requested.
type BearerFallback func(ctx context.Context, presented string) (previous string, adopt func() bool, ok bool)

var bearerFallback atomic.Pointer[BearerFallback]

// SetBearerFallback installs the process-wide fallback hook; nil means a 401
// is final. It returns a func that restores the previous hook.
func SetBearerFallback(fn BearerFallback) (restore func()) {
	var p *BearerFallback
	if fn != nil {
		p = &fn
	}
	prev := bearerFallback.Swap(p)
	return func() { bearerFallback.Store(prev) }
}

// PeerPinned reports whether this till holds a pin for its main till, i.e.
// whether its traffic to the main till is pinned TLS (fleetlink's report).
func PeerPinned(ctx context.Context) bool {
	t, err := peerTarget(ctx)
	return err == nil && t.PrimaryURL != "" && t.Pin != ""
}

// NewPeerClient returns a client for replica → main till traffic. Requests
// to any other host pass through as they are. A timeout of 0 means none
// (the websocket dial bounds itself by its context).
func NewPeerClient(timeout time.Duration) *http.Client {
	return netaccess.NewClientWithTransport(timeout, peerRT)
}

// peerRT is the one transport every peer client shares, so a pinned
// connection is reused across call sites.
var peerRT = &peerTransport{tls: newPinnedTransport()}

type peerTransport struct {
	tls *pinnedTransport
}

func (p *peerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t, err := peerTarget(r.Context())
	if err != nil {
		closeBody(r)
		return nil, fmt.Errorf("lantls: cannot read the main till's address or pin: %w", err)
	}
	resp, err := p.route(r, t)
	if err == nil && resp.StatusCode == http.StatusUnauthorized {
		resp = p.retryWithPreviousBearer(r, t, resp)
	}
	if err == nil && resp.Header.Get(BearerRotateHeader) != "" &&
		samePeerHost(r.URL, t.PrimaryURL) && r.URL.Path != BearerRotatePath {
		fireBearerRotate()
	}
	return resp, err
}

// retryWithPreviousBearer handles a 401 for the replica's current bearer
// (ut-docs#5203). It sends r once more with the previous bearer when the
// installed fallback hook offers one, and returns whichever answer stands:
// the retry's (a 401 included) when it got one, else first. A 401 means the
// main till refused the credential before any handler ran, so replaying is
// safe for any method; a body is replayed only through GetBody. It never
// retries the retry and never touches the rotation call itself.
func (p *peerTransport) retryWithPreviousBearer(r *http.Request, t PeerTarget, first *http.Response) *http.Response {
	hook := bearerFallback.Load()
	if hook == nil || r.URL.Path == BearerRotatePath || !samePeerHost(r.URL, t.PrimaryURL) {
		return first
	}
	presented, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || presented == "" {
		return first
	}
	hasBody := r.Body != nil && r.Body != http.NoBody
	if hasBody && r.GetBody == nil {
		return first
	}
	prev, adopt, ok := (*hook)(r.Context(), presented)
	if !ok || prev == "" || prev == presented {
		return first
	}
	retry := r.Clone(r.Context())
	retry.Header.Set("Authorization", "Bearer "+prev)
	if hasBody {
		body, err := r.GetBody()
		if err != nil {
			return first
		}
		retry.Body = body
	}
	// Drain and close the 401 before the retry goes out, so the retry can
	// reuse the keep-alive connection. The body is kept to hand back if the
	// retry fails to produce any answer.
	buf, _ := io.ReadAll(io.LimitReader(first.Body, 4096))
	_ = first.Body.Close()
	second, err := p.route(retry, t)
	if err != nil {
		first.Body = io.NopCloser(bytes.NewReader(buf))
		return first
	}
	if second.StatusCode != http.StatusUnauthorized && adopt() {
		fireBearerRotateOpt(true)
	}
	return second
}

// route sends r over pinned TLS when it is aimed at the main till and a pin
// is held, and as it is otherwise.
func (p *peerTransport) route(r *http.Request, t PeerTarget) (*http.Response, error) {
	if t.Pin == "" || !samePeerHost(r.URL, t.PrimaryURL) {
		return http.DefaultTransport.RoundTrip(r)
	}
	resp, err := p.tls.roundTrip(upgradeToPeerTLS(r), pinSet{t.Pin, t.NextPin})
	if err == nil || !errors.Is(err, ErrPinMismatch) {
		return resp, err
	}
	return p.reproveAndRetry(r, t.PrimaryURL, t.Pin, err)
}

// reproveAndRetry handles a certificate that matches no held pin (neither
// the primary pin nor a next pin)
// (ADR-0114 §7, ut-docs#4410). The mismatch was caught in the handshake, so
// no byte of r — its bearer included — reached that server. The hook
// proves the main till over a separate bearer-less connection; only if it
// leaves a different pin held is r sent again, once, verified by that pin.
// Otherwise the original mismatch stands.
func (p *peerTransport) reproveAndRetry(r *http.Request, primaryURL, heldPin string, mismatch error) (*http.Response, error) {
	hook := peerReproof.Load()
	if hook == nil {
		return nil, mismatch
	}
	if err := (*hook)(r.Context(), primaryURL, heldPin); err != nil {
		return nil, mismatch
	}
	now, err := peerTarget(r.Context())
	if err != nil || now.Pin == "" || now.Pin == heldPin || !samePeerHost(r.URL, now.PrimaryURL) {
		// Not re-proven, or the main till moved meanwhile: the request
		// would only mismatch again.
		return nil, mismatch
	}
	retry := upgradeToPeerTLS(r)
	if r.Body != nil && r.Body != http.NoBody {
		if r.GetBody == nil {
			// Can't be replayed; the next request uses the new pin.
			return nil, mismatch
		}
		body, err := r.GetBody()
		if err != nil {
			return nil, mismatch
		}
		retry.Body = body
	}
	return p.tls.roundTrip(retry, pinSet{now.Pin, now.NextPin})
}

// closeBody honours RoundTrip's contract to close the body even on error.
func closeBody(r *http.Request) {
	if r.Body != nil {
		_ = r.Body.Close()
	}
}

func (p *peerTransport) CloseIdleConnections() {
	p.tls.closeIdle()
	if c, ok := http.DefaultTransport.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}

// pinSet is the pins a pinned connection may present: the held pin and,
// during a key rotation, the main till's next pin ("" = none).
type pinSet struct{ pin, next string }

// pinnedTransport is the TLS side. Each *http.Transport is bound to one
// pinSet; any change swaps in a fresh one, so no connection verified under
// the old pins — idle or still in flight — can ever carry a request meant
// for the new ones.
type pinnedTransport struct {
	mu   sync.Mutex
	pins pinSet
	tr   *http.Transport
}

func newPinnedTransport() *pinnedTransport { return &pinnedTransport{} }

// transportFor returns the transport bound to pins, replacing the current
// one (and closing its idle connections) when the pins changed.
func (p *pinnedTransport) transportFor(pins pinSet) *http.Transport {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tr != nil && p.pins == pins {
		return p.tr
	}
	if p.tr != nil {
		p.tr.CloseIdleConnections()
	}
	p.pins, p.tr = pins, newPinBoundTransport(pins)
	return p.tr
}

func (p *pinnedTransport) closeIdle() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tr != nil {
		p.tr.CloseIdleConnections()
	}
}

func (p *pinnedTransport) roundTrip(r *http.Request, pins pinSet) (*http.Response, error) {
	return p.transportFor(pins).RoundTrip(r)
}

// matches reports whether got is one of the set's pins.
func (s pinSet) matches(got string) bool {
	ok := subtle.ConstantTimeCompare([]byte(got), []byte(s.pin)) == 1
	if s.next != "" && subtle.ConstantTimeCompare([]byte(got), []byte(s.next)) == 1 {
		ok = true
	}
	return ok
}

// newPinBoundTransport speaks TLS 1.3 to the main till and accepts only a
// certificate whose SPKI pin is one of pins.
func newPinBoundTransport(pins pinSet) *http.Transport {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	// A LAN peer is dialled directly: a proxy would present its own
	// certificate, which is exactly what pinning refuses.
	tr.Proxy = nil
	// HTTP/1.1 only: the link's websocket upgrade needs it, and it is what
	// the main till's LAN port speaks.
	tr.ForceAttemptHTTP2 = false
	tr.TLSClientConfig = &tls.Config{
		MinVersion: tls.VersionTLS13,
		NextProtos: []string{"http/1.1"},
		// Chain, name and date checks are replaced by the pin check: the
		// certificate is self-signed and carries no names (ADR-0114 §7).
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("lantls: server sent no certificate")
			}
			if !pins.matches(PinOf(cs.PeerCertificates[0])) {
				return ErrPinMismatch
			}
			return nil
		},
	}
	return tr
}

// upgradeToPeerTLS returns a copy of r aimed at the same host and port over
// https. The LAN port serves TLS and plain HTTP alike, so an http:// URL
// with no port keeps port 80 rather than moving to 443.
func upgradeToPeerTLS(r *http.Request) *http.Request {
	out := r.Clone(r.Context())
	if out.URL.Scheme == "http" {
		if out.URL.Port() == "" {
			// The Host header stays what the plain request would have sent.
			if out.Host == "" {
				out.Host = r.URL.Host
			}
			out.URL.Host = net.JoinHostPort(out.URL.Hostname(), "80")
		}
		out.URL.Scheme = "https"
	}
	return out
}

// samePeerHost reports whether u addresses the main till at primaryURL:
// same host (case-insensitive) and same port, defaulting each by scheme.
func samePeerHost(u *url.URL, primaryURL string) bool {
	if primaryURL == "" {
		return false
	}
	p, err := url.Parse(primaryURL)
	if err != nil || p.Host == "" {
		return false
	}
	return strings.EqualFold(u.Hostname(), p.Hostname()) && portOf(u) == portOf(p)
}

func portOf(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	if u.Scheme == "https" || u.Scheme == "wss" {
		return "443"
	}
	return "80"
}

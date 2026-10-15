package lantls

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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

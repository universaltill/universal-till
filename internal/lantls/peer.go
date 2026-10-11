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
// (ADR-0114 §11). A pin mismatch fails the request like an unreachable main
// till; adopting a new pin (re-proof) is ut-docs#2737.
//
// Every replica → main till client shares one transport, built by
// NewPeerClient. Call sites keep building plain http:// URLs from
// sync.primary_url; the transport upgrades the ones aimed at the main till,
// so no call site can forget to.

// PeerSource reports the main till this till follows (its sync.primary_url)
// and the pin held for it (sync.primary_cert_pin); "" pin = not pinned. An
// error (the settings could not be read) fails the request closed: an
// unreadable pin is never taken to mean "not pinned".
type PeerSource func(ctx context.Context) (primaryURL, pin string, err error)

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

func peerTarget(ctx context.Context) (primaryURL, pin string, err error) {
	src := peerSource.Load()
	if src == nil {
		return "", "", nil
	}
	primaryURL, pin, err = (*src)(ctx)
	return strings.TrimSpace(primaryURL), strings.TrimSpace(pin), err
}

// PeerPinned reports whether this till holds a pin for its main till, i.e.
// whether its traffic to the main till is pinned TLS (fleetlink's report).
func PeerPinned(ctx context.Context) bool {
	primaryURL, pin, err := peerTarget(ctx)
	return err == nil && primaryURL != "" && pin != ""
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
	primaryURL, pin, err := peerTarget(r.Context())
	if err != nil {
		closeBody(r)
		return nil, fmt.Errorf("lantls: cannot read the main till's address or pin: %w", err)
	}
	if pin == "" || !samePeerHost(r.URL, primaryURL) {
		return http.DefaultTransport.RoundTrip(r)
	}
	return p.tls.roundTrip(upgradeToPeerTLS(r), pin)
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

// pinnedTransport is the TLS side. Each *http.Transport is bound to one
// pin; a pin change swaps in a fresh one, so no connection verified under
// the old pin — idle or still in flight — can ever carry a request meant
// for the new one.
type pinnedTransport struct {
	mu  sync.Mutex
	pin string
	tr  *http.Transport
}

func newPinnedTransport() *pinnedTransport { return &pinnedTransport{} }

// transportFor returns the transport bound to pin, replacing the current
// one (and closing its idle connections) when the pin changed.
func (p *pinnedTransport) transportFor(pin string) *http.Transport {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tr != nil && p.pin == pin {
		return p.tr
	}
	if p.tr != nil {
		p.tr.CloseIdleConnections()
	}
	p.pin, p.tr = pin, newPinBoundTransport(pin)
	return p.tr
}

func (p *pinnedTransport) closeIdle() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tr != nil {
		p.tr.CloseIdleConnections()
	}
}

func (p *pinnedTransport) roundTrip(r *http.Request, pin string) (*http.Response, error) {
	return p.transportFor(pin).RoundTrip(r)
}

// newPinBoundTransport speaks TLS 1.3 to the main till and accepts only a
// certificate whose SPKI pin is pin.
func newPinBoundTransport(pin string) *http.Transport {
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
			if subtle.ConstantTimeCompare([]byte(PinOf(cs.PeerCertificates[0])), []byte(pin)) != 1 {
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

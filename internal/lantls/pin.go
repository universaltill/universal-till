package lantls

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/universaltill/universal-till/internal/netaccess"
)

// Pinning (ADR-0114 §7, ut-docs#4091): a peer trusts the main till by the
// SHA-256 of its certificate's SubjectPublicKeyInfo — learned at pairing,
// or through the primary-proof handshake — never by names or dates.

// ErrPinMismatch is a TLS handshake refused because the server's
// certificate is not the one this client was told to expect.
var ErrPinMismatch = errors.New("lantls: certificate does not match the pinned main till")

// PinOf is the pin of cert: the hex SHA-256 of its SubjectPublicKeyInfo.
func PinOf(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return hex.EncodeToString(sum[:])
}

// Pin is this till's own pin — what it serves on every TLS connection.
func (c *Cert) Pin() string { return c.pin }

type servedPinKey struct{}

// ConnContext is an http.Server ConnContext that stamps c's pin onto every
// request arriving over a TLS connection, so a handler knows which pin the
// peer saw on THAT connection (ServedPin) and a plain-HTTP request carries
// none. A nil c (TLS off: the key couldn't be loaded) stamps nothing.
func ConnContext(c *Cert) func(context.Context, net.Conn) context.Context {
	return func(ctx context.Context, conn net.Conn) context.Context {
		if _, ok := conn.(*tls.Conn); ok && c != nil {
			return context.WithValue(ctx, servedPinKey{}, c.Pin())
		}
		return ctx
	}
}

// ServedPin is the pin served on the connection that carried this request,
// or "" for plain HTTP.
func ServedPin(ctx context.Context) string {
	pin, _ := ctx.Value(servedPinKey{}).(string)
	return pin
}

// PinnedClient speaks TLS to a till's LAN port and verifies the server by
// pin only: no names, no dates, no CA (ADR-0114 §7) — an expired
// certificate, a new IP or a Pi with no RTC never fails a request. With an
// expected pin it refuses any other certificate (ErrPinMismatch) before a
// single request byte is sent; without one it accepts the certificate and
// records its pin (Pin), for the caller to bind into a verification code
// or proof that the real main till must match.
//
// Built for one exchange: each call site makes its own, so Pin is the pin
// of that exchange's connection.
type PinnedClient struct {
	*http.Client
	mu  sync.Mutex
	pin string
}

// pinnedClientTimeout bounds a PinnedClient made with timeout 0.
const pinnedClientTimeout = 10 * time.Second

// NewPinnedClient returns a PinnedClient; expectPin "" means learn, not
// enforce. A timeout of 0 means 10 s.
func NewPinnedClient(timeout time.Duration, expectPin string) *PinnedClient {
	if timeout <= 0 {
		timeout = pinnedClientTimeout
	}
	p := &PinnedClient{}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	// A LAN peer is dialled directly: a proxy in between would present its
	// own certificate, which is exactly what pinning refuses.
	tr.Proxy = nil
	tr.ForceAttemptHTTP2 = false
	// One exchange per client: nothing idles on either till afterwards.
	tr.DisableKeepAlives = true
	tr.TLSClientConfig = &tls.Config{
		MinVersion: tls.VersionTLS13,
		NextProtos: []string{"http/1.1"},
		// Chain and name checks are replaced by VerifyConnection's pin
		// check: the certificate is self-signed and carries no names.
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("lantls: server sent no certificate")
			}
			pin := PinOf(cs.PeerCertificates[0])
			if expectPin != "" && subtle.ConstantTimeCompare([]byte(pin), []byte(expectPin)) != 1 {
				return ErrPinMismatch
			}
			p.mu.Lock()
			p.pin = pin
			p.mu.Unlock()
			return nil
		},
	}
	p.Client = netaccess.NewClientWithTransport(timeout, tr)
	// The pinned peer answers itself: a redirect could carry a URL secret
	// (pairing's request_secret) somewhere unpinned, plain HTTP included.
	p.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return p
}

// Exchange sends req over TLS. On failure, plainOK reports whether the
// caller may retry over plain HTTP: the TCP connection opened (so the peer
// is there — a dead host would only cost a second timeout) but no request
// was written (so the peer can't have acted on it twice), and the failure
// isn't a pin mismatch. That is the shape of a till without TLS answering a
// ClientHello as a bad HTTP request (ADR-0114 §11).
func (p *PinnedClient) Exchange(req *http.Request) (resp *http.Response, plainOK bool, err error) {
	var connected, wrote atomic.Bool
	trace := &httptrace.ClientTrace{
		ConnectDone: func(_, _ string, err error) {
			if err == nil {
				connected.Store(true)
			}
		},
		WroteRequest: func(httptrace.WroteRequestInfo) { wrote.Store(true) },
	}
	resp, err = p.Do(req.WithContext(httptrace.WithClientTrace(req.Context(), trace)))
	if err != nil {
		return nil, connected.Load() && !wrote.Load() && !errors.Is(err, ErrPinMismatch), err
	}
	return resp, false, nil
}

// Pin is the pin of the certificate this client accepted ("" before any
// handshake succeeded).
func (p *PinnedClient) Pin() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pin
}

// HTTPSBase turns a till's plain-HTTP base URL into the TLS URL for the same
// port (the LAN port serves both, ADR-0114 §7). ok is false for anything but
// an http:// URL with a host: an https:// base is not a LAN till's same-port
// upgrade, and is left to the caller as it is.
func HTTPSBase(base string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Scheme != "http" || u.Host == "" {
		return "", false
	}
	u.Scheme = "https"
	return strings.TrimSuffix(u.String(), "/"), true
}

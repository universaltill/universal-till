// Package netaccess is the till's single place that opens outbound network
// connections (ADR-0113 §1.6, ut-docs#2795). Every outbound *http.Client and
// every raw TCP dial in internal/ is built here — scripts/ci/guard-netaccess.sh
// forbids `http.Client{` literals and `net.Dial*` anywhere else — so the
// public-demo till ("demo mode", UT_DEMO) can deny all of them at one seam.
//
// With demo mode off (every real till) the clients and dialers behave exactly
// like the plain http.Client / net.Dialer they replace: same timeout, the
// caller's transport (or http.DefaultTransport, resolved per request as an
// unset Transport would be), same dial options.
//
// With demo mode on, every request and every dial fails with ErrDemoDenied
// before anything leaves the process. The switch is process-wide and read at
// request time, not construction time, because most clients are package-level
// vars built at init — before config.Init has even read UT_DEMO. app.Run sets
// it from cfg.Demo once, at boot, before anything can make a request.
//
// StartService is the matching seam for background services: app.Run and
// pages.Init start every network service through it, so a demo till never
// starts them at all.
package netaccess

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
)

// ErrDemoDenied is returned by every request and dial made through this
// package while demo mode is on.
var ErrDemoDenied = errors.New("outbound network is disabled on the public demo till (ADR-0113)")

var demo atomic.Bool

// SetDemo switches demo mode on or off for the whole process. app.Run calls
// it with cfg.Demo at boot; tests call it directly.
func SetDemo(on bool) { demo.Store(on) }

// Demo reports whether demo mode is on.
func Demo() bool { return demo.Load() }

// NewClient returns an outbound HTTP client with the given timeout (0 = no
// client timeout, as on http.Client) using http.DefaultTransport. It is the
// replacement for `&http.Client{Timeout: timeout}`.
func NewClient(timeout time.Duration) *http.Client {
	return NewClientWithTransport(timeout, nil)
}

// NewClientWithTransport is NewClient over the caller's own transport (custom
// TLS, pooling). A nil base means http.DefaultTransport. Callers that need
// CheckRedirect or a Jar set those on the returned client; they must not
// replace its Transport.
func NewClientWithTransport(timeout time.Duration, base http.RoundTripper) *http.Client {
	return &http.Client{Timeout: timeout, Transport: &guardTransport{base: base}}
}

// guardTransport denies every request while demo mode is on and otherwise
// delegates to base (or http.DefaultTransport, looked up per request exactly
// as http.Client does for a nil Transport).
type guardTransport struct {
	base http.RoundTripper
}

func (g *guardTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if Demo() {
		// RoundTripper contract: close the request body even on error.
		if r.Body != nil {
			_ = r.Body.Close()
		}
		return nil, ErrDemoDenied
	}
	return g.transport().RoundTrip(r)
}

func (g *guardTransport) transport() http.RoundTripper {
	if g.base != nil {
		return g.base
	}
	return http.DefaultTransport
}

// CloseIdleConnections forwards to the underlying transport, so
// http.Client.CloseIdleConnections keeps working as on a plain client.
func (g *guardTransport) CloseIdleConnections() {
	if c, ok := g.transport().(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}

// BaseTransport returns the transport a netaccess client's Transport
// delegates to (http.DefaultTransport if it was built without one); any other
// RoundTripper is returned unchanged. For tests that pin the knobs of the
// transport a caller supplied.
func BaseTransport(rt http.RoundTripper) http.RoundTripper {
	if g, ok := rt.(*guardTransport); ok {
		return g.transport()
	}
	return rt
}

// DialFunc is the shape of net.Dialer.DialContext / http.Transport.DialContext.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// DialContext returns a dial function equivalent to
// (&net.Dialer{Timeout: timeout, KeepAlive: keepAlive}).DialContext that
// refuses every dial while demo mode is on. It is the replacement for raw
// net.Dial / net.Dialer use (network printers, plugin tcp/http egress).
func DialContext(timeout, keepAlive time.Duration) DialFunc {
	d := &net.Dialer{Timeout: timeout, KeepAlive: keepAlive}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if Demo() {
			return nil, ErrDemoDenied
		}
		return d.DialContext(ctx, network, addr)
	}
}

// serviceObserver, when set, is told about every StartService call. It is a
// test seam (ObserveServices), nil in production.
var serviceObserver atomic.Pointer[func(name string, started bool)]

// StartService runs start — which launches a background service that talks
// to the network (cloud sync, enrolment, marketplace, self-update, LAN sync,
// mDNS...) — unless demo mode is on, in which case the service is never
// started and the skip is logged. It reports whether start ran.
func StartService(name string, start func()) bool {
	started := !Demo()
	if started {
		start()
	} else {
		logging.L().Infof("demo mode (ADR-0113): not starting network service %q", name)
	}
	if fn := serviceObserver.Load(); fn != nil {
		(*fn)(name, started)
	}
	return started
}

// ObserveServices registers fn to be called for every StartService call
// (name, whether it started) until the returned restore func is called. Test
// seam, in the same spirit as app.pagesInit: it lets a test prove which
// network services a boot started or skipped.
func ObserveServices(fn func(name string, started bool)) (restore func()) {
	prev := serviceObserver.Swap(&fn)
	return func() { serviceObserver.Store(prev) }
}

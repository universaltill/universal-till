// Package netreach answers "can this till reach its cloud right now?" for
// the status-bar light (ut-docs#3095). navigator.onLine only says a network
// interface is up, so on a Wi-Fi network whose router has no internet every
// platform still showed "Online".
//
// A Monitor probes the configured cloud host's unauthenticated /healthz
// (at the host root, not under /api). Any completed HTTP response — any
// status — means reachable; a transport error or timeout means unreachable.
// That is sound for the production https endpoint: a captive portal or a
// proxy without internet fails the TLS handshake or CONNECT. A plain-http
// non-loopback endpoint could be answered by a portal's own page and read
// as reachable — an accepted limitation (review of ut-docs#3095).
// Status never waits on the network: it returns the cached result and, when
// that is older than the TTL, starts at most one background probe.
//
// It is display-only. Checkout never consults it (offline-first; the sale
// path's own offline flag stays on navigator.onLine, ADR-0044 D1).
package netreach

import (
	"context"
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

const (
	// DefaultTimeout bounds one probe, and so the background goroutine.
	DefaultTimeout = 5 * time.Second
	// DefaultTTL is how long a probe result is fresh.
	DefaultTTL = 10 * time.Second
	// healthPath is ut-cloud's unauthenticated liveness endpoint.
	healthPath = "/healthz"
	// maxDrain caps how much of a probe response body is read.
	maxDrain = 4 << 10
)

// State is the tri-state reachability result.
type State int

const (
	// Unknown: disabled, or no probe has completed yet.
	Unknown State = iota
	// Reachable: the last probe got an HTTP response.
	Reachable
	// Unreachable: the last probe failed at the transport or timed out.
	Unreachable
)

// Reachable maps s to the JSON tri-state: nil (unknown), true or false.
func (s State) Reachable() *bool {
	switch s {
	case Reachable:
		v := true
		return &v
	case Unreachable:
		v := false
		return &v
	default:
		return nil
	}
}

// Options configures a Monitor. Zero values take the defaults.
type Options struct {
	// Endpoint is the till's cloud endpoint (cfg.Marketplace.EndpointURL,
	// e.g. https://cloud.universaltill.com/api).
	Endpoint string
	// Ctx ends in-flight probes on shutdown (app-lifetime bgCtx).
	Ctx     context.Context
	Client  *http.Client
	Now     func() time.Time
	Timeout time.Duration
	TTL     time.Duration

	// allowLoopback lets in-package tests point at an httptest server.
	allowLoopback bool
}

// Monitor caches the cloud host's reachability. The zero value and a nil
// *Monitor are disabled (always Unknown).
type Monitor struct {
	probeURL string
	ctx      context.Context
	client   *http.Client
	now      func() time.Time
	timeout  time.Duration
	ttl      time.Duration

	mu       sync.Mutex
	state    State
	checked  time.Time // when the last probe completed; zero = never
	inFlight bool
}

// New builds a Monitor. It is disabled when the endpoint is empty,
// unparseable, not http(s), or on a loopback host (the dev/e2e default
// http://127.0.0.1:8081/api) — there it always reports Unknown and the
// light behaves as it did before ut-docs#3095.
func New(o Options) *Monitor {
	m := &Monitor{ctx: o.Ctx, client: o.Client, now: o.Now, timeout: o.Timeout, ttl: o.TTL}
	if m.ctx == nil {
		m.ctx = context.Background()
	}
	if m.client == nil {
		// netaccess.NewClient(0) replaces http.DefaultClient (same zero
		// timeout, same default transport) so the public demo till refuses
		// this request too, in addition to Enabled()'s own demo check below
		// — defence in depth (ADR-0113 §1.6, ut-docs#2795 review finding
		// F1, ut-docs#3588).
		m.client = netaccess.NewClient(0)
	}
	if m.now == nil {
		m.now = time.Now
	}
	if m.timeout <= 0 {
		m.timeout = DefaultTimeout
	}
	if m.ttl <= 0 {
		m.ttl = DefaultTTL
	}
	if u, ok := probeURL(o.Endpoint, o.allowLoopback); ok {
		m.probeURL = u
	}
	return m
}

func probeURL(endpoint string, allowLoopback bool) (string, bool) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return "", false
	}
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "", false
	}
	if !allowLoopback && isLoopback(u.Hostname()) {
		return "", false
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: healthPath}).String(), true
}

// isLoopback mirrors internal/cloudlink's rule: localhost or a loopback IP.
func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Enabled reports whether this Monitor probes anything. False on the public
// demo till (ADR-0113 §1.6, ut-docs#3588): checked live, not just at
// construction, so the guarantee holds even if a Monitor is ever built
// before netaccess.SetDemo runs at boot.
func (m *Monitor) Enabled() bool { return m != nil && m.probeURL != "" && !netaccess.Demo() }

// Status returns the cached state immediately. When the result is stale
// (older than the TTL, or never probed) and no probe is running, it starts
// one in the background; the next call sees its result.
func (m *Monitor) Status() State {
	if !m.Enabled() {
		return Unknown
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.inFlight && (m.checked.IsZero() || m.now().Sub(m.checked) >= m.ttl) {
		m.inFlight = true
		go func() {
			defer logging.RecoverAndLog("netreach.probe")
			m.probe()
		}()
	}
	return m.state
}

// probe runs one bounded request and records its outcome.
func (m *Monitor) probe() {
	st := Unreachable
	// Record the outcome in a defer so a panic mid-probe (recovered by the
	// caller's logging.RecoverAndLog) still clears inFlight; otherwise no
	// probe would ever start again (ut-docs#3304).
	defer func() {
		m.mu.Lock()
		m.state, m.checked, m.inFlight = st, m.now(), false
		m.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(m.ctx, m.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.probeURL, nil)
	if err == nil {
		resp, err := m.client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrain))
			_ = resp.Body.Close()
			st = Reachable
		}
	}
}

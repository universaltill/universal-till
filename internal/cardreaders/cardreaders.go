// Package cardreaders holds the provider-agnostic parts of the per-till card
// reader registry (ut-docs#6566, ADR-0201 amendment 2026-11-18): the
// `payment.<key>.readers` state answer parser, a bounded short-lived state
// cache, and the set of readers a payment is currently running on. It has no
// SQL and names no provider: the registry rows live in internal/data, the
// plugin call is injected as a Querier.
package cardreaders

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
)

// State is a reader's readiness as shown in the picker.
type State string

const (
	Ready      State = "ready"
	Busy       State = "busy"
	Offline    State = "offline"
	NeedsSetup State = "needs_setup"
	Unknown    State = "unknown"
	// Unchecked: the plugin does not answer `.readers`, so nothing is known.
	Unchecked State = "unchecked"
)

// Selectable: only busy, offline and needs_setup readers are refused; an
// unknown or unchecked reader can still be tried (the provider fails safe).
func (s State) Selectable() bool {
	switch s {
	case Busy, Offline, NeedsSetup:
		return false
	}
	return true
}

const (
	// MaxRefsPerCall bounds one `.readers` request.
	MaxRefsPerCall = 16
	// MaxAnswerBytes bounds one `.readers` answer; a larger one is unknown.
	MaxAnswerBytes = 8 << 10
	// CacheTTL and CacheMax bound the state cache (till binding rule).
	CacheTTL = 10 * time.Second
	CacheMax = 32
	// Deadline is the total time a picker open waits for provider states.
	Deadline = 3 * time.Second
)

// Answer is one reader's state plus an optional reason: only a known
// reason slug is kept (it becomes the i18n key tap.reason.<slug>), a
// plugin-supplied string is never shown.
type Answer struct {
	State  State
	Reason string
}

var knownReasons = map[string]bool{"reader_busy": true, "reader_unreachable": true, "reader_needs_setup": true}

// ParseAnswer maps a plugin's `.readers` answer onto refs. A missing reader,
// an unknown state value, an oversize or malformed answer is `unknown`.
func ParseAnswer(raw json.RawMessage, refs []string) map[string]Answer {
	out := make(map[string]Answer, len(refs))
	for _, r := range refs {
		out[r] = Answer{State: Unknown}
	}
	if len(raw) == 0 || len(raw) > MaxAnswerBytes {
		return out
	}
	var body struct {
		Readers []struct {
			ReaderID string `json:"reader_id"`
			State    string `json:"state"`
			Reason   string `json:"reason"`
		} `json:"readers"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return out
	}
	for _, r := range body.Readers {
		if _, asked := out[r.ReaderID]; !asked {
			continue
		}
		a := Answer{State: Unknown}
		switch State(r.State) {
		case Ready, Busy, Offline, NeedsSetup, Unknown:
			a.State = State(r.State)
		}
		if knownReasons[r.Reason] {
			a.Reason = r.Reason
		}
		out[r.ReaderID] = a
	}
	return out
}

// TapToPayAvailable reports whether a Tap to Pay device bridge is registered
// on this build (the iOS/Android flags default OFF, so a desktop, Pi or
// flag-off build never has one). The mobile shell swaps in the real check
// (devicepay.RegisteredBridge() != nil) when it registers its bridge; core
// pages never import the device package, which keeps the provider-specific
// Tap to Pay code out of the till's own binary. A kind='tap_to_pay' registry
// row is offered only while this is true.
var TapToPayAvailable = func() bool { return false }

// Probe names one registry row to check.
type Probe struct{ ID, Method, Ref string }

// Querier asks the plugin behind method for its readers' states and returns
// the raw answer. Injected by the caller (the plugin bus lives in pages).
type Querier func(ctx context.Context, method string, refs []string) (json.RawMessage, error)

type cached struct {
	a  Answer
	at time.Time
}

type flight struct {
	done chan struct{}
	res  map[string]Answer
}

// Cache is a bounded (CacheMax entries, TTL) state cache with single-flight
// per (method, refs), so concurrent picker opens ask the provider once.
type Cache struct {
	mu       sync.Mutex
	ttl      time.Duration
	now      func() time.Time
	deadline time.Duration
	m        map[string]cached
	flights  map[string]*flight
}

func newCache(ttl time.Duration, now func() time.Time) *Cache {
	return &Cache{ttl: ttl, now: now, deadline: Deadline, m: map[string]cached{}, flights: map[string]*flight{}}
}

var shared = newCache(CacheTTL, time.Now)

// SharedCache is the process-wide state cache.
func SharedCache() *Cache { return shared }

// Reset empties the cache (tests, and after a registry edit).
func (c *Cache) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m = map[string]cached{}
}

func (c *Cache) put(id string, a Answer) {
	// caller holds c.mu
	now := c.now()
	for k, v := range c.m {
		if now.Sub(v.at) >= c.ttl {
			delete(c.m, k)
		}
	}
	for len(c.m) >= CacheMax {
		oldest, oldestAt := "", time.Time{}
		for k, v := range c.m {
			if oldest == "" || v.at.Before(oldestAt) {
				oldest, oldestAt = k, v.at
			}
		}
		delete(c.m, oldest)
	}
	c.m[id] = cached{a: a, at: now}
}

// States returns the provider-reported state of every probe, keyed by probe
// id: fresh cache entries first, then one parallel `.readers` call per
// (method, chunk of MaxRefsPerCall) under one overall Deadline. Anything
// unanswered in time is Unknown (selectable). Failed checks are not cached.
func (c *Cache) States(ctx context.Context, probes []Probe, q Querier) map[string]Answer {
	out := make(map[string]Answer, len(probes))
	byMethod := map[string][]Probe{}
	now := c.now()
	c.mu.Lock()
	for _, p := range probes {
		if v, ok := c.m[p.ID]; ok && now.Sub(v.at) < c.ttl {
			out[p.ID] = v.a
			continue
		}
		byMethod[p.Method] = append(byMethod[p.Method], p)
	}
	c.mu.Unlock()
	if len(byMethod) == 0 {
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, c.deadline)
	defer cancel()

	type result struct {
		probes []Probe
		ans    map[string]Answer
		ok     bool
	}
	var wg sync.WaitGroup
	resCh := make(chan result, len(probes)+1)
	methods := make([]string, 0, len(byMethod))
	for m := range byMethod {
		methods = append(methods, m)
	}
	sort.Strings(methods)
	for _, m := range methods {
		ps := byMethod[m]
		for i := 0; i < len(ps); i += MaxRefsPerCall {
			end := i + MaxRefsPerCall
			if end > len(ps) {
				end = len(ps)
			}
			chunk := ps[i:end]
			wg.Add(1)
			go func(method string, chunk []Probe) {
				defer logging.RecoverAndLog("cardreaders.readers")
				defer wg.Done()
				refs := make([]string, len(chunk))
				for j, p := range chunk {
					refs[j] = p.Ref
				}
				ans, ok := c.ask(ctx, method, refs, q)
				resCh <- result{probes: chunk, ans: ans, ok: ok}
			}(m, chunk)
		}
	}
	done := make(chan struct{})
	go func() {
		defer logging.RecoverAndLog("cardreaders.wait")
		wg.Wait()
		close(done)
	}()
	collect := func(r result) {
		c.mu.Lock()
		defer c.mu.Unlock()
		for _, p := range r.probes {
			a, found := r.ans[p.Ref]
			if !r.ok || !found {
				out[p.ID] = Answer{State: Unknown}
				continue
			}
			out[p.ID] = a
			if r.ok {
				c.put(p.ID, a)
			}
		}
	}
	pending := 0
	for _, m := range methods {
		pending += (len(byMethod[m]) + MaxRefsPerCall - 1) / MaxRefsPerCall
	}
loop:
	for pending > 0 {
		select {
		case r := <-resCh:
			collect(r)
			pending--
		case <-ctx.Done():
			break loop
		case <-done:
			for len(resCh) > 0 {
				collect(<-resCh)
			}
			break loop
		}
	}
	for _, p := range probes {
		if _, ok := out[p.ID]; !ok {
			out[p.ID] = Answer{State: Unknown}
		}
	}
	return out
}

// ask runs one `.readers` call, single-flight per (method, refs).
func (c *Cache) ask(ctx context.Context, method string, refs []string, q Querier) (map[string]Answer, bool) {
	key := method + "|" + strings.Join(refs, ",")
	c.mu.Lock()
	if f, ok := c.flights[key]; ok {
		c.mu.Unlock()
		select {
		case <-f.done:
			return f.res, f.res != nil
		case <-ctx.Done():
			return nil, false
		}
	}
	f := &flight{done: make(chan struct{})}
	c.flights[key] = f
	c.mu.Unlock()
	// Released even when q panics (the caller's goroutine recovers), or the key
	// would stay in flight, and unknown, until a restart.
	defer func() {
		c.mu.Lock()
		delete(c.flights, key)
		c.mu.Unlock()
		close(f.done)
	}()
	raw, err := q(ctx, method, refs)
	if err == nil {
		f.res = ParseAnswer(raw, refs)
	}
	return f.res, f.res != nil
}

// InFlight is the set of registry rows a payment is running on right now
// (added before the authorize dispatch, released after). Core's own busy
// knowledge: it wins over whatever the provider reports.
type InFlight struct {
	mu sync.Mutex
	m  map[string]struct{}
}

// NewInFlight returns an empty set.
func NewInFlight() *InFlight { return &InFlight{m: map[string]struct{}{}} }

var sharedInFlight = NewInFlight()

// SharedInFlight is the process-wide in-flight set.
func SharedInFlight() *InFlight { return sharedInFlight }

// Acquire marks id busy; false when it already is.
func (f *InFlight) Acquire(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.m[id]; ok {
		return false
	}
	f.m[id] = struct{}{}
	return true
}

// Release frees id.
func (f *InFlight) Release(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.m, id)
}

// Has reports whether id is in flight.
func (f *InFlight) Has(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.m[id]
	return ok
}

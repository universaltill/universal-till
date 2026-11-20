package pages

import (
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Per-IP bearer-failure limiter (ADR-0114 §7, ut-docs#6538): a source that
// keeps presenting bearers nobody holds is locked out for a while, so a LAN
// device cannot walk the 256-bit space by brute force. Till and card reader
// lookups share one counter (a probe cannot alternate tables to get double
// the budget). While locked, even a VALID bearer from that source is refused
// (401 + Retry-After), otherwise a guess that hits would be told apart.
//
// Loopback is never limited: the main till calls itself. The map is bounded:
// stale sources are swept, and past bearerFailureMaxSources the table resets
// (a flood from that many distinct sources has already left the LAN's scale).

const (
	bearerFailMax        = 20
	bearerFailWindow     = time.Minute
	bearerFailLockout    = time.Minute
	bearerFailMaxSources = 4096
)

type ipFailures struct {
	times []time.Time
	until time.Time
}

type bearerFailureLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	lock   time.Duration
	now    func() time.Time
	m      map[string]*ipFailures
}

func newBearerFailureLimiter(max int, window, lock time.Duration, now func() time.Time) *bearerFailureLimiter {
	return &bearerFailureLimiter{max: max, window: window, lock: lock, now: now, m: map[string]*ipFailures{}}
}

func newDefaultBearerFailureLimiter() *bearerFailureLimiter {
	return newBearerFailureLimiter(bearerFailMax, bearerFailWindow, bearerFailLockout, time.Now)
}

// syncBearerFailures is the process-wide limiter; a var so tests swap it.
var syncBearerFailures = newDefaultBearerFailureLimiter()

func limitedIP(ip string) bool {
	if ip == "" {
		return false
	}
	if p := net.ParseIP(ip); p != nil && p.IsLoopback() {
		return false
	}
	return true
}

// fail records one failed bearer from ip and locks it out at the cap.
func (l *bearerFailureLimiter) fail(ip string) {
	if !limitedIP(ip) {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.m) >= bearerFailMaxSources {
		for k, v := range l.m {
			if !v.until.After(now) && (len(v.times) == 0 || !v.times[len(v.times)-1].After(now.Add(-l.window))) {
				delete(l.m, k)
			}
		}
		if len(l.m) >= bearerFailMaxSources {
			l.m = map[string]*ipFailures{}
		}
	}
	f := l.m[ip]
	if f == nil {
		f = &ipFailures{}
		l.m[ip] = f
	}
	cutoff := now.Add(-l.window)
	kept := f.times[:0]
	for _, t := range f.times {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	f.times = append(kept, now)
	if len(f.times) >= l.max {
		f.until = now.Add(l.lock)
		f.times = f.times[:0]
	}
}

// retryAfter is how long ip stays locked out; 0 when it is not.
func (l *bearerFailureLimiter) retryAfter(ip string) time.Duration {
	if !limitedIP(ip) {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	f := l.m[ip]
	if f == nil {
		return 0
	}
	if d := f.until.Sub(l.now()); d > 0 {
		return d
	}
	return 0
}

func (l *bearerFailureLimiter) locked(ip string) bool { return l.retryAfter(ip) > 0 }

// bearerLockedOut reports whether r's source is locked out and, if so, sets
// Retry-After on w.
func bearerLockedOut(w http.ResponseWriter, r *http.Request) bool {
	d := syncBearerFailures.retryAfter(sourceOf(r))
	if d <= 0 {
		return false
	}
	secs := int(d.Seconds()) + 1
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	return true
}

func recordBearerFailure(r *http.Request) { syncBearerFailures.fail(sourceOf(r)) }

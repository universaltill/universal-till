package print

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
)

// DestState is what the OS print system says about print destinations
// (ut-docs#6154).
type DestState int

const (
	// DestUnknown: error, timeout or not probed yet — keep browser print.
	DestUnknown DestState = iota
	// DestNone: definitely no destination.
	DestNone
	// DestPresent: at least one CUPS queue.
	DestPresent
)

// DestProber answers one probe (blocking, bounded by its own timeout).
type DestProber interface {
	Probe(ctx context.Context) DestState
}

// LpstatProber runs `lpstat -e` (destination names, one per line).
type LpstatProber struct {
	Timeout  time.Duration                // 0 → 2s
	LookPath func(string) (string, error) // nil → exec.LookPath
}

// Probe maps lpstat's answer to a DestState. Only a definite answer is
// DestNone/DestPresent; every error or timeout is DestUnknown.
func (p LpstatProber) Probe(ctx context.Context) DestState {
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	look := p.LookPath
	if look == nil {
		look = exec.LookPath
	}
	path, err := look("lpstat")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return DestNone
		}
		return DestUnknown
	}
	ctx2, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx2, path, "-e")
	cmd.WaitDelay = timeout
	var out bytes.Buffer
	cmd.Stdout = &out
	runErr := cmd.Run()
	if ctx2.Err() != nil || runErr != nil {
		return DestUnknown
	}
	if strings.TrimSpace(out.String()) == "" {
		return DestNone
	}
	return DestPresent
}

// CachedProber is a TTL cache over a DestProber that NEVER blocks a caller:
// State returns the last known value and, when stale, starts one background
// refresh.
type CachedProber struct {
	inner DestProber
	ttl   time.Duration
	now   func() time.Time
	spawn func(func())

	mu    sync.Mutex
	state DestState
	at    time.Time
	have  bool
	busy  bool
}

// NewCachedProber wraps inner; ttl 0 → 60s.
func NewCachedProber(inner DestProber, ttl time.Duration) *CachedProber {
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	return &CachedProber{
		inner: inner, ttl: ttl, now: time.Now,
		spawn: func(f func()) {
			go func() {
				defer logging.RecoverAndLog("print.sysprobe")
				f()
			}()
		},
	}
}

// State returns the last known state (DestUnknown before the first probe).
func (c *CachedProber) State() DestState {
	c.mu.Lock()
	stale := !c.have || c.now().Sub(c.at) >= c.ttl
	start := stale && !c.busy
	if start {
		c.busy = true
	}
	c.mu.Unlock()
	if start {
		c.spawn(func() { c.refresh(context.Background()) })
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// Refresh probes now (blocking) and stores the result; used for boot warm-up
// and tests.
func (c *CachedProber) Refresh(ctx context.Context) DestState {
	c.mu.Lock()
	c.busy = true
	c.mu.Unlock()
	return c.refresh(ctx)
}

func (c *CachedProber) refresh(ctx context.Context) DestState {
	// A panicking probe must not leave busy set, or no refresh would ever
	// start again (review nit, ut-docs#6154).
	defer func() {
		c.mu.Lock()
		c.busy = false
		c.mu.Unlock()
	}()
	st := c.inner.Probe(ctx)
	c.mu.Lock()
	c.state, c.at, c.have = st, c.now(), true
	c.mu.Unlock()
	return st
}

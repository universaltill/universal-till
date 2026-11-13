package print

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeLpstat writes an executable `lpstat` shell script into a fresh dir and
// returns the dir, to be set as PATH.
func fakeLpstat(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "lpstat"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLpstatProber_States(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh fake lpstat")
	}
	emptyDir := t.TempDir()
	cases := []struct {
		name    string
		path    string
		timeout time.Duration
		look    func(string) (string, error)
		want    DestState
	}{
		{"no lpstat on PATH", emptyDir, 0, nil, DestNone},
		{"prints nothing", fakeLpstat(t, "exit 0"), 0, nil, DestNone},
		{"prints a queue", fakeLpstat(t, "echo Office_HP"), 0, nil, DestPresent},
		{"hangs past timeout", fakeLpstat(t, "sleep 5"), 100 * time.Millisecond, nil, DestUnknown},
		{"non-zero exit", fakeLpstat(t, "echo 'scheduler is not running' >&2; exit 1"), 0, nil, DestUnknown},
		{"other lookpath error", emptyDir, 0, func(string) (string, error) { return "", errors.New("boom") }, DestUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PATH", tc.path)
			start := time.Now()
			got := LpstatProber{Timeout: tc.timeout, LookPath: tc.look}.Probe(context.Background())
			if got != tc.want {
				t.Fatalf("Probe = %v, want %v", got, tc.want)
			}
			if el := time.Since(start); el > time.Second {
				t.Fatalf("Probe took %v, want < 1s", el)
			}
		})
	}
}

type countingProber struct {
	n     atomic.Int32
	state DestState
	block chan struct{}
}

func (c *countingProber) Probe(context.Context) DestState {
	c.n.Add(1)
	if c.block != nil {
		<-c.block
	}
	return c.state
}

func newSyncCached(inner DestProber, ttl time.Duration, now func() time.Time) *CachedProber {
	c := NewCachedProber(inner, ttl)
	c.now = now
	c.spawn = func(f func()) { f() }
	return c
}

func TestCachedProber_TTL(t *testing.T) {
	inner := &countingProber{state: DestNone}
	clock := time.Unix(1000, 0) // fixed-date:ok synthetic clock, no real-clock check reads it
	c := newSyncCached(inner, time.Minute, func() time.Time { return clock })
	if got := c.State(); got != DestNone {
		// sync spawn: the first State() refreshes before returning the cache
		_ = got
	}
	for i := 0; i < 10; i++ {
		c.State()
	}
	if n := inner.n.Load(); n != 1 {
		t.Fatalf("inner calls within TTL = %d, want 1", n)
	}
	clock = clock.Add(2 * time.Minute)
	c.State()
	if n := inner.n.Load(); n != 2 {
		t.Fatalf("inner calls after TTL = %d, want 2", n)
	}
	if got := c.State(); got != DestNone {
		t.Fatalf("State = %v, want DestNone", got)
	}
}

func TestCachedProber_NeverBlocks(t *testing.T) {
	inner := &countingProber{state: DestNone, block: make(chan struct{})}
	c := NewCachedProber(inner, time.Minute)
	start := time.Now()
	if got := c.State(); got != DestUnknown {
		t.Fatalf("first State = %v, want DestUnknown", got)
	}
	if el := time.Since(start); el > 50*time.Millisecond {
		t.Fatalf("State blocked for %v", el)
	}
	// second call while busy must not start another refresh
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); c.State() }()
	}
	wg.Wait()
	time.Sleep(20 * time.Millisecond)
	if n := inner.n.Load(); n != 1 {
		t.Fatalf("inner calls while busy = %d, want 1", n)
	}
	close(inner.block)
	deadline := time.Now().Add(time.Second)
	for c.State() != DestNone && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := c.State(); got != DestNone {
		t.Fatalf("after refresh State = %v, want DestNone", got)
	}
}

type panicOnceProber struct{ calls int }

func (p *panicOnceProber) Probe(context.Context) DestState {
	p.calls++
	if p.calls == 1 {
		panic("probe blew up")
	}
	return DestNone
}

// A probe that panics must not leave the cache busy forever (review nit,
// ut-docs#6154): the next stale State() starts a fresh probe.
func TestCachedProber_PanicDoesNotWedge(t *testing.T) {
	inner := &panicOnceProber{}
	now := time.Unix(1_000_000, 0) // fixed-date:ok synthetic clock, no real-clock check
	c := newSyncCached(inner, time.Minute, func() time.Time { return now })
	func() {
		defer func() { _ = recover() }()
		c.State()
	}()
	if got := c.State(); got != DestNone {
		t.Fatalf("after a panicking probe, State() = %v, want DestNone (second probe never ran; calls=%d)", got, inner.calls)
	}
}

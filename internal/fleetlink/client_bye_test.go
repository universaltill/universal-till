package fleetlink

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// ut-docs#4422: a clean shutdown of the main till says bye, which is not an
// outage by itself (a restart or an update comes back) — but a main till
// that said bye and is still gone one presence window later is gone: the
// chip must not read "polling" until PrimaryWatch's 90 s window runs out.

// The main till as the client's probe sees it.
const (
	mainUp     int32 = iota // offers the link
	mainGone                // the process is gone: nothing answers
	mainNoLink              // answers, but offers no link (an older build)
)

// byeHarness links a client to a swap harness whose probe answers as main
// says.
func byeHarness(t *testing.T, peerTimeout time.Duration) (*swapHarness, *Client, *atomic.Int32, *clientRec) {
	t.Helper()
	cfg := fastConfig()
	cfg.PeerTimeout = peerTimeout
	h := newSwapHarness(t, cfg)
	tt := &testTarget{}
	tt.set(Target{BaseURL: h.srv.URL, Bearer: "good-till-2"})
	rec := &clientRec{}
	opts := fastClientOptions(cfg, tt, rec, advertised())
	var main atomic.Int32
	opts.Probe = func(context.Context, Target) (int, error) {
		switch main.Load() {
		case mainGone:
			return 0, errors.New("connection refused")
		case mainNoLink:
			return 0, nil
		}
		return SyncProtocolLevel, nil
	}
	c := NewClient(opts)
	startClient(t, c)
	waitFor(t, "linked", c.Linked)
	return h, c, &main, rec
}

func TestClient_ByeThenGoneReadsLostAfterOnePresenceWindow(t *testing.T) {
	const window = 300 * time.Millisecond
	h, c, main, rec := byeHarness(t, window)

	main.Store(mainGone)
	before := time.Now()
	h.currentHub().Close() // bye "shutdown", then nothing answers
	waitFor(t, "link down", func() bool { return !c.Linked() })
	after := time.Now()
	if s := c.Status(); !s.LostAt.IsZero() {
		t.Fatalf("a fresh bye read as an outage at once: %+v", s)
	}

	waitFor(t, "a bye with no comeback reads as lost", func() bool { return !c.Status().LostAt.IsZero() })
	took := time.Since(before)
	s := c.Status()
	if s.LostAt.Before(before) || s.LostAt.After(after) {
		t.Fatalf("LostAt = %v, want the bye (%v … %v)", s.LostAt, before, after)
	}
	if s.LostSeen.Before(s.LostAt.Add(window)) {
		t.Fatalf("LostSeen = %v, want one presence window after the bye (%v)", s.LostSeen, s.LostAt)
	}
	if took < window || took > window+2*time.Second {
		t.Fatalf("read as lost after %v, want about one presence window (%v)", took, window)
	}
	if _, _, lost, _ := rec.get(); len(lost) != 0 {
		t.Fatalf("a bye counted as a dropped link: %v", lost)
	}
}

func TestClient_ByeThenBackWithinTheWindowIsNoOutage(t *testing.T) {
	const window = 300 * time.Millisecond
	h, c, _, _ := byeHarness(t, window)

	h.restart() // a fresh hub on the same address, then bye from the old one
	waitFor(t, "relinked", func() bool { return h.currentHub().Peer("till-2") != nil && c.Linked() })
	time.Sleep(2 * window)
	if s := c.Status(); !s.LostAt.IsZero() || !s.Linked {
		t.Fatalf("a restart that came back read as an outage: %+v", s)
	}
}

func TestClient_ByeThenAnAnswerIsNoOutage(t *testing.T) {
	const window = 300 * time.Millisecond
	h, c, main, _ := byeHarness(t, window)

	// The main till comes back without offering a link (an older build):
	// it answered, so it is reachable.
	main.Store(mainNoLink)
	h.currentHub().Close()
	waitFor(t, "polling", func() bool { return c.Status().Mode == ModePolling })
	time.Sleep(2 * window)
	if s := c.Status(); !s.LostAt.IsZero() {
		t.Fatalf("a main till that answered after its bye read as gone: %+v", s)
	}
}

func TestClient_ByeThenAProbeAnswerIsNoOutageEvenIfTheDialIsRefused(t *testing.T) {
	const window = 300 * time.Millisecond
	h, c, _, _ := byeHarness(t, window)

	// The main till is back and answers its probe, but refuses the link
	// for now (503 while it starts up): it answered, so it is reachable.
	h.mu.Lock()
	h.before = func(w http.ResponseWriter, _ *http.Request) bool {
		http.Error(w, "starting", http.StatusServiceUnavailable)
		return true
	}
	h.mu.Unlock()
	dials := h.dials.Load()
	h.currentHub().Close()
	waitFor(t, "redialled", func() bool { return h.dials.Load() > dials })
	time.Sleep(2 * window)
	if s := c.Status(); !s.LostAt.IsZero() {
		t.Fatalf("a main till that answered its probe after its bye read as gone: %+v", s)
	}
}

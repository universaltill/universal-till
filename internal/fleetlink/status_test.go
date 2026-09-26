package fleetlink

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// ADR-0114 §10 (ut-docs#2742): what the link knows about itself, for the
// replica's connectivity chip and the main till's Tills page.

func TestClient_StatusFollowsTheLinkLifecycle(t *testing.T) {
	cfg := fastConfig()
	h := newSwapHarness(t, cfg)
	tt := &testTarget{}
	var level atomic.Int32 // 0: the main till does not offer the link yet
	c := NewClient(fastClientOptions(cfg, tt, &clientRec{}, &level))
	startClient(t, c)

	// Not a replica: nothing to say.
	time.Sleep(60 * time.Millisecond)
	if s := c.Status(); s.Mode != ModeIdle || s.Linked {
		t.Fatalf("no main till: status = %+v, want idle", s)
	}

	// An older main till (no link on its ping): polling, never linked.
	tt.set(Target{BaseURL: h.srv.URL, Bearer: "good-till-2"})
	c.Redial()
	waitFor(t, "polling mode", func() bool { return c.Status().Mode == ModePolling })

	// It is updated and advertises the link: linked, with its version.
	level.Store(1)
	waitFor(t, "linked mode", func() bool { s := c.Status(); return s.Mode == ModeLinked && s.Linked })
	s := c.Status()
	if s.MainVersion != "v9.9.9" || s.LinkedSince.IsZero() || !s.LostAt.IsZero() {
		t.Fatalf("linked status = %+v, want main version v9.9.9, LinkedSince set, no LostAt", s)
	}

	// The main till restarts: a bye is not an outage.
	h.restart()
	waitFor(t, "relinked", func() bool { return c.Status().Linked && h.currentHub().Peer("till-2") != nil })
	if s := c.Status(); !s.LostAt.IsZero() {
		t.Fatalf("a bye recorded an outage: %+v", s)
	}
}

// The chip must turn within ~15 s of the main till going silent: Linked is
// false the moment no frame arrived for PeerTimeout, and the outage start
// is kept until the link is back.
func TestClient_StatusRecordsWhenAnEstablishedLinkWasLost(t *testing.T) {
	cfg := fastConfig()
	cfg.PingInterval = 20 * time.Millisecond
	cfg.PeerTimeout = 200 * time.Millisecond
	h := newSwapHarness(t, cfg)
	// A main till that says hello and then goes silent.
	var mu sync.Mutex
	var held []*websocket.Conn
	var silent, refuse atomic.Bool
	h.before = func(w http.ResponseWriter, r *http.Request) bool {
		if refuse.Load() {
			http.Error(w, "down", http.StatusBadGateway)
			return true
		}
		if !silent.Load() {
			return false // the real hub
		}
		ws, err := websocket.Accept(w, r, nil)
		if err == nil {
			mu.Lock()
			held = append(held, ws)
			mu.Unlock()
			writeEnv(t, ws, mustMsg(t, "m-1", TypeHello, "", testHello(context.Background(), "till-2")))
		}
		return true
	}
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range held {
			_ = c.CloseNow()
		}
	})
	silent.Store(true)
	tt := &testTarget{}
	tt.set(Target{BaseURL: h.srv.URL, Bearer: "good-till-2"})
	rec := &clientRec{}
	c := NewClient(fastClientOptions(cfg, tt, rec, advertised()))
	startClient(t, c)

	waitFor(t, "hello from the silent main till", func() bool { n, _, _, _ := rec.get(); return n >= 1 })
	// No second link: a redial that got a fresh hello would be real contact
	// and rightly restart the outage later than the one captured below (#2846).
	refuse.Store(true)
	// One snapshot: a second read can land in runLink's defer between
	// setLinked(false) and markLinkLost, and see no outage at all (#2846).
	var s ClientStatus
	waitFor(t, "outage recorded", func() bool { s = c.Status(); return !s.LostAt.IsZero() })
	if s.Linked {
		t.Fatalf("still linked %v after the main till went silent", time.Since(s.LostAt))
	}
	if s.MainVersion != "v9.9.9" {
		t.Fatalf("main version forgotten on loss: %+v", s)
	}
	if s.LostSeen.Before(s.LostAt) {
		t.Fatalf("loss noticed (%v) before the last frame (%v)", s.LostSeen, s.LostAt)
	}
	lost := s.LostAt
	// While the main till stays away (the dial now fails outright), the
	// outage start does not move: the chip's "since" is when the main till
	// was last heard.
	silent.Store(false)
	before := h.dials.Load()
	waitFor(t, "failed redials", func() bool { return h.dials.Load() >= before+3 })
	if got := c.Status().LostAt; !got.Equal(lost) {
		t.Fatalf("outage start moved from %v to %v on failed redials", lost, got)
	}

	// The real main till answers again: the outage is over.
	refuse.Store(false)
	waitFor(t, "relinked to the real main till", func() bool { return c.Status().Linked })
	if s := c.Status(); !s.LostAt.IsZero() {
		t.Fatalf("LostAt kept after the link came back: %+v", s)
	}
}

// Independent review (Fable 5.1): a probe the main till never answered is
// recorded (FailedAt), so a replica restarted while its main till is down
// can show "not reachable" before the pull loop's first tick; an answer of
// any kind — here an older main till without the link — forgets it, and
// forgets a recorded outage too: the main till is reachable again.
func TestClient_StatusRecordsAFailedAttemptAndForgetsItOnAnAnswer(t *testing.T) {
	cfg := fastConfig()
	tt := &testTarget{}
	tt.set(Target{BaseURL: "http://127.0.0.1:1", Bearer: "good-till-2"})
	var down atomic.Bool
	down.Store(true)
	opts := fastClientOptions(cfg, tt, &clientRec{}, advertised())
	opts.Probe = func(context.Context, Target) (int, error) {
		if down.Load() {
			return 0, errors.New("dial tcp: connection refused")
		}
		return 0, nil // answered, but an older main till: no link
	}
	c := NewClient(opts)
	c.markLinkLost(time.Now().Add(-time.Minute)) // an outage from before, still running
	startClient(t, c)

	waitFor(t, "failed attempt recorded", func() bool { return !c.Status().FailedAt.IsZero() })
	if s := c.Status(); s.Mode != ModeConnecting || s.Linked {
		t.Fatalf("status while the main till refuses = %+v, want connecting, not linked", s)
	}

	down.Store(false)
	waitFor(t, "polling after an answer", func() bool { return c.Status().Mode == ModePolling })
	if s := c.Status(); !s.FailedAt.IsZero() || !s.LostAt.IsZero() {
		t.Fatalf("the main till answered, yet status = %+v: want FailedAt and LostAt cleared", s)
	}
}

func TestClient_StatusIsNotLinkedOnceFramesStopEvenBeforeThePeerCloses(t *testing.T) {
	c := NewClient(ClientOptions{Config: Config{PeerTimeout: 12 * time.Second}})
	p := newPeer(c, c.cfg, "r", "main", newFakeConn())
	c.cur.Store(p)
	c.linked.Store(true)
	now := time.Now()
	p.touch(now.Add(-11 * time.Second))
	if s := c.statusAt(now); !s.Linked || !s.LostAt.IsZero() {
		t.Fatalf("a frame 11 s ago is still a live link: %+v", s)
	}
	last := now.Add(-13 * time.Second)
	p.touch(last)
	s := c.statusAt(now)
	if s.Linked {
		t.Fatal("no frame for 13 s must read as not linked (ADR-0114 §4: 12 s)")
	}
	if !s.LostAt.Equal(last) {
		t.Fatalf("LostAt = %v, want the last frame %v", s.LostAt, last)
	}
}

// A wall-clock step — an NTP correction, or a Pi without an RTC setting its
// clock after boot — must not move the watchdog or the client's link-lost
// status: both are measured from the monotonic reading time.Now() carries,
// never the wall clock (ut-docs#2853).
func TestClient_StatusIgnoresAWallClockStep(t *testing.T) {
	c := NewClient(ClientOptions{Config: Config{PeerTimeout: 12 * time.Second}})
	p := newPeer(c, c.cfg, "r", "main", newFakeConn())
	c.cur.Store(p)
	c.linked.Store(true)
	now := time.Now()
	p.touch(now)

	t.Run("forward step right after a frame stays linked", func(t *testing.T) {
		stepped := wallStepped(now, time.Hour)
		if s := c.statusAt(stepped); !s.Linked || !s.LostAt.IsZero() {
			t.Errorf("status after a +1h wall step = %+v, want still linked", s)
		}
		if got := p.sinceFrame(stepped); got >= p.cfg.PeerTimeout {
			t.Fatalf("sinceFrame after a +1h wall step = %v, want < %v (the watchdog would wrongly fire)", got, p.cfg.PeerTimeout)
		}
	})

	t.Run("backward step does not delay detecting a lost link", func(t *testing.T) {
		// 13 s of real (monotonic) time since the last frame, then the wall
		// clock is corrected an hour into the past.
		elapsed := now.Add(13 * time.Second)
		back := wallStepped(elapsed, -time.Hour)
		if s := c.statusAt(back); s.Linked {
			t.Errorf("status 13s on, after a -1h wall step, = %+v, want not linked", s)
		}
		if got := p.sinceFrame(back); got <= p.cfg.PeerTimeout {
			t.Fatalf("sinceFrame 13s on, after a -1h wall step, = %v, want > %v", got, p.cfg.PeerTimeout)
		}
	})
}

// lastFrameAt places the last frame on the CALLER's wall clock — so once
// that clock has stepped forward (NTP, a Pi without an RTC after boot), the
// display value moves with it too, rather than staying pinned to a wall
// timestamp captured before the step.
func TestPeer_LastFrameAtTracksAWallClockStep(t *testing.T) {
	c := NewClient(ClientOptions{Config: Config{PeerTimeout: 12 * time.Second}})
	p := newPeer(c, c.cfg, "r", "main", newFakeConn())
	now := time.Now()
	p.touch(now)

	elapsed := 3 * time.Second
	stepped := wallStepped(now.Add(elapsed), time.Hour)

	got := p.lastFrameAt(stepped)
	if d := got.Round(0).Sub(now.Round(0)); d < 59*time.Minute || d > 61*time.Minute {
		t.Fatalf("lastFrameAt's wall clock moved by %v since the frame, want ~1h (it should track the step)", d)
	}
	// Still the right distance behind the (stepped) caller's clock.
	if d := stepped.Sub(got); d != elapsed {
		t.Fatalf("stepped.Sub(lastFrameAt) = %v, want %v (the real time since the frame)", d, elapsed)
	}
}

// The hub's Peers() snapshot uses lastFrameAt the same way (one now, read
// right before the loop): a live peer's LastFrame is a few moments before
// the snapshot, and stays a monotonically well-formed time.
func TestHub_PeersSnapshotLastFrame(t *testing.T) {
	hub := NewHub(HubOptions{Config: fastConfig(), Hello: testHello})
	defer hub.Close()
	fa, _ := servePeer(t, hub, "till-a")
	fa.next(t)

	before := time.Now()
	peers := hub.Peers()
	after := time.Now()
	if len(peers) != 1 {
		t.Fatalf("peers = %+v, want 1", peers)
	}
	last := peers[0].LastFrame
	if last.IsZero() || last.Before(before.Add(-time.Second)) || last.After(after) {
		t.Fatalf("LastFrame = %v, want a recent time between ~%v and %v", last, before, after)
	}
}

func TestHub_PeersSnapshotIsSortedAndCarriesHelloAndReport(t *testing.T) {
	hub := NewHub(HubOptions{Config: fastConfig(), Hello: testHello})
	defer hub.Close()
	fb, _ := servePeer(t, hub, "till-b")
	fa, _ := servePeer(t, hub, "till-a")
	fb.next(t)
	fa.next(t)
	fa.send(t, mustMsg(t, "r-1", TypeHello, "", Hello{TillID: "till-a", Role: "replica", Version: "v1.2.0"}))
	fa.send(t, mustMsg(t, "r-2", TypeReport, "", Report{Version: "v1.2.0", UpdateState: "idle", PushQueueDepth: 4, TLSPinned: true}))
	waitFor(t, "report", func() bool { _, ok := hub.Report("till-a"); return ok })

	peers := hub.Peers()
	if len(peers) != 2 || peers[0].TillID != "till-a" || peers[1].TillID != "till-b" {
		t.Fatalf("peers = %+v, want till-a then till-b", peers)
	}
	a := peers[0]
	if !a.HasHello || a.Hello.Role != "replica" || a.Hello.Version != "v1.2.0" ||
		!a.HasReport || a.Report.PushQueueDepth != 4 || !a.Report.TLSPinned || a.LastFrame.IsZero() {
		t.Fatalf("till-a snapshot = %+v", a)
	}
	if b := peers[1]; b.HasHello || b.HasReport {
		t.Fatalf("till-b said nothing yet: %+v", b)
	}
}

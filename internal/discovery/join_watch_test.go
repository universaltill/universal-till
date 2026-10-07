package discovery

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
)

const testSelfID = "33333333-3333-4333-8333-333333333333"

type joinFixture struct {
	w        *JoinWatch
	settings *data.SettingsRepo
	browses  atomic.Int32
	now      time.Time
	cands    []Candidate
	err      error
}

// newJoinFixture is a standalone till (no sync.primary_url) whose own LAN
// discovery id is testSelfID, browsing a fake LAN that answers f.cands.
func newJoinFixture(t *testing.T, cands ...Candidate) *joinFixture {
	t.Helper()
	settings := openFastFileSettings(t)
	if err := settings.Set(context.Background(), TillIDSettingKey, testSelfID); err != nil {
		t.Fatal(err)
	}
	f := &joinFixture{settings: settings, now: time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC), cands: cands}
	f.w = NewJoinWatch(settings, func(context.Context, time.Duration) ([]Candidate, error) {
		f.browses.Add(1)
		return f.cands, f.err
	})
	f.w.now = func() time.Time { return f.now }
	return f
}

func (f *joinFixture) set(t *testing.T, key, value string) {
	t.Helper()
	if err := f.settings.Set(context.Background(), key, value); err != nil {
		t.Fatal(err)
	}
}

var shopMain = Candidate{Name: "test-2721-main", TillID: "44444444-4444-4444-8444-444444444444", BaseURL: "http://192.168.1.20:37673"}

// The card (ut-docs#2721): a standalone till finds the shop's main till on
// launch — the very first tick browses, no waiting out an interval.
func TestJoinWatch_FirstTickBrowsesAndCachesCandidate(t *testing.T) {
	f := newJoinFixture(t, shopMain)
	if _, ok := f.w.Candidate(); ok {
		t.Fatal("no candidate expected before any browse")
	}
	f.w.Tick(context.Background())
	if got := f.browses.Load(); got != 1 {
		t.Fatalf("browses = %d, want 1 on the first tick", got)
	}
	c, ok := f.w.Candidate()
	if !ok || c != shopMain {
		t.Fatalf("Candidate() = %+v, %v; want %+v", c, ok, shopMain)
	}
}

// This till advertises itself too (ut-docs#1261): its own advertisement
// must never be offered as the main till to join — the next one is.
func TestJoinWatch_SkipsOwnAdvertisement(t *testing.T) {
	self := Candidate{Name: "Me", TillID: testSelfID, BaseURL: "http://192.168.1.21:37673"}
	f := newJoinFixture(t, self, shopMain)
	f.w.Tick(context.Background())
	c, ok := f.w.Candidate()
	if !ok || c != shopMain {
		t.Fatalf("Candidate() = %+v, %v; want %+v (self filtered)", c, ok, shopMain)
	}
}

func TestJoinWatch_OnlySelfOnLANMeansNoCandidate(t *testing.T) {
	f := newJoinFixture(t, Candidate{Name: "Me", TillID: testSelfID, BaseURL: "http://192.168.1.21:37673"})
	f.w.Tick(context.Background())
	if c, ok := f.w.Candidate(); ok {
		t.Fatalf("Candidate() = %+v, want none — only this till is on the LAN", c)
	}
}

// Rate limit: ticks come every 30s, the LAN is browsed at most once per
// MinBrowseInterval.
func TestJoinWatch_BrowseIsRateLimited(t *testing.T) {
	f := newJoinFixture(t, shopMain)
	ctx := context.Background()
	f.w.Tick(ctx)
	for i := 0; i < 5; i++ {
		f.now = f.now.Add(30 * time.Second)
		f.w.Tick(ctx)
	}
	if got := f.browses.Load(); got != 1 {
		t.Fatalf("browses = %d within %s, want 1", got, MinBrowseInterval)
	}
	f.now = f.now.Add(MinBrowseInterval)
	f.w.Tick(ctx)
	if got := f.browses.Load(); got != 2 {
		t.Fatalf("browses = %d after the interval passed, want 2", got)
	}
}

// A main till that went away stops being offered at the next browse.
func TestJoinWatch_LaterEmptyBrowseClearsCandidate(t *testing.T) {
	f := newJoinFixture(t, shopMain)
	ctx := context.Background()
	f.w.Tick(ctx)
	f.cands = nil
	f.now = f.now.Add(MinBrowseInterval)
	f.w.Tick(ctx)
	if c, ok := f.w.Candidate(); ok {
		t.Fatalf("Candidate() = %+v, want none after an empty browse", c)
	}
}

func TestJoinWatch_BrowseErrorMeansNoCandidate(t *testing.T) {
	f := newJoinFixture(t, shopMain)
	f.err = errors.New("multicast unavailable")
	f.w.Tick(context.Background())
	if c, ok := f.w.Candidate(); ok {
		t.Fatalf("Candidate() = %+v, want none when the browse failed", c)
	}
}

// A replica already has its main till: never browse, never offer.
func TestJoinWatch_ReplicaNeverBrowses(t *testing.T) {
	f := newJoinFixture(t, shopMain)
	f.set(t, "sync.primary_url", "http://192.168.1.20:37673")
	f.w.Tick(context.Background())
	if got := f.browses.Load(); got != 0 {
		t.Fatalf("browses = %d on a replica, want 0", got)
	}
	if _, ok := f.w.Candidate(); ok {
		t.Fatal("a replica must never have a join candidate")
	}
}

// A till that became a replica since the last browse drops its candidate.
func TestJoinWatch_BecomingReplicaClearsCandidate(t *testing.T) {
	f := newJoinFixture(t, shopMain)
	ctx := context.Background()
	f.w.Tick(ctx)
	f.set(t, "sync.primary_url", shopMain.BaseURL)
	f.w.Tick(ctx)
	if _, ok := f.w.Candidate(); ok {
		t.Fatal("candidate must clear once this till is a replica")
	}
}

// Dismissed for good (sync.join_banner_dismissed): stop browsing the LAN
// on this till's behalf at all.
func TestJoinWatch_DismissedNeverBrowses(t *testing.T) {
	f := newJoinFixture(t, shopMain)
	f.set(t, JoinBannerDismissedSettingKey, "1")
	f.w.Tick(context.Background())
	if got := f.browses.Load(); got != 0 {
		t.Fatalf("browses = %d after dismissal, want 0", got)
	}
}

// A nil watch (tests that don't wire one, demo mode) is safe to read.
func TestJoinWatch_NilSafe(t *testing.T) {
	var w *JoinWatch
	w.Tick(context.Background())
	if _, ok := w.Candidate(); ok {
		t.Fatal("nil watch has no candidate")
	}
}

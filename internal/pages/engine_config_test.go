package pages

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// blockingAsker lets the first (setup) ask through, then parks every later
// AskChargePolicy call until released.
type blockingAsker struct {
	mu        sync.Mutex
	calls     int
	startOnce sync.Once
	relOnce   sync.Once
	started   chan struct{}
	release   chan struct{}
}

func newBlockingAsker() *blockingAsker {
	return &blockingAsker{started: make(chan struct{}), release: make(chan struct{})}
}

func (a *blockingAsker) releaseNow() { a.relOnce.Do(func() { close(a.release) }) }

func (a *blockingAsker) AskChargePolicy() (pos.ChargePolicy, bool) {
	a.mu.Lock()
	a.calls++
	setup := a.calls == 1
	a.mu.Unlock()
	if setup {
		return pos.ChargePolicy{}, false
	}
	a.startOnce.Do(func() { close(a.started) })
	<-a.release
	return pos.ChargePolicy{}, false
}

// ut-docs#3085: two concurrent applyEngineConfig calls must not interleave
// across Engine / KioskEngine / SelfOrderSessions. While call 1 is stuck
// inside the kiosk engine, call 2 must not touch the cashier Engine.
func TestEngineConfig_ConcurrentAppliesDoNotInterleave(t *testing.T) {
	_, _, d := newFullAuthDeps(t)
	ctx := t.Context()
	def := pos.Config{TaxRateBasisPoints: 2000}
	d.Engine = pos.NewServiceWithResolver(def, nil)
	kiosk := pos.NewServiceWithResolver(def, nil)
	sessions := pos.NewSessionBasketManager(func() *pos.Service { return pos.NewServiceWithResolver(def, nil) })
	_, sess, _ := sessions.Create()
	d.KioskEngine, d.SelfOrderSessions = kiosk, sessions

	asker := newBlockingAsker()
	t.Cleanup(asker.releaseNow)
	kiosk.SetChargePolicyAsker(asker)

	setTaxRate(t, d, 10)
	done1 := make(chan struct{})
	go func() { defer close(done1); applyEngineConfig(ctx, d) }()
	select {
	case <-asker.started:
	case <-time.After(2 * time.Second):
		t.Fatal("apply #1 never reached the kiosk engine's charge-policy ask")
	}

	setTaxRate(t, d, 25)
	done2 := make(chan struct{})
	go func() { defer close(done2); applyEngineConfig(ctx, d) }()

	// Call 2 must be parked behind call 1's whole batch.
	select {
	case <-done2:
		t.Fatal("apply #2 returned while apply #1 was still stuck — batches are not serialized")
	case <-time.After(200 * time.Millisecond):
	}
	if got := d.Engine.Config().TaxRateBasisPoints; got == 2500 {
		t.Fatalf("Engine already has 25%% while call 1 is still mid-apply: batches interleaved (kiosk=%+v)", kiosk.Config())
	}

	asker.releaseNow()
	for i, ch := range []chan struct{}{done1, done2} {
		select {
		case <-ch:
		case <-time.After(2 * time.Second):
			t.Fatalf("apply call %d never returned after release", i+1)
		}
	}
	assertEnginesOnTaxRate(t, d, sess, 2500)
}

// ut-docs#3245: a settings save used to build its pos.Config from state it
// read before taking engineConfigMu, so an older save whose apply ran late
// pushed its stale rate over a newer save's, leaving every engine on a rate
// the DB no longer held. The config is now read inside the lock: whichever
// apply runs last applies what the DB holds.
func TestEngineConfig_LateOlderSaveEndsOnDBConfig(t *testing.T) {
	_, _, d := newFullAuthDeps(t)
	ctx := t.Context()
	d.KioskEngine = pos.NewServiceWithResolver(pos.Config{}, stubResolver{})
	d.SelfOrderSessions = pos.NewSessionBasketManager(func() *pos.Service {
		return pos.NewServiceWithResolver(d.KioskEngine.Config(), stubResolver{})
	})
	_, sess, _ := d.SelfOrderSessions.Create()

	setTaxRate(t, d, 10) // save H1 writes 10%...
	setTaxRate(t, d, 25) // ...save H2 writes 25% and applies first
	applyEngineConfig(ctx, d)
	applyEngineConfig(ctx, d) // H1's apply, delayed past H2's

	assertEnginesOnTaxRate(t, d, sess, 2500)
}

// ut-docs#3245: newRederiveSettings compared only the cashier Engine's
// config, outside the lock, so a KioskEngine left behind by a racing apply
// was never caught up while the Engine happened to match. The compare now
// runs inside the lock over both engines.
func TestRederiveSettings_CatchesUpKioskWhenEngineAlreadyMatches(t *testing.T) {
	_, _, d := newFullAuthDeps(t)
	d.KioskEngine = pos.NewServiceWithResolver(pos.Config{}, stubResolver{})
	d.SelfOrderSessions = pos.NewSessionBasketManager(func() *pos.Service {
		return pos.NewServiceWithResolver(d.KioskEngine.Config(), stubResolver{})
	})
	_, sess, _ := d.SelfOrderSessions.Create()
	i18n, err := config.NewI18n("web/locales", "en")
	if err != nil {
		t.Fatal(err)
	}

	setTaxRate(t, d, 25)
	want := engineConfigFor(common.LoadState(t.Context(), d.Settings, d.Cfg))
	d.Engine.SetConfig(want) // the cashier engine is already current...
	newRederiveSettings(d, true, i18n)(context.Background())

	assertEnginesOnTaxRate(t, d, sess, 2500) // ...the kiosk and session must follow
}

func setTaxRate(t *testing.T, d *common.Deps, pct int) {
	t.Helper()
	if err := d.Settings.Set(t.Context(), common.KeyTaxRate, strconv.Itoa(pct)); err != nil {
		t.Fatal(err)
	}
}

func assertEnginesOnTaxRate(t *testing.T, d *common.Deps, sess *pos.Service, wantBP int) {
	t.Helper()
	for name, got := range map[string]int{
		"Engine":      d.Engine.Config().TaxRateBasisPoints,
		"KioskEngine": d.KioskEngine.Config().TaxRateBasisPoints,
		"session":     sess.Config().TaxRateBasisPoints,
	} {
		if got != wantBP {
			t.Errorf("%s tax rate = %d bp, want %d bp (the DB's)", name, got, wantBP)
		}
	}
}

// A settings save's request context is often cancelled by then (the client
// got its response and went away); that must not turn LoadState's reads
// into failures that fall back to default rates.
func TestEngineConfig_CancelledContextStillAppliesDBConfig(t *testing.T) {
	_, _, d := newFullAuthDeps(t)
	setTaxRate(t, d, 25)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	applyEngineConfig(ctx, d)
	if got := d.Engine.Config().TaxRateBasisPoints; got != 2500 {
		t.Fatalf("Engine tax rate = %d bp after an apply with a cancelled context, want 2500 bp", got)
	}
}

// Review of ut-docs#3245: LoadState turns a failed read into the default,
// and the engines now take their config from it — a read failure right
// after a save must leave them on their last good config, not push the
// default tax rate to every live basket.
func TestEngineConfig_FailedReadKeepsLastGoodConfig(t *testing.T) {
	_, _, d := newFullAuthDeps(t)
	setTaxRate(t, d, 25)
	applyEngineConfig(t.Context(), d)
	if err := d.Db.Close(); err != nil {
		t.Fatal(err)
	}
	applyEngineConfig(t.Context(), d)
	if got := d.Engine.Config().TaxRateBasisPoints; got != 2500 {
		t.Fatalf("Engine tax rate = %d bp after an apply whose settings read failed, want the last good 2500 bp", got)
	}
}

package pages

import (
	"sync"
	"testing"
	"time"

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
	def := pos.Config{TaxRateBasisPoints: 2000}
	engine := pos.NewServiceWithResolver(def, nil)
	kiosk := pos.NewServiceWithResolver(def, nil)
	sessions := pos.NewSessionBasketManager(func() *pos.Service { return pos.NewServiceWithResolver(def, nil) })
	_, sess, _ := sessions.Create()
	d := &common.Deps{Engine: engine, KioskEngine: kiosk, SelfOrderSessions: sessions}

	asker := newBlockingAsker()
	t.Cleanup(asker.releaseNow)
	kiosk.SetChargePolicyAsker(asker)

	cfg1 := pos.Config{TaxRateBasisPoints: 1000}
	cfg2 := pos.Config{TaxRateBasisPoints: 2500}

	done1 := make(chan struct{})
	go func() { defer close(done1); applyEngineConfig(d, cfg1) }()
	select {
	case <-asker.started:
	case <-time.After(2 * time.Second):
		t.Fatal("apply(cfg1) never reached the kiosk engine's charge-policy ask")
	}

	done2 := make(chan struct{})
	go func() { defer close(done2); applyEngineConfig(d, cfg2) }()

	// Call 2 must be parked behind call 1's whole batch.
	select {
	case <-done2:
		t.Fatal("apply(cfg2) returned while apply(cfg1) was still stuck — batches are not serialized")
	case <-time.After(200 * time.Millisecond):
	}
	if got := engine.Config(); got == cfg2 {
		t.Fatalf("Engine already has cfg2 while call 1 is still mid-apply: batches interleaved (engine=%+v kiosk=%+v)", got, kiosk.Config())
	}

	asker.releaseNow()
	for i, ch := range []chan struct{}{done1, done2} {
		select {
		case <-ch:
		case <-time.After(2 * time.Second):
			t.Fatalf("apply call %d never returned after release", i+1)
		}
	}

	if got := engine.Config(); got != cfg2 {
		t.Errorf("Engine = %+v, want %+v", got, cfg2)
	}
	if got := kiosk.Config(); got != cfg2 {
		t.Errorf("KioskEngine = %+v, want %+v", got, cfg2)
	}
	if got := sess.Config(); got != cfg2 {
		t.Errorf("session = %+v, want %+v", got, cfg2)
	}
}

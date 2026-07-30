package common

import (
	"context"
	"sync"
	"testing"
)

func TestDeps_CurrentStateReturnsCopy(t *testing.T) {
	d := &Deps{State: RuntimeState{Theme: "dark"}}

	got := d.CurrentState()
	if got.Theme != "dark" {
		t.Fatalf("CurrentState().Theme = %q, want %q", got.Theme, "dark")
	}

	// Mutating the returned copy must not affect Deps' own state.
	got.Theme = "light"
	if d.State.Theme != "dark" {
		t.Fatalf("Deps.State.Theme = %q after mutating a CurrentState() copy, want unchanged %q", d.State.Theme, "dark")
	}
}

func TestDeps_UpdateStateAppliesAndReturns(t *testing.T) {
	d := &Deps{State: RuntimeState{Theme: "dark"}}

	got := d.UpdateState(func(s *RuntimeState) { s.Theme = "light" })

	if got.Theme != "light" {
		t.Fatalf("UpdateState return = %q, want %q", got.Theme, "light")
	}
	if d.State.Theme != "light" {
		t.Fatalf("Deps.State.Theme = %q after UpdateState, want %q", d.State.Theme, "light")
	}
}

// StateMu must actually serialize concurrent readers/writers — this is the
// exact concurrency guarantee the field's own doc comment promises
// ("settings handlers replace fields while every request renders from
// them"). Run under -race to prove it.
func TestDeps_StateMuSerializesConcurrentAccess(t *testing.T) {
	d := &Deps{}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func(n int) {
			defer wg.Done()
			d.UpdateState(func(s *RuntimeState) { s.TaxRatePct = n })
		}(i)
		go func() {
			defer wg.Done()
			_ = d.CurrentState()
		}()
	}
	wg.Wait()
}

func TestDeps_SyncPrimaryURL_NilSettings(t *testing.T) {
	d := &Deps{}
	if got := d.SyncPrimaryURL(context.Background()); got != "" {
		t.Fatalf("SyncPrimaryURL with nil Settings = %q, want empty", got)
	}
}

func TestDeps_SyncPrimaryURL_TrimsAndReturnsValue(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	if err := store.Set(ctx, "sync.primary_url", "  https://primary.local  "); err != nil {
		t.Fatalf("seed: %v", err)
	}
	d := &Deps{Settings: store}

	got := d.SyncPrimaryURL(ctx)
	if got != "https://primary.local" {
		t.Fatalf("SyncPrimaryURL = %q, want trimmed %q", got, "https://primary.local")
	}
}

func TestDeps_SyncPrimaryURL_EmptyWhenUnset(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	d := &Deps{Settings: store}

	if got := d.SyncPrimaryURL(ctx); got != "" {
		t.Fatalf("SyncPrimaryURL with unset key = %q, want empty", got)
	}
}

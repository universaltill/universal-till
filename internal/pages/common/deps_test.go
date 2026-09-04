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

// ut-docs#1555 review finding F1: WindowModeChanged/LaunchOnStartupChanged
// are a PER-SAVE intent signal for SaveState, never a property of the
// cached RuntimeState. If either SetState or UpdateState let a `true`
// value ride into d.State, the very next unrelated save would inherit it
// from d.CurrentState(), skip SaveState's out-of-band re-read, and
// silently reinstate the exact clobber ut-docs#1555's fix exists to
// prevent — permanently, for the rest of the process's life, after the
// FIRST deliberate window-mode/launch-on-startup change.
func TestDeps_SetStateClearsWindowAndLaunchChangedFlags(t *testing.T) {
	d := &Deps{}
	d.SetState(RuntimeState{WindowMode: "kiosk", WindowModeChanged: true, LaunchOnStartup: true, LaunchOnStartupChanged: true})

	got := d.CurrentState()
	if got.WindowModeChanged {
		t.Fatal("CurrentState().WindowModeChanged = true after SetState, want cleared — it must not survive into cached state")
	}
	if got.LaunchOnStartupChanged {
		t.Fatal("CurrentState().LaunchOnStartupChanged = true after SetState, want cleared — it must not survive into cached state")
	}
	// The actual values these flags authorized must still have taken
	// effect — clearing the flag is not the same as discarding the change.
	if got.WindowMode != "kiosk" {
		t.Fatalf("WindowMode = %q after SetState, want %q (the deliberate change itself must still land)", got.WindowMode, "kiosk")
	}
	if !got.LaunchOnStartup {
		t.Fatal("LaunchOnStartup = false after SetState, want true (the deliberate change itself must still land)")
	}
}

func TestDeps_UpdateStateClearsWindowAndLaunchChangedFlags(t *testing.T) {
	d := &Deps{}
	d.UpdateState(func(s *RuntimeState) {
		s.WindowModeChanged = true
		s.LaunchOnStartupChanged = true
	})

	got := d.CurrentState()
	if got.WindowModeChanged || got.LaunchOnStartupChanged {
		t.Fatalf("*Changed flags survived UpdateState: WindowModeChanged=%v LaunchOnStartupChanged=%v, want both cleared", got.WindowModeChanged, got.LaunchOnStartupChanged)
	}
}

// End-to-end reproduction of the F1 failure sequence exactly as the
// reviewer found it, using the real settings_page.go handler shape (not
// just the SetState unit above): a deliberate window-mode change, followed
// by an out-of-band write (the provisioning CLI, or a raw settings
// upsert), followed by a completely unrelated save — the out-of-band
// value must survive that third save.
func TestSaveState_DeliberateChangeDoesNotStickyBlockLaterOutOfBandProtection(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	d := &Deps{Settings: store}

	// 1. Operator deliberately sets window mode via Settings → Display —
	// the real handler shape: read current state, set the field + its
	// Changed flag, save, then cache via SetState (settings_page.go's
	// window-mode and launch-on-startup handlers both do exactly this).
	st := d.CurrentState()
	st.WindowMode = "maximized"
	st.WindowModeChanged = true
	st.LaunchOnStartup = true
	st.LaunchOnStartupChanged = true
	if err := SaveState(ctx, store, st); err != nil {
		t.Fatalf("SaveState (deliberate change): %v", err)
	}
	d.SetState(st)

	// 2. Out-of-band write: provisioning (or a raw settings upsert) sets
	// window_mode straight in the DB, bypassing SaveState/d.State entirely.
	if err := store.Set(ctx, KeyWindowMode, "kiosk"); err != nil {
		t.Fatalf("seed out-of-band KeyWindowMode: %v", err)
	}
	if err := store.Set(ctx, KeyLaunchOnStartup, "false"); err != nil {
		t.Fatalf("seed out-of-band KeyLaunchOnStartup: %v", err)
	}

	// 3. A completely unrelated save (e.g. the theme card) — if d.State
	// still carries WindowModeChanged/LaunchOnStartupChanged=true from
	// step 1, this call skips the out-of-band re-read and clobbers step
	// 2's values right back.
	unrelated := d.CurrentState()
	unrelated.Theme = "light"
	if err := SaveState(ctx, store, unrelated); err != nil {
		t.Fatalf("SaveState (unrelated save): %v", err)
	}

	if raw, _, _ := store.Get(ctx, KeyWindowMode); raw != "kiosk" {
		t.Fatalf("stored %s = %q after an unrelated save two steps after a deliberate change, want the out-of-band %q to survive", KeyWindowMode, raw, "kiosk")
	}
	if raw, _, _ := store.Get(ctx, KeyLaunchOnStartup); raw != "false" {
		t.Fatalf("stored %s = %q after an unrelated save two steps after a deliberate change, want the out-of-band %q to survive", KeyLaunchOnStartup, raw, "false")
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

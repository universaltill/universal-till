package pages

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// TestRederiveSettings_PushesWindowModeOntoShellChannel (review of
// ut-docs#1039, finding 9 — closes ut-docs#1058): the replica drift loop
// and cloud set_setting directives re-derive RuntimeState, WindowMode
// included, but used to leave the live shell channel untouched — a
// directive setting kiosk left the Settings page showing kiosk selected
// with a shell attached and no warning, while the window stayed put until
// the next restart (at which point the finding-7 restart re-seed fired).
// The re-derive must publish the freshly-loaded mode to the channel the
// shell actually polls.
func TestRederiveSettings_PushesWindowModeOntoShellChannel(t *testing.T) {
	_, _, d := newFullAuthDeps(t)
	// The kiosk engine drift branch needs the second engine instance too.
	d.KioskEngine = pos.NewServiceWithResolver(pos.Config{}, stubResolver{})
	d.WindowCtl = common.NewShellPollWindowController(d.Shell, nil)

	i18n, err := config.NewI18n("web/locales", "en")
	if err != nil {
		t.Fatal(err)
	}
	rederive := newRederiveSettings(d, true, i18n)

	// A cloud directive / replica drift writes the setting straight into
	// the store — no settings handler, no ApplyMode call.
	if err := d.Settings.Set(t.Context(), common.KeyWindowMode, "kiosk"); err != nil {
		t.Fatal(err)
	}
	rederive(context.Background())

	if got := d.CurrentState().WindowMode; got != "kiosk" {
		t.Fatalf("RuntimeState.WindowMode after rederive = %q, want kiosk", got)
	}
	if mode, _ := d.Shell.Snapshot(); mode != "kiosk" {
		t.Fatalf("shell channel mode after rederive = %q, want kiosk — the drift never reached the channel the shell polls (ut-docs#1058)", mode)
	}

	// And back down again: the directive flips it off, the live window
	// must follow without waiting for a restart.
	if err := d.Settings.Set(t.Context(), common.KeyWindowMode, "normal"); err != nil {
		t.Fatal(err)
	}
	rederive(context.Background())
	if mode, _ := d.Shell.Snapshot(); mode != "normal" {
		t.Fatalf("shell channel mode after second rederive = %q, want normal", mode)
	}
}

// TestRederiveSettings_PublishesSelfOrderMode (review of ut-docs#2099,
// finding B1): display.mode is deliberately NOT part of RuntimeState (see
// pages.Init's own boot-time InitSelfOrderMode call), so it got no free
// ride from this function's `*s = st` and was left stale until the next
// process restart after a cloud set_setting directive (ADR-0018) or replica
// drift — exactly the kiosk-containment flag coding-standards.md §10
// depends on to withhold lock/status/exit-to-OS from a device an admin just
// declared customer-facing (or restore them on a device taken out of kiosk
// mode). Same shape as the WindowMode test above.
func TestRederiveSettings_PublishesSelfOrderMode(t *testing.T) {
	_, _, d := newFullAuthDeps(t)
	d.KioskEngine = pos.NewServiceWithResolver(pos.Config{}, stubResolver{})

	i18n, err := config.NewI18n("web/locales", "en")
	if err != nil {
		t.Fatal(err)
	}
	rederive := newRederiveSettings(d, true, i18n)
	selforder := func() bool { return httpx.FuncsFor("en")["selforder"].(func() bool)() }

	httpx.InitSelfOrderMode(false)
	defer httpx.InitSelfOrderMode(false)

	// A cloud directive / replica drift writes display.mode straight into
	// the store — no settings handler, no InitSelfOrderMode call of its own.
	if err := d.Settings.Set(t.Context(), "display.mode", "self_order"); err != nil {
		t.Fatal(err)
	}
	rederive(context.Background())
	if !selforder() {
		t.Fatal("selforder template func = false after rederive with display.mode=self_order — the flag went stale, leaving lock/status/exit-to-OS rendered to a device just declared customer-facing")
	}

	// And back down again: taken out of kiosk mode, the till must regain
	// the affordance without waiting for a restart.
	if err := d.Settings.Set(t.Context(), "display.mode", "register"); err != nil {
		t.Fatal(err)
	}
	rederive(context.Background())
	if selforder() {
		t.Fatal("selforder template func = true after rederive with display.mode=register — an ordinary register is wrongly withholding lock/status/exit-to-OS")
	}
}

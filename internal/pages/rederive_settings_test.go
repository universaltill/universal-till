package pages

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
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

package pages

import (
	"context"
	"errors"

	"github.com/universaltill/universal-till/internal/pages/common"
)

// cloudKioskUnlock is the kiosk_unlock hook (ut-docs#3466, ADR-0142 D3):
// the shop owner (or staff admin), with step-up, asked from the cloud to
// release THIS till's self-order kiosk pin — the remote way out when the
// page the kiosk shows is broken and its own manager-PIN escape cannot
// render. cloudsync.Tick only hands it a directive addressed to this
// till's own device id.
//
//  1. The till decides. Its own display.mode must be self_order, else the
//     result is failed / not_self_order and nothing changes. This is the
//     security boundary: the cloud's own self_order check reads the last
//     check-in and is only a UX guard. It checks the configured device
//     profile, not the page currently loaded. A read error refuses too
//     (inSelfOrderMode).
//  2. WindowCtl.ReleaseKiosk does the platform work, never through the
//     WebView. Its refusals become the result reasons my. shows:
//     ErrNoOSDesktop → kiosk_appliance (the Pi cage appliance, the same
//     answer exit-to-OS gives), ErrKioskReleaseNotSupported →
//     not_supported (desktop shells; also a till with no controller), and
//     ErrNoKioskShell → no_shell (Android, Activity not in the
//     foreground). Any other error — the native bridge's own failure —
//     passes through as it is.
//  3. Every outcome writes a till audit row kiosk_unlock with actor
//     system (audit_log.actor_id references users, and a cloud directive
//     has no till operator — ut-docs#1676), carrying the directive id and
//     its created_by subject; a failure adds its reason.
//
// The till does not check the directive's age: the cloud expires one older
// than 15 minutes on serve (ADR-0142 D1), and this till's clock may be
// wrong.
func cloudKioskUnlock(ctx context.Context, d *common.Deps, directiveID, createdBy string) (string, error) {
	err := releaseKioskForDirective(ctx, d)
	payload := map[string]any{
		"directive_id": directiveID,
		"created_by":   createdBy,
		"via":          "cloud",
		"status":       "applied",
	}
	if err != nil {
		payload["status"] = "failed"
		payload["reason"] = err.Error()
	}
	entityID := directiveID
	if entityID == "" {
		entityID = "-"
	}
	auditCloudDirective(ctx, d, "kiosk", entityID, "kiosk_unlock", payload)
	if err != nil {
		return "", err
	}
	return "kiosk released", nil
}

// releaseKioskForDirective is cloudKioskUnlock's decision and platform call,
// returning the result reason as the error.
func releaseKioskForDirective(ctx context.Context, d *common.Deps) error {
	if !inSelfOrderMode(ctx, d) {
		return errors.New("not_self_order")
	}
	wc := d.WindowCtl
	if wc == nil {
		wc = common.NoopWindowController{}
	}
	err := wc.ReleaseKiosk()
	switch {
	case err == nil:
		return nil
	case errors.Is(err, common.ErrNoOSDesktop):
		return errors.New("kiosk_appliance")
	case errors.Is(err, common.ErrKioskReleaseNotSupported):
		return errors.New("not_supported")
	case errors.Is(err, common.ErrNoKioskShell):
		return errors.New("no_shell")
	}
	return err
}

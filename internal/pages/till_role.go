package pages

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// A joined till's role (ut-docs#2781). A manager picks it on the main till
// before the till is created — on the pairing-code card or the
// approve-to-pair card — and can change it later on the Tills page.
// tills.role (migration 067) on the main till is the single source of
// truth; each joined till keeps its own copy as the per-till setting
// sync.till_role (db.TillRoleSettingsKey), learns a change after its next
// admin pull (reconcileOwnTillRole) and reports it in its link hello
// (linkHelloRole).
//
// Scope until ut-docs#1154: a satellite is exactly ADR-0020's counter-pay
// kiosk — order capture only, pay at the counter; it never records a sale
// or takes a card payment alone (ADR-0086). The only behaviour this file
// gives the role is the device-profile gate: a satellite runs as the
// self-order kiosk and nothing else.

// errSatelliteDisplayMode refuses a register or back-office profile on a
// satellite till.
var errSatelliteDisplayMode = errors.New("a satellite till can only run as the self-order kiosk")

// parseTillRole validates a role posted by a form: blank means the default
// (additional); anything else must be a role tills.role accepts.
func parseTillRole(raw string) (string, bool) {
	role := strings.TrimSpace(raw)
	if role == "" {
		return data.TillRoleAdditional, true
	}
	return role, data.ValidTillRole(role)
}

// ownTillRole is THIS till's role: satellite only when its own
// sync.till_role says so, otherwise additional. A main or standalone till
// has no sync.till_role and is never a satellite.
func ownTillRole(ctx context.Context, d *common.Deps) string {
	if d == nil || d.Settings == nil {
		return data.TillRoleAdditional
	}
	v, _, err := d.Settings.Get(ctx, db.TillRoleSettingsKey)
	if err == nil && strings.TrimSpace(v) == data.TillRoleSatellite {
		return data.TillRoleSatellite
	}
	return data.TillRoleAdditional
}

// tillIsSatellite reports whether this till's own role is satellite.
func tillIsSatellite(ctx context.Context, d *common.Deps) bool {
	return ownTillRole(ctx, d) == data.TillRoleSatellite
}

// displayModeAllowedForRole is the device-profile gate: a satellite may only
// run as the self-order kiosk. rawMode is a form value ("register",
// "backoffice", "self_order") or a stored one ("" is register).
func displayModeAllowedForRole(role, rawMode string) bool {
	return role != data.TillRoleSatellite || rawMode == "self_order"
}

// linkHelloRole is the role this till reports in its link hello: what the
// main till recorded for it, never invented here (ut-docs#2781). Anything
// that is not a satellite links as a plain replica, as before.
func linkHelloRole(ctx context.Context, d *common.Deps) string {
	if tillIsSatellite(ctx, d) {
		return "satellite"
	}
	return "replica"
}

// setOwnTillRole writes this till's own role, a per-till sync.* setting
// like every other local identity write (never synced, never written
// through to the main till).
func setOwnTillRole(ctx context.Context, d *common.Deps, role string) error {
	return d.Settings.Set(ctx, db.TillRoleSettingsKey, role)
}

// reconcileOwnTillRole runs after every admin pull that reached the main
// till: it reads this till's own row in the freshly synced roster and, when
// the main till's role for it has CHANGED since this till last saw it
// (sync.till_role_main), adopts it as sync.till_role. Following a change —
// not re-copying the value every tick — is what lets a joined till's own
// confirmed "make this a satellite" (the Settings display-mode switch)
// stand until a manager next sets the role on the main till's Tills page.
//
// Then it re-applies the device-profile gate: a satellite left on the
// register or back-office profile (by an older build, a restore, or any
// other path that skipped the gate) is forced to the self-order kiosk, with
// a log line and an audit row, rather than left inconsistent.
func reconcileOwnTillRole(ctx context.Context, d *common.Deps) {
	if d == nil || d.Settings == nil || d.Db == nil {
		return
	}
	get := func(k string) string {
		v, _, _ := d.Settings.Get(ctx, k)
		return strings.TrimSpace(v)
	}
	tillID := get("sync.till_id")
	if tillID == "" {
		return
	}
	posRepo := data.NewPOSRepo(d.Db)
	now := func() string { return time.Now().UTC().Format(time.RFC3339) }
	mainRole, found, err := data.NewTillsRepo(d.Db).RoleByID(ctx, tillID)
	if err != nil {
		logging.L().Warnf("till role: read this till's roster row: %v", err)
	} else if found && mainRole != get(db.TillRoleMainSettingsKey) {
		prev := ownTillRole(ctx, d)
		if err := d.Settings.Set(ctx, db.TillRoleMainSettingsKey, mainRole); err != nil {
			logging.L().Warnf("till role: record the main till's role: %v", err)
			return
		}
		if mainRole != prev {
			if err := setOwnTillRole(ctx, d, mainRole); err != nil {
				logging.L().Warnf("till role: apply %s: %v", mainRole, err)
				return
			}
			logging.L().Infof("till role: the main till set this till's role to %s (was %s)", mainRole, prev)
			_ = posRepo.InsertAudit(ctx, nil, "system", "till", tillID, "till_role_changed",
				map[string]any{"from": prev, "to": mainRole, "source": "main_till"}, now(), "")
		}
	}
	if !tillIsSatellite(ctx, d) {
		return
	}
	mode := get("display.mode")
	if displayModeAllowedForRole(data.TillRoleSatellite, mode) {
		return
	}
	if err := applyDisplayMode(ctx, d, "self_order"); err != nil {
		logging.L().Errorf("till role: force a satellite back to the self-order kiosk: %v", err)
		return
	}
	from := mode
	if from == "" {
		from = "register"
	}
	logging.L().Warnf("till role: this satellite till was set to the %s profile; forced back to the self-order kiosk", from)
	_ = posRepo.InsertAudit(ctx, nil, "system", "settings", "display.mode", "display_mode_forced",
		map[string]any{"from": from, "mode": "self_order", "reason": "satellite"}, now(), "")
}

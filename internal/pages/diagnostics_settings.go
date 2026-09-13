package pages

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/diagnostics"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// diagnosticsView is the Settings card's (and its htmx re-renders') data —
// web/ui/partials/diagnostics_block.html.
type diagnosticsView struct {
	Active    bool
	SessionID string
	Since     string // localized activation date-time
	// PendingBatches/PendingEvents are what a local stop would discard —
	// surfaced in the stop confirmation (ADR-0092 §1's "after showing
	// their disposition").
	PendingBatches int
	PendingEvents  int
	ConfirmStop    bool   // render the two-step stop confirmation
	ErrKey         string // localized activation failure, if any
	Activated      bool   // render the just-turned-on notice
	Stopped        bool   // render the just-turned-off notice
	StoppedBatches int
	StoppedEvents  int
	EndedReason    string // diagnostics.Ended* for the last session, "" if none
	EndedAt        string
}

func diagnosticsViewFor(ctx context.Context, d *common.Deps, locale string) diagnosticsView {
	v := diagnosticsView{}
	if s, ok := diagnostics.Current(); ok {
		v.Active = true
		v.SessionID = s.ID
		v.Since = httpx.FormatDateTime(s.ActivatedAt.Local(), locale)
		v.PendingBatches, v.PendingEvents = diagnostics.PendingSummary()
		return v
	}
	reason, endedAt := diagnostics.EndedInfo(ctx, d.Settings)
	if reason != "" {
		v.EndedReason = reason
		if !endedAt.IsZero() {
			v.EndedAt = httpx.FormatDateTime(endedAt.Local(), locale)
		}
	}
	return v
}

// renderDiagnosticsBlock answers an htmx swap of the card body with the
// nav chip pushed out-of-band in the same response, so the rail flips the
// moment the card does rather than on the chip's next 30s poll.
func renderDiagnosticsBlock(w http.ResponseWriter, r *http.Request, d *common.Deps, view diagnosticsView) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	httpx.RenderPartial("ui/partials/diagnostics_block.html", map[string]any{"diagnostics": view})(w, r)
	fmt.Fprint(w, `<span id="diagnostics-chip" hx-swap-oob="innerHTML">`)
	if view.Active {
		renderDiagnosticsChip(w, r, d)
	}
	fmt.Fprint(w, `</span>`)
}

// renderDiagnosticsChip writes the active-session rail indicator. The
// href is gated like fiscal_chip.html's: only a session that may open
// /settings gets a link; everyone else gets the same disclosure as plain
// (non-focusable) markup rather than a link that 403s.
func renderDiagnosticsChip(w http.ResponseWriter, r *http.Request, d *common.Deps) {
	httpx.RenderPartial("ui/partials/diagnostics_chip.html", map[string]any{
		"canManage": canPerform(d, r, "settings"),
	})(w, r)
}

// auditDiagnostics writes one audit_log row for a diagnostic-mode state
// change. entityID is the session id when known.
func auditDiagnostics(ctx context.Context, d *common.Deps, actorID, action string, payload map[string]any) {
	sessionID, _ := payload["session_id"].(string)
	if sessionID == "" {
		sessionID = "-"
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if err := data.NewPOSRepo(d.Db).InsertAudit(ctx, nil, actorID, "diagnostics", sessionID, action, payload, now, ""); err != nil {
		logging.L().Errorf("diagnostics: audit %s: %v", action, err)
	}
}

// emitDiagnosticsInventory emits the environment + installed-plugin
// inventory events (ADR-0092 §2: app/build/OS/device/till id and plugin
// id/version/checksum/state) — once at activation and once per boot while
// active, so every session opens with the till's identity. No-op when
// inactive. DeviceModel is left empty for now: the Android shell does not
// plumb Build.MODEL through the gomobile bind (mobile.go sets only data/
// tmp/listen env) — a follow-up, not silently faked.
func emitDiagnosticsInventory(ctx context.Context, d *common.Deps) {
	if !diagnostics.Active() {
		return
	}
	mode, _, _ := d.Settings.Get(ctx, "display.mode")
	displayMode := diagnostics.DisplayModeRegister
	switch strings.TrimSpace(mode) {
	case "backoffice":
		displayMode = diagnostics.DisplayModeBackoffice
	case "self_order":
		displayMode = diagnostics.DisplayModeSelfOrder
	}
	diagnostics.Emit(diagnostics.Environment{
		AppVersion:  buildinfo.Version,
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		TillID:      enroll.CurrentStatus().DeviceID,
		DisplayMode: displayMode,
	})
	rows, err := data.NewPluginRepo(d.Db).ListInstalledPlugins(ctx)
	if err != nil {
		logging.L().Debugf("diagnostics: plugin inventory: %v", err)
		return
	}
	for _, row := range rows {
		state := diagnostics.PluginStateUnknown
		switch row.InstallState {
		case "", "active":
			state = diagnostics.PluginStateActive
		case "installed":
			state = diagnostics.PluginStateInstalled
		case "broken":
			state = diagnostics.PluginStateBroken
		case "revoked":
			state = diagnostics.PluginStateRevoked
		case "failed":
			state = diagnostics.PluginStateFailed
		}
		diagnostics.Emit(diagnostics.PluginState{PluginID: row.ID, Version: row.Version, Checksum: row.InstalledSHA256, State: state})
	}
}

// activationErrKey maps an ActivateDiagnostics failure to the locale key
// the card shows. The raw error text never reaches the operator (it can
// carry a URL/host and isn't translated); the code is never echoed.
//
// A *cloudsync.CloudError not matched by one of the specific cases below
// (e.g. a 400 invalid_request from a blank/oversized device_id, or any
// other documented code this card doesn't special-case) falls to
// err_refused, NOT err_unreachable — the cloud DID answer, so "could not
// be reached" would be factually wrong and gives the operator nothing
// actionable (review finding, ut-docs#2169). err_unreachable is reserved
// for the case with no CloudError at all — a transport-level failure
// (DNS, TLS, connection refused, timeout) where the cloud genuinely never
// answered.
func activationErrKey(err error) string {
	var ce *cloudsync.CloudError
	switch {
	case errors.Is(err, cloudsync.ErrNotRegistered):
		return "settings.diagnostics.err_not_registered"
	case errors.As(err, &ce):
		switch {
		case ce.Code == "invalid_code":
			return "settings.diagnostics.err_invalid_code"
		case ce.Code == "code_unavailable":
			return "settings.diagnostics.err_code_unavailable"
		case ce.Status == http.StatusUnauthorized:
			return "settings.diagnostics.err_unauthorized"
		default:
			return "settings.diagnostics.err_refused"
		}
	}
	return "settings.diagnostics.err_unreachable"
}

// registerDiagnosticsSettings wires ADR-0092's till-side controls
// (ut-docs#2169): the nav chip, activation-code redemption and the local
// stop. Both mutations are gated by the PLAIN "settings" role check
// (canPerform) — the same gate registerCountrySettings' requireManager and
// settings_page.go's dismiss/retry-tse-provisioning handlers use — not the
// checkOrElevate approver-PIN flow: turning diagnostics on/off is an
// ordinary manager settings change, not a payments/security-critical
// mutation on the scale of a permission grant (ADR-0092 §1: "the same
// owner/manager role check that already protects every other sensitive
// Settings mutation — no new role, no new permission bit").
func registerDiagnosticsSettings(mux *http.ServeMux, d *common.Deps) {
	requireManager := func(w http.ResponseWriter, r *http.Request) bool {
		if !canPerform(d, r, "settings") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return false
		}
		return true
	}

	// Rail indicator (ADR-0092 §7): empty 200 while inactive — the chips'
	// shared zero-state — else the persistent pulse + dot for EVERY role.
	mux.HandleFunc("GET /ui/diagnostics-chip", func(w http.ResponseWriter, r *http.Request) {
		if !diagnostics.Active() {
			w.WriteHeader(http.StatusOK)
			return
		}
		renderDiagnosticsChip(w, r, d)
	})

	// Redeem a Universal Till-issued code (ADR-0092 §1). Validation
	// (blank code) runs before any network call; the redemption itself is
	// the ONE place this feature touches the cloud synchronously — it is a
	// manager's explicit action on the Settings page, never the sale
	// path. On success the session is persisted as ordinary settings rows
	// (survives restart/reboot/update) before the card re-renders ON.
	mux.HandleFunc("POST /api/settings/diagnostics/activate", func(w http.ResponseWriter, r *http.Request) {
		if !requireManager(w, r) {
			return
		}
		locale := httpx.ResolveLocale(w, r)
		_ = r.ParseForm()
		code := strings.TrimSpace(r.Form.Get("code"))
		view := diagnosticsViewFor(r.Context(), d, locale)
		if view.Active {
			renderDiagnosticsBlock(w, r, d, view) // already on — nothing to redeem
			return
		}
		if code == "" {
			view.ErrKey = "settings.diagnostics.err_code_required"
			renderDiagnosticsBlock(w, r, d, view)
			return
		}
		sessionID, err := cloudsync.ActivateDiagnostics(r.Context(), d.Cfg, code)
		if err != nil {
			logging.L().Warnf("diagnostics: activation refused: %v", err)
			view.ErrKey = activationErrKey(err)
			renderDiagnosticsBlock(w, r, d, view)
			return
		}
		if err := diagnostics.Activate(r.Context(), d.Settings, sessionID, time.Now()); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "settings.error.save_failed", "diagnostics", err)
			return
		}
		auditDiagnostics(r.Context(), d, settingsActorID(r), "diagnostics_activated", map[string]any{"session_id": sessionID})
		emitDiagnosticsInventory(r.Context(), d)
		view = diagnosticsViewFor(r.Context(), d, locale)
		view.Activated = true
		renderDiagnosticsBlock(w, r, d, view)
	})

	// Backing out of the stop confirmation: re-render the plain card.
	mux.HandleFunc("POST /api/settings/diagnostics/cancel-stop", func(w http.ResponseWriter, r *http.Request) {
		if !requireManager(w, r) {
			return
		}
		renderDiagnosticsBlock(w, r, d, diagnosticsViewFor(r.Context(), d, httpx.ResolveLocale(w, r)))
	})

	// Local stop (ADR-0092 §1): two steps. Without confirm=1 the card
	// re-renders with the disposition — how many unsent batches/events
	// will be DISCARDED, not uploaded — and changes nothing. With it, the
	// stop is immediate and fully offline (settings store + local disk
	// only; the cloud is told best-effort on the next cloudsync tick).
	mux.HandleFunc("POST /api/settings/diagnostics/stop", func(w http.ResponseWriter, r *http.Request) {
		if !requireManager(w, r) {
			return
		}
		locale := httpx.ResolveLocale(w, r)
		_ = r.ParseForm()
		view := diagnosticsViewFor(r.Context(), d, locale)
		if !view.Active {
			renderDiagnosticsBlock(w, r, d, view) // already off (revoked/rejected meanwhile)
			return
		}
		if r.Form.Get("confirm") != "1" {
			view.ConfirmStop = true
			renderDiagnosticsBlock(w, r, d, view)
			return
		}
		res, err := diagnostics.Stop(r.Context(), d.Settings, diagnostics.EndedStopped)
		if err != nil {
			// The in-process flag is already off (capture stopped); only the
			// persisted rows failed. Log it, still show the operator the
			// truthful state.
			logging.L().Errorf("diagnostics: stop persisted with errors: %v", err)
		}
		auditDiagnostics(r.Context(), d, settingsActorID(r), "diagnostics_stopped", map[string]any{
			"session_id": res.SessionID, "discarded_batches": res.DiscardedBatches, "discarded_events": res.DiscardedEvents,
		})
		view = diagnosticsViewFor(r.Context(), d, locale)
		view.Stopped, view.StoppedBatches, view.StoppedEvents = true, res.DiscardedBatches, res.DiscardedEvents
		renderDiagnosticsBlock(w, r, d, view)
	})
}

package pages

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/print"
)

// noSaleReasonMaxRunes caps the optional free-text reason on a no-sale.
const noSaleReasonMaxRunes = 200

// noSaleRequest is POST /api/pos/no-sale's JSON body.
type noSaleRequest struct {
	ManagerPIN string `json:"manager_pin"`
	Reason     string `json:"reason"`
}

// registerNoSaleAPI wires POST /api/pos/no-sale (ut-docs#2558): open the cash
// drawer without a sale. Behind the normal session middleware (not
// auth-exempt) and deliberately NOT a self-order/kiosk route — it never
// touches a basket at all.
//
// Order of operations:
//  1. authorize — the dedicated no_sale permission (ut-docs#3560; a cash skim
//     is cash_adjustment, a separate right): a role holding it opens with no
//     PIN, any other session needs a manager PIN (checkOrElevate).
//  2. kick the drawer — print.OpenDrawer sends only the kick pulse over the
//     thermal transport. No printer / a system printer → 409; the printer
//     refusing or unreachable → 503. Nothing is recorded on failure: the
//     drawer did not open.
//  3. record — the no_sale_events row and its audit_log row in one
//     transaction (InsertAuditElevated when a PIN elevated the request).
//
// Two response shapes share one core (openDrawerAndRecord):
//   - JSON (body {manager_pin, reason}): the original contract, statuses and
//     error codes unchanged — 403 manager_pin_required (429 locked out).
//   - htmx (HX-Request and a non-JSON body, the sale screen's No sale
//     button, ut-docs#3560): a form with override_pin; always HTTP 200 —
//     the shared elevation prompt (ADR-0052) or an inline notice for
//     #no-sale-status, since htmx swaps only 2xx.
//
// Not a customer document: no receipt is printed, so the ADR-0124
// customer-document suppression does not apply.
func registerNoSaleAPI(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("POST /api/pos/no-sale", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("HX-Request") == "true" && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			noSaleHTMX(w, r, d)
			return
		}
		ctx := r.Context()
		var req noSaleRequest
		// An empty body is an empty request (no PIN, no reason).
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeNoSaleError(w, http.StatusBadRequest, "invalid_request", "request body must be a JSON object")
			return
		}
		reason := strings.TrimSpace(req.Reason)
		if utf8.RuneCountInString(reason) > noSaleReasonMaxRunes {
			writeNoSaleError(w, http.StatusBadRequest, "reason_too_long", "reason must be at most 200 characters")
			return
		}

		elev := checkOrElevate(d, r, "no_sale", strings.TrimSpace(req.ManagerPIN))
		if elev.Outcome == needsElevation || (elev.Outcome == allowed && elev.ActorID == "") {
			// An allowed outcome without a session user can't happen while
			// auth is on; fail closed if it ever does (the skim's rule).
			status := http.StatusForbidden
			if errors.Is(elev.Err, auth.ErrLockedOut) {
				status = http.StatusTooManyRequests
			}
			writeNoSaleError(w, status, "manager_pin_required", "manager PIN required")
			return
		}

		id, f := openDrawerAndRecord(ctx, d, elev, reason)
		switch f {
		case noSaleOK:
			writeJSON(w, http.StatusOK, map[string]any{"data": map[string]string{"id": id}, "error": nil})
		case noSaleSettingsUnavailable:
			writeNoSaleError(w, http.StatusInternalServerError, string(f), "printer settings could not be read")
		case noSaleNoDrawerPrinter:
			writeNoSaleError(w, http.StatusConflict, string(f), "no receipt printer is configured to open the cash drawer")
		case noSaleDrawerUnsupported:
			writeNoSaleError(w, http.StatusConflict, string(f), "the configured printer type cannot open a cash drawer")
		case noSaleKickFailed:
			writeNoSaleError(w, http.StatusServiceUnavailable, string(f), "the cash drawer could not be opened")
		default: // noSaleNotRecorded
			writeNoSaleError(w, http.StatusInternalServerError, string(noSaleNotRecorded), "the drawer opened but the no-sale could not be recorded")
		}
	})
}

// noSaleFailure is "" on success, else the JSON error code.
type noSaleFailure string

const (
	noSaleOK                  noSaleFailure = ""
	noSaleSettingsUnavailable noSaleFailure = "printer_settings_unavailable"
	noSaleNoDrawerPrinter     noSaleFailure = "no_drawer_printer"
	noSaleDrawerUnsupported   noSaleFailure = "drawer_unsupported"
	noSaleKickFailed          noSaleFailure = "drawer_kick_failed"
	noSaleNotRecorded         noSaleFailure = "not_recorded"
)

// openDrawerAndRecord is the shared core of both response shapes: read the
// printer config, kick the drawer, then record the event and audit row. The
// caller has already authorized (elev). Nothing is recorded unless the kick
// was accepted.
func openDrawerAndRecord(ctx context.Context, d *common.Deps, elev elevationCheck, reason string) (string, noSaleFailure) {
	actorID, approverID := elev.ActorID, ""
	if elev.Outcome == elevated {
		approverID = elev.ApproverID
	}
	cfg, err := printerConfigChecked(ctx, d)
	if err != nil {
		logging.L().Errorf("no-sale: read printer settings: %v", err)
		return "", noSaleSettingsUnavailable
	}
	if err := print.OpenDrawer(ctx, cfg); err != nil {
		switch {
		case errors.Is(err, print.ErrNoDrawerPrinter):
			return "", noSaleNoDrawerPrinter
		case errors.Is(err, print.ErrDrawerUnsupported):
			return "", noSaleDrawerUnsupported
		default:
			logging.L().Warnf("no-sale: drawer kick failed: %v", err)
			return "", noSaleKickFailed
		}
	}

	// The drawer is physically open from here on: a client that goes
	// away now must not cancel the record (engine_config.go's
	// WithoutCancel rule for work after an irreversible step).
	ctx = context.WithoutCancel(ctx)

	// The register a sale rung up on this device right now would
	// record (tender: this till's register identity, then the
	// EnsureRegister self-heal), so the open lands in the same
	// (business date, till) cloud-rollup key as that device's sales.
	repo := data.NewPOSRepo(d.Db)
	registerID := tillRegisterIDBestEffort(ctx, d)
	if registerID == "" {
		if regID, err := repo.EnsureRegister(ctx); err == nil {
			registerID = regID
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	id, err := recordNoSale(ctx, d, repo, data.NoSaleEvent{
		CreatedAt: now, RegisterID: registerID, ActorID: actorID, ApproverID: approverID, Reason: reason,
	}, elev.Outcome == elevated)
	if err != nil {
		// The drawer is already open; say so rather than pretend.
		logging.L().Errorf("no-sale: drawer opened but not recorded: %v", err)
		return "", noSaleNotRecorded
	}
	return id, noSaleOK
}

// noSaleHTMX serves the sale screen button (ut-docs#3560): the shared
// elevation prompt for a session without no_sale, else an inline notice for
// #no-sale-status. Always 200 — htmx does not swap 4xx/5xx and a 5xx would
// raise the generic server-error alert.
func noSaleHTMX(w http.ResponseWriter, r *http.Request, d *common.Deps) {
	locale := httpx.ResolveLocale(w, r)
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		renderNoSaleStatus(w, r, "error", "pos.no_sale.error.invalid", false, false)
		return
	}
	reason := strings.TrimSpace(r.Form.Get("reason"))
	if utf8.RuneCountInString(reason) > noSaleReasonMaxRunes {
		renderNoSaleStatus(w, r, "error", "pos.no_sale.error.invalid", false, false)
		return
	}
	elev := checkOrElevate(d, r, "no_sale", strings.TrimSpace(r.Form.Get("override_pin")))
	if elev.Outcome == needsElevation || (elev.Outcome == allowed && elev.ActorID == "") {
		renderElevationPrompt(w, r, "/api/pos/no-sale", "#no-sale-status",
			httpx.T(locale, "elevation.summary.no_sale"), nil, elev)
		return
	}
	_, f := openDrawerAndRecord(r.Context(), d, elev, reason)
	switch f {
	case noSaleOK:
		renderNoSaleStatus(w, r, "success", "pos.no_sale.opened", false, false)
	case noSaleNoDrawerPrinter, noSaleDrawerUnsupported:
		key := "pos.no_sale.error.no_printer"
		if f == noSaleDrawerUnsupported {
			key = "pos.no_sale.error.unsupported"
		}
		link := canPerform(d, r, "settings")
		renderNoSaleStatus(w, r, "error", key, link, !link)
	case noSaleSettingsUnavailable:
		renderNoSaleStatus(w, r, "error", "pos.no_sale.error.settings_unavailable", false, false)
	case noSaleKickFailed:
		renderNoSaleStatus(w, r, "error", "pos.no_sale.error.kick_failed", false, false)
	default:
		renderNoSaleStatus(w, r, "error", "pos.no_sale.error.not_recorded", false, false)
	}
}

// renderNoSaleStatus writes the notice fragment for #no-sale-status.
// X-UT-Response is "refused" for a failure and absent for a success: the
// elevation dialog closes on any answer but another prompt, and "ok" would
// make it refresh the target's region (a full page reload when none is
// declared), wiping the very notice just swapped in.
func renderNoSaleStatus(w http.ResponseWriter, r *http.Request, level, msgKey string, settingsLink, askManager bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if level != "success" {
		w.Header().Set("X-UT-Response", "refused")
	}
	httpx.RenderPartial("ui/partials/no_sale_status.html", map[string]any{
		"Level": level, "MsgKey": msgKey, "ShowSettingsLink": settingsLink, "ShowAskManager": askManager,
	})(w, r)
}

// noSaleDrawerAvailable reports whether the till has a drawer-capable
// printer, i.e. whether the sale screen renders its No sale button. One
// settings read, no plugin receipt-policy bus; any error hides the button.
func noSaleDrawerAvailable(ctx context.Context, d *common.Deps) bool {
	if d == nil || d.Settings == nil {
		return false
	}
	v, ok, err := d.Settings.Get(ctx, keyPrinterMode)
	if err != nil || !ok {
		return false
	}
	return print.DrawerCapable(strings.TrimSpace(v))
}

// recordNoSale writes the event row and its audit row atomically.
func recordNoSale(ctx context.Context, d *common.Deps, repo *data.POSRepo, e data.NoSaleEvent, wasElevated bool) (string, error) {
	tx, err := d.Db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	id, err := repo.InsertNoSaleEvent(ctx, tx, e)
	if err != nil {
		return "", err
	}
	payload := map[string]any{"reason": e.Reason, "register_id": e.RegisterID}
	if wasElevated {
		// Approver of record is the PIN owner; the blocked session user
		// rides along (InsertAuditElevated's convention).
		err = repo.InsertAuditElevated(ctx, tx, e.ApproverID, e.ActorID, "cash_drawer", id, "no_sale", payload, e.CreatedAt, "")
	} else {
		err = repo.InsertAudit(ctx, tx, e.ActorID, "cash_drawer", id, "no_sale", payload, e.CreatedAt, "")
	}
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return id, nil
}

// writeNoSaleError writes the repo's JSON error envelope.
func writeNoSaleError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"data": nil, "error": map[string]string{"code": code, "message": msg}})
}

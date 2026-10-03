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
//  1. authorize — the cash_adjustment permission, exactly like a cash skim
//     (shifts_api.go): a role holding it opens with no PIN, any other
//     session needs a manager PIN (checkOrElevate). 403 manager_pin_required
//     (429 once the PIN lockout trips).
//  2. kick the drawer — print.OpenDrawer sends only the kick pulse over the
//     thermal transport. No printer / a system printer → 409; the printer
//     refusing or unreachable → 503. Nothing is recorded on failure: the
//     drawer did not open.
//  3. record — the no_sale_events row and its audit_log row in one
//     transaction (InsertAuditElevated when a PIN elevated the request).
//
// Not a customer document: no receipt is printed, so the ADR-0124
// customer-document suppression does not apply.
func registerNoSaleAPI(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("POST /api/pos/no-sale", func(w http.ResponseWriter, r *http.Request) {
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

		elev := checkOrElevate(d, r, "cash_adjustment", strings.TrimSpace(req.ManagerPIN))
		actorID, approverID := elev.ActorID, ""
		switch elev.Outcome {
		case needsElevation:
			status := http.StatusForbidden
			if errors.Is(elev.Err, auth.ErrLockedOut) {
				status = http.StatusTooManyRequests
			}
			writeNoSaleError(w, status, "manager_pin_required", "manager PIN required")
			return
		case elevated:
			approverID = elev.ApproverID
		default: // allowed
			if actorID == "" {
				// canPerform can't be true without a session user while
				// auth is on; fail closed if it ever is (the skim's rule).
				writeNoSaleError(w, http.StatusForbidden, "manager_pin_required", "manager PIN required")
				return
			}
		}

		cfg, err := printerConfigChecked(ctx, d)
		if err != nil {
			logging.L().Errorf("no-sale: read printer settings: %v", err)
			writeNoSaleError(w, http.StatusInternalServerError, "printer_settings_unavailable", "printer settings could not be read")
			return
		}
		if err := print.OpenDrawer(ctx, cfg); err != nil {
			switch {
			case errors.Is(err, print.ErrNoDrawerPrinter):
				writeNoSaleError(w, http.StatusConflict, "no_drawer_printer", "no receipt printer is configured to open the cash drawer")
			case errors.Is(err, print.ErrDrawerUnsupported):
				writeNoSaleError(w, http.StatusConflict, "drawer_unsupported", "the configured printer type cannot open a cash drawer")
			default:
				logging.L().Warnf("no-sale: drawer kick failed: %v", err)
				writeNoSaleError(w, http.StatusServiceUnavailable, "drawer_kick_failed", "the cash drawer could not be opened")
			}
			return
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
			writeNoSaleError(w, http.StatusInternalServerError, "not_recorded", "the drawer opened but the no-sale could not be recorded")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]string{"id": id}, "error": nil})
	})
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

package pages

import (
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// tableTile is one table as the SVG floor plan renders it: the persisted
// entity plus presentation-ready live state. OpenMinutes is the elapsed time
// of the oldest open order on the table (0 when free) — pre-#820 nothing
// assigns orders to tables, so every tile renders free; that is the correct,
// honest state, not a gap (ADR-0054 scope split).
type tableTile struct {
	data.TableWithState
	OpenMinutes int
}

// elapsedMinutes parses a held_sales.created_at value and returns whole
// minutes elapsed at `now`, floored at 0. Both live formats are accepted:
// the schema default (datetime('now') → "2006-01-02 15:04:05", UTC) and
// RFC3339 in case a writer ever sets the column explicitly. Unparseable or
// empty input reads as 0 — a missing elapsed label, never a broken page.
func elapsedMinutes(since string, now time.Time) int {
	if since == "" {
		return 0
	}
	ts, err := time.Parse(time.RFC3339, since)
	if err != nil {
		ts, err = time.ParseInLocation("2006-01-02 15:04:05", since, time.UTC)
		if err != nil {
			return 0
		}
	}
	m := int(now.Sub(ts).Minutes())
	if m < 0 {
		return 0
	}
	return m
}

// registerTables wires the table floor-plan page (universaltill/ut-docs#814,
// ADR-0054): CRUD for dining tables plus the product's first free-position
// 2D editor — an inline SVG on a fixed 1000×1000 logical canvas, drag-to-
// place via pointer events, live free/occupied state polled as an HTMX
// partial. Manager/admin only, modelled on registerKitchenStations. Tables
// are soft-disabled, never deleted. Order assignment (which would make
// tables actually show occupied) is the follow-on slice, ut-docs#820.
func registerTables(mux *http.ServeMux, d *common.Deps) {
	posRepo := data.NewPOSRepo(d.Db)

	// requireManager gates on the "settings" action (039's catalog) via
	// canPerform — see country_settings_page.go's identical
	// requireManager for why (ut-docs#901/#902): the old raw
	// IsManager() check never saw canPerform's UT_AUTH=off escape hatch,
	// so this page 403'd permanently under the dev/CI auth-bypass. No
	// change to gated (UT_AUTH on) behavior.
	requireManager := func(w http.ResponseWriter, r *http.Request) (auth.User, bool) {
		if !canPerform(d, r, "settings") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return auth.User{}, false
		}
		u, _ := auth.FromContext(r.Context())
		return u, true
	}

	// requirePageManager is requireManager's gate, answered as a full
	// RenderError page instead of a bare LocalizedError body — for the
	// /tables page route itself (ut-docs#1455 review finding: the reported
	// incident's own page still 403'd with a bare, rail-less body via
	// requireManager, one call away from the fix this file otherwise
	// carries). requireManager stays as-is for /ui/tables/state (a
	// fragment) and the /api/tables/* mutation routes, which need the
	// short body their JS callers expect.
	requirePageManager := func(w http.ResponseWriter, r *http.Request) (auth.User, bool) {
		if !canPerform(d, r, "settings") {
			httpx.RenderError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required", nil)
			return auth.User{}, false
		}
		u, _ := auth.FromContext(r.Context())
		return u, true
	}

	// requirePrimary gates every mutation (create/update/position/active)
	// on this till being the primary (ut-docs#1585). tables syncs shop-wide
	// as an admin table (adminTables, ut-docs#1546) via a one-way
	// primary-wins pull, so a write accepted on a satellite would silently
	// vanish (a new table deleted, an edit reverted) on the very next admin
	// pull -- refuse it up front instead, with a clear localized message,
	// same pattern as registers_page.go's requirePrimary (ut-docs#1590).
	// The redirect-based routes below use this; the JS-driven position
	// endpoint answers with a status code instead (see below).
	requirePrimary := func(w http.ResponseWriter, r *http.Request) bool {
		if d.SyncPrimaryURL(r.Context()) != "" {
			http.Redirect(w, r, "/tables?err=tables.error.replica_use_primary", http.StatusSeeOther)
			return false
		}
		return true
	}

	audit := func(r *http.Request, actorID, targetID, action string, payload any) {
		now := time.Now().UTC().Format(time.RFC3339)
		// A table audit write should never be able to block or fail the
		// mutation it records, so its error stays fire-and-forget (same
		// convention as every other InsertAudit call site in this
		// package) -- but a failure here was previously invisible even
		// to an operator looking at the logs (ut-docs#1715). Log it, same
		// shape as hold_api.go's own "silent, durable leak" log line for
		// a comparable swallowed-error case.
		if err := posRepo.InsertAudit(r.Context(), nil, actorID, "table", targetID, action, payload, now, ""); err != nil {
			log.Printf("table audit write failed: actor=%s target=%s action=%s: %v", actorID, targetID, action, err)
		}
	}

	// tiles is shared by both the full page below and its live-state HTMX
	// partial, which need different failure treatments — a full page can
	// render the error through the base layout (rail + Back to sale), a
	// partial swap cannot — so it returns the error to each caller instead
	// of writing to w itself (ut-docs#1455: the error was previously
	// written here as a bare, unlogged http.Error, which on a pinned
	// Android kiosk replaced the whole screen with plain text and no way
	// back).
	tiles := func(r *http.Request) ([]tableTile, error) {
		// ut-docs#1392: a replica shows the PRIMARY's live occupancy when
		// reachable, falling back to its own local state otherwise — see
		// tablesWithStateForDisplay's own doc comment.
		now := time.Now().UTC()
		states, err := tablesWithStateForDisplay(r.Context(), d, posRepo, now.Add(-tillClaimTTL))
		if err != nil {
			return nil, err
		}
		out := make([]tableTile, 0, len(states))
		for _, s := range states {
			out = append(out, tableTile{TableWithState: s, OpenMinutes: elapsedMinutes(s.OccupiedSince, now)})
		}
		return out, nil
	}

	mux.HandleFunc("GET /tables", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requirePageManager(w, r); !ok {
			return
		}
		rows, err := tiles(r)
		if err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "tables.error.load_failed", err)
			return
		}
		httpx.Render("ui/pages/tables.html", map[string]any{
			"title":       "Tables",
			"theme":       d.CurrentState().Theme,
			"menuItems":   d.MenuSnapshot(),
			"tables":      rows,
			"canvasSize":  data.TableCanvasSize,
			"canvasInset": data.TableEdgeInset,
			"errKey":      r.URL.Query().Get("err"),
		})(w, r)
	})

	// Live-state SVG partial: polled by the page (HTMX every 15s, paused
	// while editing) and loaded once on first paint. Renders the WHOLE
	// <svg> element — an HTML-parsed fragment rooted at <svg> keeps the SVG
	// namespace, so the swapped nodes stay real SVG elements.
	mux.HandleFunc("GET /ui/tables/state", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireManager(w, r); !ok {
			return
		}
		rows, err := tiles(r)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "tables.error.load_failed", "tables_state", err)
			return
		}
		httpx.RenderPartial("ui/partials/tables_state.html", map[string]any{
			"tables":     rows,
			"canvasSize": data.TableCanvasSize,
		})(w, r)
	})

	// tableFieldMaxLen bounds label/area_zone (2026-08-19 code review,
	// ut-docs#814): both render into an SVG <text> with no wrap or overflow
	// container (web/ui/partials/tables_state.html), so an unbounded paste
	// would draw one unclipped line across the whole floor plan.
	const tableFieldMaxLen = 64
	// tableMaxSeats is a generous real-world ceiling (no venue reviewed in
	// #814's competitive scan has anywhere near this many seats at one
	// table) that still catches a fat-fingered or malicious huge value.
	const tableMaxSeats = 999

	parseTableForm := func(r *http.Request) (label, zone, shape string, seats int, errKey string) {
		_ = r.ParseForm()
		label = strings.TrimSpace(r.PostFormValue("label"))
		zone = strings.TrimSpace(r.PostFormValue("area_zone"))
		shape = strings.TrimSpace(r.PostFormValue("shape"))
		if label == "" {
			return "", "", "", 0, "tables.error.required"
		}
		if len(label) > tableFieldMaxLen || len(zone) > tableFieldMaxLen {
			return "", "", "", 0, "tables.error.too_long"
		}
		if shape != "rect" && shape != "round" {
			return "", "", "", 0, "tables.error.shape"
		}
		if s := strings.TrimSpace(r.PostFormValue("seat_count")); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 0 || n > tableMaxSeats {
				return "", "", "", 0, "tables.error.seats"
			}
			seats = n
		}
		return label, zone, shape, seats, ""
	}

	mux.HandleFunc("POST /api/tables", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		if !requirePrimary(w, r) {
			return
		}
		label, zone, shape, seats, errKey := parseTableForm(r)
		if errKey != "" {
			http.Redirect(w, r, "/tables?err="+errKey, http.StatusSeeOther)
			return
		}
		// Position: the tap-to-place dialog (ut-docs#1025) sends the tapped
		// canvas coordinates as optional pos_x/pos_y. When absent, empty or
		// unparseable, fall back to the canvas centre — exactly the pre-#1025
		// behaviour the bottom-of-page add form (which never sends these
		// fields) still relies on; the operator drags from there. The repo
		// clamps either way, so no client value can land off-plan.
		posX, posY := data.TableCanvasSize/2, data.TableCanvasSize/2
		if v, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue("pos_x"))); err == nil {
			posX = v
		}
		if v, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue("pos_y"))); err == nil {
			posY = v
		}
		id, err := posRepo.CreateTable(r.Context(), label, zone, seats, shape, posX, posY)
		if err != nil {
			http.Redirect(w, r, "/tables?err=tables.error.create", http.StatusSeeOther)
			return
		}
		audit(r, actor.ID, id, "table_create", nil)
		http.Redirect(w, r, "/tables", http.StatusSeeOther)
	})

	mux.HandleFunc("POST /api/tables/{id}", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		if !requirePrimary(w, r) {
			return
		}
		id := r.PathValue("id")
		label, zone, shape, seats, errKey := parseTableForm(r)
		if errKey != "" {
			http.Redirect(w, r, "/tables?err="+errKey, http.StatusSeeOther)
			return
		}
		if err := posRepo.UpdateTable(r.Context(), id, label, zone, seats, shape); err != nil {
			key := "tables.error.update"
			if strings.Contains(err.Error(), "not found") {
				key = "tables.error.not_found"
			}
			http.Redirect(w, r, "/tables?err="+key, http.StatusSeeOther)
			return
		}
		audit(r, actor.ID, id, "table_update", nil)
		http.Redirect(w, r, "/tables", http.StatusSeeOther)
	})

	// Drag-end position save — called by the editor's JS (fetch), so it
	// answers with status codes, not redirects: 204 on success, 400/404 on
	// bad input, and the JS surfaces a translated error line.
	mux.HandleFunc("POST /api/tables/{id}/position", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		// A JS fetch caller, not a redirect target -- answers with a status
		// code like every other failure on this route (ut-docs#1585). 409
		// Conflict, same code plugins_store_page.go's replica gate uses, so
		// the client can tell "you're on a replica" apart from a generic
		// failure (persistPosition in tables.html's own <script>).
		if d.SyncPrimaryURL(r.Context()) != "" {
			http.Error(w, "replica", http.StatusConflict)
			return
		}
		id := r.PathValue("id")
		_ = r.ParseForm()
		x, errX := strconv.Atoi(strings.TrimSpace(r.PostFormValue("pos_x")))
		y, errY := strconv.Atoi(strings.TrimSpace(r.PostFormValue("pos_y")))
		if errX != nil || errY != nil {
			http.Error(w, "pos_x and pos_y must be integers", http.StatusBadRequest)
			return
		}
		if err := posRepo.SetTablePosition(r.Context(), id, x, y); err != nil {
			if strings.Contains(err.Error(), "not found") {
				http.Error(w, "table not found", http.StatusNotFound)
				return
			}
			http.Error(w, "failed to save position", http.StatusInternalServerError)
			return
		}
		audit(r, actor.ID, id, "table_move", nil)
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /api/tables/{id}/active", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		if !requirePrimary(w, r) {
			return
		}
		id := r.PathValue("id")
		_ = r.ParseForm()
		enable := r.PostFormValue("active") == "1"
		if err := posRepo.SetTableEnabled(r.Context(), id, enable); err != nil {
			key := "tables.error.update"
			if strings.Contains(err.Error(), "not found") {
				key = "tables.error.not_found"
			}
			http.Redirect(w, r, "/tables?err="+key, http.StatusSeeOther)
			return
		}
		action := "table_deactivate"
		if enable {
			action = "table_activate"
		}
		audit(r, actor.ID, id, action, nil)
		http.Redirect(w, r, "/tables", http.StatusSeeOther)
	})

	// Manual "Free table" override (ut-docs#1393): the escape hatch for a
	// table stuck reading occupied with nothing real behind it — a till
	// crash between claiming a table and completing/clearing the sale, or a
	// replica claim nobody ever revisits (ClearLocalTableClaims and
	// ClaimTableForTill's TTL reconciliation only ever clear a claim when
	// its OWNING till acts again). Manager/primary-gated like every other
	// mutation here. Idempotent — releasing an already-free table succeeds
	// with the same plain redirect as a real release, same no-op convention
	// as the rest of this file's release primitives. Never destroys a real
	// held order: ForceReleaseTableClaim only ever touches table_claims, so
	// a table with a genuine held_sales row still reads occupied
	// immediately afterwards — reported back via an err key rather than
	// pretending the table is now free.
	mux.HandleFunc("POST /api/tables/{id}/release", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		if !requirePrimary(w, r) {
			return
		}
		id := r.PathValue("id")
		// GetTable's error and not-found cases are kept distinct (independent
		// review finding, ut-docs#1393) — a real DB fault reported as "table
		// not found" would hide the actual problem from whoever reads the
		// redirect.
		_, found, err := posRepo.GetTable(r.Context(), id)
		if err != nil {
			http.Redirect(w, r, "/tables?err=tables.error.load_failed", http.StatusSeeOther)
			return
		}
		if !found {
			http.Redirect(w, r, "/tables?err=tables.error.not_found", http.StatusSeeOther)
			return
		}
		result, err := posRepo.ForceReleaseTableClaim(r.Context(), id, time.Now().Add(-tillClaimTTL))
		if err != nil {
			http.Redirect(w, r, "/tables?err=tables.error.release", http.StatusSeeOther)
			return
		}
		audit(r, actor.ID, id, "table_release", map[string]any{
			"claim_released":            result.Released,
			"held_order_still_attached": result.StillHeld,
			"other_till_recently_seen":  result.OtherTillRecentlySeen,
			"other_till_id":             result.OtherTillID,
		})
		if result.StillHeld {
			// Two distinct messages (independent review finding, ut-docs#1393):
			// "claim cleared" is only true when a claim actually existed —
			// pressing this on a table occupied ONLY by a genuine held order
			// (no stuck claim at all, arguably the more common press) cleared
			// nothing, and saying otherwise would misdescribe what happened.
			key := "tables.error.held_order_attached"
			if !result.Released {
				key = "tables.error.held_order_only"
			}
			http.Redirect(w, r, "/tables?err="+key, http.StatusSeeOther)
			return
		}
		if result.OtherTillRecentlySeen {
			// ut-docs#1723: held_sales isn't cross-till visible (StillHeld
			// above only ever sees THIS till's own held orders), so this is
			// the strongest honest signal available — the claim belonged to
			// a different till this primary heard from within the last
			// tillClaimTTL, not proof a held order is sitting there. See
			// TableReleaseResult's doc comment in internal/data.
			http.Redirect(w, r, "/tables?err=tables.error.released_other_till_recent", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/tables", http.StatusSeeOther)
	})
}

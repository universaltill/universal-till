package pages

import (
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

	requireManager := func(w http.ResponseWriter, r *http.Request) (auth.User, bool) {
		u, ok := auth.FromContext(r.Context())
		if !ok || !u.IsManager() {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return auth.User{}, false
		}
		return u, true
	}

	audit := func(r *http.Request, actorID, targetID, action string) {
		now := time.Now().UTC().Format(time.RFC3339)
		_ = posRepo.InsertAudit(r.Context(), nil, actorID, "table", targetID, action, nil, now, "")
	}

	tiles := func(w http.ResponseWriter, r *http.Request) ([]tableTile, bool) {
		states, err := posRepo.ListTablesWithState(r.Context())
		if err != nil {
			http.Error(w, "failed to load tables", http.StatusInternalServerError)
			return nil, false
		}
		now := time.Now().UTC()
		out := make([]tableTile, 0, len(states))
		for _, s := range states {
			out = append(out, tableTile{TableWithState: s, OpenMinutes: elapsedMinutes(s.OccupiedSince, now)})
		}
		return out, true
	}

	mux.HandleFunc("GET /tables", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireManager(w, r); !ok {
			return
		}
		rows, ok := tiles(w, r)
		if !ok {
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
		rows, ok := tiles(w, r)
		if !ok {
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
		label, zone, shape, seats, errKey := parseTableForm(r)
		if errKey != "" {
			http.Redirect(w, r, "/tables?err="+errKey, http.StatusSeeOther)
			return
		}
		// New tables land at the canvas centre; the operator drags them into
		// place. The repo clamps, so no client value can land off-plan.
		id, err := posRepo.CreateTable(r.Context(), label, zone, seats, shape,
			data.TableCanvasSize/2, data.TableCanvasSize/2)
		if err != nil {
			http.Redirect(w, r, "/tables?err=tables.error.create", http.StatusSeeOther)
			return
		}
		audit(r, actor.ID, id, "table_create")
		http.Redirect(w, r, "/tables", http.StatusSeeOther)
	})

	mux.HandleFunc("POST /api/tables/{id}", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
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
		audit(r, actor.ID, id, "table_update")
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
		audit(r, actor.ID, id, "table_move")
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /api/tables/{id}/active", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
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
		audit(r, actor.ID, id, action)
		http.Redirect(w, r, "/tables", http.StatusSeeOther)
	})
}

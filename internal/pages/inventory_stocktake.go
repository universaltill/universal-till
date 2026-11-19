package pages

import (
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

// Till-side stock-take for one location on /inventory (ut-docs#5852).
// Counts are absolute and go through data.POSRepo.ApplyStockCounts (the
// ADR-0168 §4 count semantics the cloud's count directive uses): sales made
// since the take started are kept, a repeat is a zero movement, and the
// whole take is one transaction.

const (
	stockTakeMaxLines     = 500
	stockTakeMaxAge       = 48 * time.Hour
	stockTakeMaxTarget    = 1_000_000
	stockTakeMaxNoteRunes = 200
	stockTakeMaxBody      = 256 << 10
	stockTakeDefaultNote  = "stock take"
)

type stockTakeLineIn struct {
	ItemID    string  `json:"item_id"`
	VariantID string  `json:"variant_id"`
	Target    float64 `json:"target"`
}

type stockTakeRequest struct {
	LocationID string            `json:"location_id"`
	StartedAt  string            `json:"started_at"`
	Note       string            `json:"note"`
	Lines      []stockTakeLineIn `json:"lines"`
}

type stockTakeLineErr struct {
	Index int    `json:"index"`
	Code  string `json:"code"`
}

// stockTakeGate runs the checks both stock-take routes share, in order:
// permission (403), replica (409: a count made on a joined till would be
// reverted by the next stock pull from the main till), session (401).
func stockTakeGate(d *common.Deps, w http.ResponseWriter, r *http.Request) (actor string, ok bool) {
	if !canPerform(d, r, "stock_management") {
		common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
		return "", false
	}
	if d.SyncPrimaryURL(r.Context()) != "" {
		stockTakeError(w, r, http.StatusConflict, "replica_use_main", "inventory.stocktake.error.replica_use_main", nil)
		return "", false
	}
	actor = getSessionUserID(r)
	if actor == "" {
		respondError(w, r, http.StatusUnauthorized, "authentication required")
		return "", false
	}
	return actor, true
}

// stockTakeError writes the { "data": null, "error": { code, message,
// lines } } envelope.
func stockTakeError(w http.ResponseWriter, r *http.Request, status int, code, msgKey string, lines []stockTakeLineErr) {
	e := map[string]any{"code": code, "message": httpx.T(httpx.ResolveLocale(w, r), msgKey)}
	if len(lines) > 0 {
		e["lines"] = lines
	}
	writeJSON(w, status, map[string]any{"data": nil, "error": e})
}

// stockTakeKey identifies a countable line: the variant when there is one
// (a variant is stocked on its own row, ADR-0043), else the item.
func stockTakeKey(itemID, variantID string) string {
	if variantID != "" {
		return "v:" + variantID
	}
	return "i:" + itemID
}

func registerInventoryStockTake(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /ui/inventory/stock-take", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := stockTakeGate(d, w, r); !ok {
			return
		}
		ctx := r.Context()
		repo := data.NewPOSRepo(d.Db)
		locID := r.URL.Query().Get("location_id")
		locName, found := "", false
		locs, _ := repo.ListActiveStockLocations(ctx)
		for _, l := range locs {
			if l.ID == locID {
				locName, found = l.Name, true
			}
		}
		if !found {
			common.LocalizedError(w, r, http.StatusBadRequest, "inventory.stocktake.error.location_invalid")
			return
		}
		rows, err := repo.ListStockTakeRows(ctx, locID)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "inventory.stocktake.error.save_failed", "stocktake rows", err)
			return
		}
		httpx.RenderPartial("ui/partials/stock_take_table.html", map[string]any{
			"LocationID":   locID,
			"LocationName": locName,
			"Rows":         rows,
			"StartedAt":    time.Now().UTC().Truncate(time.Second).Format(time.RFC3339),
			"MaxLines":     stockTakeMaxLines,
		})(w, r)
	})

	mux.HandleFunc("POST /api/inventory/stock-take", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := stockTakeGate(d, w, r)
		if !ok {
			return
		}
		ctx := r.Context()
		if !strings.Contains(r.Header.Get("Content-Type"), "application/json") {
			writeJSON(w, http.StatusBadRequest, map[string]any{"data": nil, "error": "invalid JSON"})
			return
		}
		var req stockTakeRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, stockTakeMaxBody)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"data": nil, "error": "invalid JSON"})
			return
		}
		bad := func(code, key string) { stockTakeError(w, r, http.StatusBadRequest, code, key, nil) }

		repo := data.NewPOSRepo(d.Db)
		locOK := false
		locs, _ := repo.ListActiveStockLocations(ctx)
		for _, l := range locs {
			locOK = locOK || l.ID == req.LocationID
		}
		if !locOK {
			bad("location_invalid", "inventory.stocktake.error.location_invalid")
			return
		}
		if len(req.Lines) == 0 {
			bad("no_lines", "inventory.stocktake.none_counted")
			return
		}
		if len(req.Lines) > stockTakeMaxLines {
			bad("too_many_lines", "inventory.stocktake.too_many")
			return
		}
		startedAt, err := time.Parse(time.RFC3339, req.StartedAt)
		now := time.Now()
		if err != nil || startedAt.After(now) || startedAt.Before(now.Add(-stockTakeMaxAge)) {
			bad("started_at_invalid", "inventory.stocktake.error.started_at_invalid")
			return
		}
		note, nerr := data.CheckSingleLineName(req.Note)
		if nerr != nil || utf8.RuneCountInString(note) > stockTakeMaxNoteRunes {
			bad("note_invalid", "inventory.stocktake.error.note_invalid")
			return
		}
		if note == "" {
			note = stockTakeDefaultNote
		}

		// The countable set is both the list shown and the validator.
		rows, err := repo.ListStockTakeRows(ctx, req.LocationID)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "inventory.stocktake.error.save_failed", "stocktake rows", err)
			return
		}
		countable := make(map[string]data.StockTakeRow, len(rows))
		for _, row := range rows {
			countable[stockTakeKey(row.ItemID, row.VariantID)] = row
		}
		var lineErrs []stockTakeLineErr
		seen := make(map[string]bool, len(req.Lines))
		inputs := make([]data.StockCountInput, 0, len(req.Lines))
		for i, l := range req.Lines {
			key := stockTakeKey(l.ItemID, l.VariantID)
			row, known := countable[key]
			// A variant line may omit item_id; when given it must be the
			// variant's own item.
			if known && l.VariantID != "" && l.ItemID != "" && l.ItemID != row.ItemID {
				known = false
			}
			var code string
			switch {
			case !known:
				code = "not_countable"
			case seen[key]:
				code = "duplicate"
			case l.Target < 0 || math.IsNaN(l.Target) || math.IsInf(l.Target, 0):
				code = "target_invalid"
			case l.Target > stockTakeMaxTarget:
				code = "target_too_large"
			case math.Abs(l.Target*1000-math.Round(l.Target*1000)) > 1e-6:
				code = "too_many_decimals"
			case !row.IsWeighed && l.Target != math.Trunc(l.Target):
				code = "whole_number_required"
			}
			seen[key] = true
			if code != "" {
				lineErrs = append(lineErrs, stockTakeLineErr{Index: i, Code: code})
				continue
			}
			in := data.StockCountInput{
				LocationID: req.LocationID, Target: l.Target, CountedAt: startedAt,
				Reason: note, ActorID: actor,
			}
			if l.VariantID != "" {
				in.VariantID = l.VariantID
			} else {
				in.ItemID = l.ItemID
			}
			inputs = append(inputs, in)
		}
		if len(lineErrs) > 0 {
			stockTakeError(w, r, http.StatusBadRequest, "invalid_lines", "inventory.stocktake.error.invalid_lines", lineErrs)
			return
		}

		results, err := repo.ApplyStockCounts(ctx, inputs)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "inventory.stocktake.error.save_failed", "stocktake apply", err)
			return
		}
		changed := 0
		out := make([]map[string]any, 0, len(results))
		for i, res := range results {
			out = append(out, map[string]any{"index": i, "delta": res.Delta})
			if res.Delta == 0 {
				continue
			}
			changed++
			publishStockAdjusted(ctx, d, plugins.StockAdjustedEvent{
				ItemID: inputs[i].ItemID, VariantID: inputs[i].VariantID, DeltaQty: res.Delta,
				Reason: stockMovementReason(data.StockCountMovementType), Location: req.LocationID,
			})
		}
		if changed > 0 {
			d.NudgeLink(fleetlink.ScopeStock)
		}
		w.Header().Set("HX-Trigger", "stock-updated")
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
			"counted": len(results), "changed": changed, "unchanged": len(results) - changed, "lines": out,
		}, "error": nil})
	})
}

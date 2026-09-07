package pages

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/ui"
)

// maxHoldLabelRunes bounds a cashier-typed tab name (e.g. "Tab 1") --
// unlike the previous CustomerName/timestamp fallback, this field is now
// free text straight off the request, and it's re-rendered on every sale
// screen load until resumed.
const maxHoldLabelRunes = 64

// heldSaleMayHaveTable is the table-eligibility predicate over a held
// payload (ut-docs#1181, ADR-0073 Decision 5): a table is allowed while any
// LINE is dine-in — evaluated over the lines, with the legacy header
// fallback (a pre-ADR-0073 "takeaway" header with no line values means every
// line was takeaway). Replaces the header-only `!= Takeaway` check, which
// would have been only accidentally right for a "mixed" header.
func heldSaleMayHaveTable(payload string) bool {
	var snap pos.BasketSnapshot
	if err := json.Unmarshal([]byte(payload), &snap); err != nil {
		log.Printf("heldSaleMayHaveTable: decode held sale payload: %v", err)
		return false // fail closed, same as the direct-endpoint gate below
	}
	if len(snap.Lines) == 0 {
		return snap.OrderType != pos.OrderTypeTakeaway
	}
	anyTyped := false
	for _, l := range snap.Lines {
		if l.OrderType != "" {
			anyTyped = true
			break
		}
	}
	for _, l := range snap.Lines {
		mode := pos.NormalizeLineOrderType(l.OrderType)
		if !anyTyped && snap.OrderType == pos.OrderTypeTakeaway {
			mode = pos.OrderTypeTakeaway
		}
		if mode != pos.OrderTypeTakeaway {
			return true
		}
	}
	return false
}

func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max])
}

// registerHoldAPI wires hold/resume: park the current basket so another
// customer can be served, then bring it back. Held sales are persisted
// (held_sales table) so they survive a till restart — offline-first.
func registerHoldAPI(mux *http.ServeMux, d *common.Deps) {
	repo := data.NewHeldSalesRepo(d.Db)
	// posRepo backs table-assignment reads (ut-docs#820): resolving a held
	// sale's table_id to its current display label for the strip, and the
	// free-table check the "move to a different table" handler enforces.
	posRepo := data.NewPOSRepo(d.Db)

	// renderHeldStrip builds the held-sales strip fragment -- the shared
	// body behind both GET /ui/held (first paint / hx-trigger="held-changed"
	// re-fetch) and POST /api/pos/held/table (re-rendered in place after a
	// move, same as every other mutating handler here re-renders its own
	// fragment). Table labels are resolved via a single ListTables call
	// rather than teaching HeldSalesRepo about `tables` — display-only
	// joins like this stay at the pages layer, same choice kitchenTicketFor
	// makes for order-type text.
	renderHeldStrip := func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		locale := httpx.ResolveLocale(w, r)
		items, err := repo.List(ctx)
		if err != nil {
			items = nil
		}
		// ListTablesWithState (not ListTables) so the per-order "Move table"
		// control below can offer only tables that are actually free -- the
		// same occupancy source the basket picker uses. A shop with no tables
		// configured yields an empty map/list, so no table chrome renders at
		// all (ADR-0054 soft-gate).
		labelByTableID := map[string]string{}
		var freeTables []data.TableWithState
		if states, err := posRepo.ListTablesWithState(ctx); err == nil {
			for _, s := range states {
				if !s.Enabled {
					continue
				}
				labelByTableID[s.ID] = s.Label
				if !s.Occupied {
					freeTables = append(freeTables, s)
				}
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		var b strings.Builder
		b.WriteString(`<div id="held-sales" class="held-strip" hx-get="/ui/held" hx-trigger="held-changed from:body" hx-swap="outerHTML">`)
		if len(items) > 0 {
			fmt.Fprintf(&b, `<span class="held-title">%s</span>`, template.HTMLEscapeString(httpx.T(locale, "hold.strip.title")))
			funcs := httpx.FuncsFor(locale)
			moneyFn, _ := funcs["money"].(func(v any) string)
			for _, h := range items {
				total := fmt.Sprintf("%d", h.TotalMinor)
				if moneyFn != nil {
					total = moneyFn(h.TotalMinor)
				}
				tableChip := ""
				if h.TableID != "" {
					label := labelByTableID[h.TableID]
					if label == "" {
						label = h.TableID
					}
					tableChip = fmt.Sprintf(` <span class="held-chip-table">%s</span>`, template.HTMLEscapeString(label))
				}
				fmt.Fprintf(&b, `<span class="held-item">`)
				fmt.Fprintf(&b,
					`<button class="btn secondary held-chip" hx-post="/api/pos/resume" hx-vals='{"id":%q}' hx-target="#basket" hx-swap="outerHTML">%s · %d × · %s%s</button>`,
					h.ID, template.HTMLEscapeString(h.Label), h.LineCount, template.HTMLEscapeString(total), tableChip)
				// Move-table control (ut-docs#820): only when at least one other
				// table is free to move onto -- a parked order can't "move" to a
				// table already occupied, and a shop with no free tables gets no
				// control rather than an empty menu. Posts to the existing,
				// IsTableFree-validated POST /api/pos/held/table; the handler
				// re-renders this whole strip, so the target is #held-sales.
				// Fresh slice per order -- never alias freeTables' backing
				// array, which is reused for every held item in this loop.
				//
				// ut-docs#1381: also hidden for a held Takeaway order, same
				// soft-gate ut-docs#1355 already applies to the live basket's
				// table picker (registerTablePicker) -- a table assignment
				// doesn't make sense for takeaway, and moving one onto a table
				// would just be undone the moment the order resumes (Restore no
				// longer wipes OrderType, so a takeaway order stays takeaway).
				// Reads OrderType from the held sale's own JSON payload -- it
				// has no dedicated held_sales column (unlike TableID), so this
				// is the only place it's currently readable from at this layer.
				moveTargets := make([]data.TableWithState, 0, len(freeTables))
				if heldSaleMayHaveTable(h.Payload) {
					for _, ft := range freeTables {
						if ft.ID == h.TableID {
							// The order's own current table is never a move target.
							continue
						}
						moveTargets = append(moveTargets, ft)
					}
				}
				if len(moveTargets) > 0 {
					fmt.Fprintf(&b, `<details class="held-move"><summary>%s</summary>`, template.HTMLEscapeString(httpx.T(locale, "basket.table.move")))
					for _, ft := range moveTargets {
						fmt.Fprintf(&b,
							`<button type="button" class="btn secondary held-move-option" hx-post="/api/pos/held/table" hx-vals='{"id":%q,"table_id":%q}' hx-target="#held-sales" hx-swap="outerHTML">%s</button>`,
							h.ID, ft.ID, template.HTMLEscapeString(ft.Label))
					}
					b.WriteString(`</details>`)
				}
				b.WriteString(`</span>`)
			}
		}
		b.WriteString(`</div>`)
		_, _ = w.Write([]byte(b.String()))
	}

	renderBasket := func(w http.ResponseWriter, r *http.Request, toast, level string) {
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		basketView, _ := ui.NewBasketView(funcs)
		b := d.Engine.Basket()
		b.ToastMessage = toast
		b.ToastLevel = level
		_ = basketView.Render(w, &b)
	}

	// Hold the current sale: snapshot → persist → clear the basket.
	mux.HandleFunc("POST /api/pos/hold", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		locale := httpx.ResolveLocale(w, r)
		if !d.Engine.HasItems() {
			renderBasket(w, r, httpx.T(locale, "hold.error.empty"), "error")
			return
		}
		_ = r.ParseForm()
		snap := d.Engine.Snapshot()
		payload, err := json.Marshal(snap)
		if err != nil {
			renderBasket(w, r, httpx.T(locale, "hold.error.failed"), "error")
			return
		}
		label := truncateRunes(strings.TrimSpace(r.Form.Get("label")), maxHoldLabelRunes)
		if label == "" {
			label = strings.TrimSpace(snap.CustomerName)
		}
		if label == "" {
			label = time.Now().Format("15:04")
		}
		held := data.HeldSale{
			ID:         fmt.Sprintf("hold-%d", time.Now().UnixNano()),
			Label:      label,
			TotalMinor: snap.Total.Minor(),
			LineCount:  len(snap.Lines),
			Payload:    string(payload),
			TableID:    snap.TableID,
		}
		if err := repo.Insert(ctx, held); err != nil {
			renderBasket(w, r, httpx.T(locale, "hold.error.failed"), "error")
			return
		}
		// The live claim the table pick wrote (ut-docs#1390) is deliberately
		// LEFT IN PLACE (ut-docs#1704): table_claims is the only occupancy
		// signal that reaches other tills (the #1703 write-through), while
		// held_sales never syncs -- releasing the claim here made a parked
		// order's table read free shop-wide for as long as it sat parked. The
		// claim now spans both the live-basket and the held stage of an order;
		// it moves with the order in POST /api/pos/held/table and is only
		// dropped when the order is tendered, reset, or its table cleared.
		d.Engine.Reset()
		w.Header().Set("HX-Trigger", "held-changed")
		renderBasket(w, r, httpx.T(locale, "hold.toast.held"), "success")
	})

	// Resume a held sale into the (empty) basket.
	mux.HandleFunc("POST /api/pos/resume", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		locale := httpx.ResolveLocale(w, r)
		_ = r.ParseForm()
		id := strings.TrimSpace(r.Form.Get("id"))
		if id == "" {
			renderBasket(w, r, httpx.T(locale, "hold.error.not_found"), "error")
			return
		}
		if d.Engine.HasItems() {
			renderBasket(w, r, httpx.T(locale, "hold.error.busy"), "error")
			return
		}
		held, found, err := repo.Get(ctx, id)
		if err != nil || !found {
			renderBasket(w, r, httpx.T(locale, "hold.error.not_found"), "error")
			return
		}
		var snap pos.BasketSnapshot
		if err := json.Unmarshal([]byte(held.Payload), &snap); err != nil {
			renderBasket(w, r, httpx.T(locale, "hold.error.failed"), "error")
			return
		}
		// The held_sales.table_id COLUMN -- not the payload snapshot -- is the
		// authoritative table assignment (ut-docs#820). POST /api/pos/held/table
		// moves a parked order by updating only that column, leaving the payload
		// holding the pre-move table; restoring the snapshot verbatim would
		// silently revert a moved order to its old table and tender the sale,
		// receipt and kitchen ticket against the wrong one. Re-resolve the label
		// here too, so a table renamed while the order was parked resumes under
		// its current name -- same ListTables-at-the-pages-layer choice the strip
		// makes, rather than trusting a label snapshotted at hold time.
		snap.TableID = held.TableID
		snap.TableLabel = ""
		if held.TableID != "" {
			if t, found, err := posRepo.GetTable(ctx, held.TableID); err == nil && found {
				snap.TableLabel = t.Label
			} else {
				snap.TableLabel = held.TableID
			}
		}
		// ut-docs#1390: occupancy moves back from the held_sales row (about
		// to be deleted) to a live claim for the resumed basket -- claimed
		// BEFORE the delete so the table never reads free in between, and
		// read back from the engine rather than held.TableID because
		// Restore itself enforces the Takeaway-clears-table invariant
		// (nothing to claim then). The empty basket being resumed INTO may
		// already have picked a table of its own (a pick needs no items);
		// Restore wipes that assignment, so its claim is released -- unless
		// it is the very table being resumed, in which case the existing
		// row simply stays ours. A failed/lost claim is logged, never fails
		// the resume: the sale is restored either way, same philosophy as
		// the Delete below.
		prevTable := d.Engine.TableID()
		d.Engine.Restore(snap)
		restoredTable := d.Engine.TableID()
		if restoredTable != "" && restoredTable != prevTable {
			if claimed, err := claimTableWriteThrough(ctx, d, posRepo, restoredTable); err != nil || !claimed {
				log.Printf("resume %s: re-claim table %s failed (claimed=%v): %v", id, restoredTable, claimed, err)
			}
		}
		if prevTable != restoredTable {
			releaseTableClaim(ctx, d, posRepo, prevTable)
		}
		if err := repo.Delete(ctx, id); err != nil {
			// The sale is restored either way; a stale row is the lesser evil.
			_ = err
		}
		w.Header().Set("HX-Trigger", "held-changed")
		renderBasket(w, r, httpx.T(locale, "hold.toast.resumed"), "success")
	})

	// Held-sales strip: chips the cashier taps to resume.
	mux.HandleFunc("GET /ui/held", renderHeldStrip)

	// Move a held (parked) order onto a different table (ut-docs#820) --
	// distinct from resuming it: the order stays parked; its table_id
	// changes and its table claim moves with it (ut-docs#1704). Rejects
	// moving onto a table another order already occupies (IsTableFree),
	// leaving the held sale untouched; a held sale may move back onto its
	// own current table (short-circuited below as a no-op, not a rejection).
	mux.HandleFunc("POST /api/pos/held/table", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		_ = r.ParseForm()
		id := strings.TrimSpace(r.Form.Get("id"))
		tableID := strings.TrimSpace(r.Form.Get("table_id"))
		if id == "" {
			renderHeldStrip(w, r)
			return
		}
		// ut-docs#1381: this is the actual enforcement point, not just
		// renderHeldStrip's UI soft-gate above -- same "not just the picker's
		// soft-gate" reasoning Service.SetTable's own comment gives, and for
		// the identical reason: this endpoint is reachable directly regardless
		// of what the strip currently renders (a stale pre-refresh page, or a
		// request replayed after the order's type changed). Mirrors
		// Service.SetTable exactly: force tableID to "" for a Takeaway held
		// order rather than rejecting the request outright, so "clear the
		// table" (tableID already "") still succeeds as a no-op either way.
		//
		// independent review (ut-docs#1381): fail CLOSED on a Get error, not
		// open -- the IsTableFree check just below already rejects the move
		// on its own error, and this gate is the actual enforcement point
		// (not just the strip's UI soft-gate), so silently skipping it on an
		// error would let a Takeaway order be assigned a table after all.
		held, found, err := repo.Get(ctx, id)
		if err != nil {
			renderHeldStrip(w, r)
			return
		}
		if found && !heldSaleMayHaveTable(held.Payload) {
			tableID = ""
		}
		// Same-table (or both-empty) move is a no-op, decided HERE rather than
		// left to IsTableFree's self-exclusion (ut-docs#1704): the parked
		// order's own table_claims row now persists through the hold, and
		// IsTableFree self-excludes the held_sales row ONLY, never a claim --
		// so asking it would read the order's own claim as someone else's and
		// wrongly refuse. Must sit AFTER the Takeaway gate above so a Takeaway
		// held order (tableID forced "", held.TableID "") lands here too, same
		// observable outcome as before. Nothing in table_claims changes.
		if tableID == held.TableID {
			w.Header().Set("HX-Trigger", "held-changed")
			renderHeldStrip(w, r)
			return
		}
		if tableID != "" {
			free, err := posRepo.IsTableFree(ctx, tableID, id)
			if err != nil || !free {
				renderHeldStrip(w, r)
				return
			}
		}
		// Claim the NEW table BEFORE committing the move (independent review,
		// ut-docs#1704): IsTableFree above is a LOCAL-only check, so on a
		// replica it cannot see a table another till holds through the #1703
		// primary-side claim -- only the write-through claim call itself gets
		// the authoritative cross-till answer. Claiming first, and refusing
		// the move outright on failure (same "occupied" rejection shape as
		// the free-check above), means a refused claim leaves BOTH the
		// held_sales row and the OLD table's claim exactly as they were --
		// never a state where the order is moved with no claim anywhere.
		// Mirrors pos_api.go's own live-basket table pick, which never lets
		// go of the current table over an unconfirmed new one.
		if found && tableID != "" {
			if claimed, err := claimTableWriteThrough(ctx, d, posRepo, tableID); err != nil || !claimed {
				log.Printf("move held %s: claim table %s failed (claimed=%v): %v", id, tableID, claimed, err)
				renderHeldStrip(w, r)
				return
			}
		}
		if err := repo.SetTable(ctx, id, tableID); err != nil {
			renderHeldStrip(w, r)
			return
		}
		// Release the OLD table's claim only once the move has actually
		// committed (ut-docs#1704): the held_sales row now carries the new
		// table, and this held sale's own claim on its FORMER table would
		// otherwise linger, wrongly reading it occupied to every other till.
		// Log-and-continue, never fails the request -- a stale claim on the
		// old table is the lesser evil versus refusing floor work over
		// bookkeeping (every other call site here takes the same stance).
		// Only for a held sale that actually exists -- SetTable on an
		// unknown id is a no-row UPDATE with nothing to release.
		if found {
			releaseTableClaim(ctx, d, posRepo, held.TableID)
		}
		w.Header().Set("HX-Trigger", "held-changed")
		renderHeldStrip(w, r)
	})
}

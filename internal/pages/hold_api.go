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

	// renderHeldStripWithToast builds the held-sales strip fragment -- the
	// shared body behind both GET /ui/held (first paint / hx-trigger=
	// "held-changed" re-fetch) and POST /api/pos/held/table (re-rendered in
	// place after a move, same as every other mutating handler here
	// re-renders its own fragment). Table labels are resolved via a single
	// ListTables call rather than teaching HeldSalesRepo about `tables` --
	// display-only joins like this stay at the pages layer, same choice
	// kitchenTicketFor makes for order-type text.
	//
	// toast/level (ut-docs#1704, independent review 2026-09-07): a rejected
	// move used to just re-render the strip unchanged -- no toast, no
	// HX-Trigger, nothing the cashier could see, the exact "silent success
	// that's actually a no-op" basket.table.occupied's own toast exists to
	// avoid on the live-basket picker (pos_api.go). Same `.pos-notice`
	// markup basket.html renders, so the shared document-level dismiss
	// handler (app.js) picks it up with no new wiring. renderHeldStrip
	// below is the plain (no-toast) case every other call site uses.
	renderHeldStripWithToast := func(w http.ResponseWriter, r *http.Request, toast, level string) {
		ctx := r.Context()
		locale := httpx.ResolveLocale(w, r)
		items, err := repo.List(ctx)
		if err != nil {
			items = nil
		}
		// tablesWithStateForDisplay (ut-docs#1392/#1704), not the bare
		// posRepo.ListTablesWithState, so the per-order "Move table" control
		// below can offer only tables that are actually free -- cross-till,
		// not just locally. Before this fix (independent review,
		// 2026-09-07) this was the one remaining display site still calling
		// ListTablesWithState directly, so the strip would happily offer a
		// table another till already held (live claim or its own parked
		// order), and the move below refused it with a plain re-render --
		// no toast, no HX-Trigger, nothing the cashier could see. A shop
		// with no tables configured yields an empty map/list, so no table
		// chrome renders at all (ADR-0054 soft-gate).
		labelByTableID := map[string]string{}
		var freeTables []data.TableWithState
		if states, err := tablesWithStateForDisplay(ctx, d, posRepo, time.Now().Add(-tillClaimTTL)); err == nil {
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
		if toast != "" {
			noticeClass := "info"
			role := "status"
			if level == "error" {
				noticeClass = "error"
				role = "alert"
			} else if level == "success" {
				noticeClass = "success"
			}
			fmt.Fprintf(&b, `<div class="pos-notice %s" id="toast-message" role="%s"><span class="notice-text">%s</span><button type="button" class="notice-dismiss" aria-label="%s">✕</button></div>`,
				noticeClass, role, template.HTMLEscapeString(toast), template.HTMLEscapeString(httpx.T(locale, "notice.dismiss")))
		}
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
	renderHeldStrip := func(w http.ResponseWriter, r *http.Request) {
		renderHeldStripWithToast(w, r, "", "")
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
		held := data.HeldSale{
			TotalMinor: snap.Total.Minor(),
			LineCount:  len(snap.Lines),
			Payload:    string(payload),
			TableID:    snap.TableID,
		}
		// ut-docs#1918: an order that was resumed from an existing held sale
		// (and not yet re-parked -- Engine.HeldOrigin is set only by the
		// resume handler's RestoreHeld and cleared by every basket reset)
		// goes back under the SAME id and first-parked time it had, via
		// Upsert -- one order, one identity, however many times it is
		// picked up and put down. The label fallback chain (typed label ->
		// customer name -> clock time) runs on a true first park; on a
		// re-park the ORIGINAL label is kept unless the cashier explicitly
		// typed a new one into the (still-shown) hold dialog -- a blank
		// field never re-derives a label from the clock or the customer,
		// but a genuine rename is honoured rather than silently discarded.
		if origin := d.Engine.HeldOrigin(); !origin.IsZero() {
			held.ID = origin.ID
			held.Label = origin.Label
			if typed := truncateRunes(strings.TrimSpace(r.Form.Get("label")), maxHoldLabelRunes); typed != "" {
				held.Label = typed
			}
			held.CreatedAt = origin.CreatedAt
			if err := repo.Upsert(ctx, held); err != nil {
				renderBasket(w, r, httpx.T(locale, "hold.error.failed"), "error")
				return
			}
		} else {
			label := truncateRunes(strings.TrimSpace(r.Form.Get("label")), maxHoldLabelRunes)
			if label == "" {
				label = strings.TrimSpace(snap.CustomerName)
			}
			if label == "" {
				label = time.Now().Format("15:04")
			}
			held.ID = fmt.Sprintf("hold-%d", time.Now().UnixNano())
			held.Label = label
			if err := repo.Insert(ctx, held); err != nil {
				renderBasket(w, r, httpx.T(locale, "hold.error.failed"), "error")
				return
			}
		}
		// ut-docs#1704: the live claim the table pick wrote (ut-docs#1390) is
		// deliberately KEPT here, not released -- it's the only signal that
		// makes a parked order's table occupancy visible cross-till, since
		// held_sales itself still isn't synced or proxied to the primary at
		// all. Locally this is harmless redundancy (ListTablesWithState
		// already unions held_sales and table_claims, and the held_sales row
		// alone was always enough for THIS till's own view). On a REPLICA
		// it's the whole fix: the claim was already write-through'd to the
		// primary the instant the table was picked (claimTableWriteThrough,
		// pos_api.go), and the primary's own GET /api/sync/tables already
		// serves it to every other till (ut-docs#1392) -- parking the order
		// needs no NEW proxy call at all, it just must not throw away the
		// one already made. Resume re-affirms the SAME row
		// (claimTableWriteThrough's own-claim re-take, tables_repo.go)
		// rather than re-claiming from scratch; the held/move handler below
		// is the one place that must move the claim explicitly, since that
		// changes WHICH table is occupied.
		//
		// Known, accepted limitation (same class as tables_claim_proxy.go's
		// own note): if THIS till goes dark for the full tillClaimTTL window
		// while an order sits parked, another till's claim attempt on the
		// SAME table may reconcile this row as orphaned and take it over --
		// bounded to that outage window, and the same tradeoff #1703 already
		// accepted for a live basket's claim, not a new one. A healthy
		// till's routine ~30s admin-sync poll keeps last_seen_at fresh
		// throughout an ordinary hold, however long the table sits parked.
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
		// ut-docs#1918: RestoreHeld, not Restore -- the resumed basket
		// remembers which row it came from (id, label, first-parked time),
		// so re-parking it lands under the same identity (see the hold
		// handler above). Remembered on the engine, not by skipping the
		// Delete below: the row still goes away while the order is live,
		// exactly as before, and comes back under the same id on re-park.
		d.Engine.RestoreHeld(snap, pos.HeldOrigin{ID: held.ID, Label: held.Label, CreatedAt: held.CreatedAt})
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
	// distinct from resuming it: the order stays parked, only its table_id
	// changes. Rejects moving onto a table another held sale already
	// occupies (IsTableFree), leaving the held sale untouched; a held sale
	// may move back onto its own current table as a true no-op.
	//
	// ut-docs#1704: since the hold handler now KEEPS the table_claims row a
	// held order's table was originally picked with (instead of releasing
	// it), this handler is the one place that must move that claim
	// explicitly when the table itself changes -- claim the NEW table
	// BEFORE committing the move (same claim-first-then-commit order
	// pos_api.go's own table-pick handler uses, for the identical reason:
	// held_sales must never say a table it hasn't actually secured), then
	// release the OLD one only once the move is confirmed. The self-move
	// case (tableID == held.TableID, including both empty) skips all of
	// this: the existing claim already correctly represents it, and
	// IsTableFree/claimTableWriteThrough must NOT be asked about it --
	// IsTableFree's own doc comment is explicit that any claim it sees is
	// "by construction someone else's" (it excludes a held sale's own
	// held_sales row, never a claim), so asking it about this held sale's
	// OWN claim on its OWN table would wrongly refuse the no-op.
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
		//
		// !found (ut-docs#1704) is now also a hard stop, not a fall-through:
		// once a claim can be taken here, letting a since-resumed/deleted
		// held sale's id reach that far would claim a real table on behalf
		// of nothing that will ever release it. SetTable's own no-op-on-
		// missing-id tolerance is still fine for everything before this
		// point; it just must never be reached.
		held, found, err := repo.Get(ctx, id)
		if err != nil || !found {
			renderHeldStrip(w, r)
			return
		}
		if !heldSaleMayHaveTable(held.Payload) {
			tableID = ""
		}
		if tableID != held.TableID {
			if tableID != "" {
				free, err := posRepo.IsTableFree(ctx, tableID, id)
				if err != nil || !free {
					// ut-docs#1704, independent review 2026-09-07: this used
					// to be a completely silent refusal -- 200, no
					// HX-Trigger, unchanged HTML, nothing a cashier tapping
					// an apparently-free table could see. Same
					// basket.table.occupied toast pos_api.go's own
					// live-basket table pick already renders for the
					// identical outcome.
					renderHeldStripWithToast(w, r, httpx.T(httpx.ResolveLocale(w, r), "basket.table.occupied"), "error")
					return
				}
				claimed, err := claimTableWriteThrough(ctx, d, posRepo, tableID)
				if err != nil {
					log.Printf("held table move %s: claim %s failed: %v", id, tableID, err)
				}
				if err != nil || !claimed {
					renderHeldStripWithToast(w, r, httpx.T(httpx.ResolveLocale(w, r), "basket.table.occupied"), "error")
					return
				}
			}
			if err := repo.SetTable(ctx, id, tableID); err != nil {
				if tableID != "" {
					// Commit failed after the claim was already taken -- undo
					// it, mirroring pos_api.go's own "SetTable refused: undo
					// the claim we just took" handling of the same situation.
					releaseTableClaim(ctx, d, posRepo, tableID)
				}
				renderHeldStrip(w, r)
				return
			}
			// Move confirmed -- only now let go of the old table's claim.
			// Logged loudly (Errorf, not the usual silent fire-and-forget)
			// specifically when this IS a replica and the PRIMARY-side
			// release still fails (independent review, ut-docs#1704):
			// unlike a live basket's release sites, nothing else ever
			// revisits this one table again until a manager notices and
			// taps Free table -- a leaked claim here is silent and
			// durable, not self-healing. Gated on isReplica so a
			// standalone/primary till -- releaseTableClaim's ordinary,
			// expected `false` there, every single move -- never logs a
			// false alarm about a primary that was never involved.
			_, _, isReplica := replicaSyncTarget(ctx, d)
			if held.TableID != "" {
				released := releaseTableClaim(ctx, d, posRepo, held.TableID)
				if isReplica && !released {
					log.Printf("held table move %s: table %s claim NOT released on the primary -- it will read occupied on other tills until a manager frees it", id, held.TableID)
				}
			}
		}
		w.Header().Set("HX-Trigger", "held-changed")
		renderHeldStrip(w, r)
	})
}

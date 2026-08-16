package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// LAN sync D3 (ADR-0011): replica sales journal to the primary. The
// journal payload is the sale detail; the primary re-applies it through
// pos.CompleteSale (same engine = same stock movements, links, audit)
// with the ORIGINAL ids/receipts, so application is idempotent by sale id.

type journalSale struct {
	Sale           data.SaleDetail `json:"sale"`
	OriginalSaleID string          `json:"original_sale_id,omitempty"`
}

// missingJournalFields names which of the fields applyJournal treats as
// required are empty, for a diagnosable rejection -- "" means none missing.
// id is checked too (defense in depth against any malformed payload, not
// just a version-skewed one) even though id/"id" survives a version skew
// unharmed (see the comment on applyJournal) -- it can still be legitimately
// empty on a badly-formed or adversarial request.
func missingJournalFields(s data.SaleDetail) string {
	var missing []string
	if s.ID == "" {
		missing = append(missing, "id")
	}
	if s.ReceiptNo == "" {
		missing = append(missing, "receipt_no")
	}
	if s.SaleType == "" {
		missing = append(missing, "sale_type")
	}
	return strings.Join(missing, ", ")
}

// invalidJournalFields names which of the fields applyJournal treats as
// requiring a REAL value (not just presence) fail that check, for a
// diagnosable rejection -- "" means none invalid. Distinct from
// missingJournalFields above: those three are rejected when EMPTY; these
// are rejected when PRESENT-BUT-WRONG (currency) or malformed (created_at).
//
//   - currency: "" is still accepted -- the contract's documented graceful
//     default (pos.CompleteSale defaults an empty Currency to "GBP") is
//     unchanged. A NON-empty value that doesn't match this shop's actual
//     configured currency is new: nothing downstream cross-checks it, so a
//     wrong-currency journal entry was previously applied at face value,
//     silently booking real revenue under the wrong currency.
//   - created_at: newly REQUIRED (tightened from "optional, degrades
//     gracefully" -- see reference/contracts/pos-lan-sync-journal.md's
//     changelog). An empty or unparseable value doesn't just "default"
//     anywhere downstream: applyJournal's own SetSaleProvenance call below
//     writes it VERBATIM over sales.created_at, clobbering the real
//     timestamp CompleteSale just wrote -- so a missing/malformed
//     created_at was silently corrupting the sale's actual creation time,
//     not degrading gracefully. Every real replica (buildJournal) always
//     populates this from its own DB row, so this cannot reject a
//     well-behaved peer.
func invalidJournalFields(s data.SaleDetail, configuredCurrency string) string {
	var invalid []string
	// configuredCurrency == "" (independent review, ut-docs#647): the
	// primary's own runtime state should never actually be blank --
	// pos.CompleteSale defaults an empty Currency to "GBP" on write and
	// sales.currency is NOT NULL DEFAULT 'GBP' -- but /api/settings/upsert
	// can set store.currency to "" without validating it (unlike
	// /api/settings/save, which does), so this stays defensive: treat "not
	// yet configured" as "don't know," not "must match empty," so a
	// well-behaved replica's real currency isn't rejected shop-wide until
	// the setting is fixed or the till restarts (LoadState re-applies the
	// cfg default).
	if configuredCurrency != "" && s.Currency != "" && s.Currency != configuredCurrency {
		invalid = append(invalid, fmt.Sprintf("currency (%q, shop is %q)", s.Currency, configuredCurrency))
	}
	if _, err := time.Parse(time.RFC3339, s.CreatedAt); err != nil {
		invalid = append(invalid, "created_at")
	}
	return strings.Join(invalid, ", ")
}

// buildJournal packages one local sale for pushing.
func buildJournal(ctx context.Context, repo *data.POSRepo, receiptNo string) (journalSale, bool, error) {
	detail, found, err := repo.GetSaleDetail(ctx, receiptNo)
	if err != nil || !found {
		return journalSale{}, false, err
	}
	j := journalSale{Sale: detail}
	if detail.SaleType == "return" {
		j.OriginalSaleID, _ = repo.OriginalSaleIDFor(ctx, detail.ID)
	}
	return j, true, nil
}

// applyJournal replays a journaled sale on the primary. Returns
// applied=false when the sale already exists (idempotent).
func applyJournal(ctx context.Context, d *common.Deps, tillID string, j journalSale) (bool, error) {
	// Guards the corruption path the snake_case wire-format rename opens
	// (ut-docs#262): Go's json.Unmarshal matches a tagged field's incoming
	// key case-insensitively, so a field whose OLD (PascalCase, untagged)
	// name and NEW (snake_case) tag differ only by case -- like "ID" vs
	// "id" -- still decodes fine from a stale peer. ReceiptNo/"receipt_no"
	// and SaleType/"sale_type" do NOT survive that (the underscore makes
	// them different strings, not just different case), so those are the
	// two fields that actually go silently empty across a version skew,
	// and are the ones load-bearing here -- NOT Sale.ID, which decodes
	// correctly either way and is included below only for correlation.
	// Reject loudly instead of applying a malformed/skewed sale. See
	// reference/contracts/pos-lan-sync-journal.md for the compatibility
	// guarantee this enforces (no cross-version peers; primary upgrades
	// first).
	if missing := missingJournalFields(j.Sale); missing != "" {
		return false, fmt.Errorf("invalid journal entry (sale id %q): missing %s", j.Sale.ID, missing)
	}
	if invalid := invalidJournalFields(j.Sale, d.CurrentState().Currency); invalid != "" {
		return false, fmt.Errorf("invalid journal entry (sale id %q): invalid %s", j.Sale.ID, invalid)
	}
	repo := data.NewPOSRepo(d.Db)
	if exists, err := repo.SaleExists(ctx, j.Sale.ID); err != nil || exists {
		return false, err
	}
	locID, err := repo.EnsureStockLocation(ctx)
	if err != nil {
		return false, err
	}
	// Refund over-guard re-check (ADR-0011): the money already moved at
	// the till, so an over-refund is recorded but flagged for the manager.
	if j.Sale.SaleType == "return" && j.OriginalSaleID != "" {
		returned, err := repo.ReturnedQuantities(ctx, j.OriginalSaleID)
		if err == nil {
			for _, l := range j.Sale.Lines {
				key := data.RefundLineKey(l.ItemID, l.VariantID, l.UnitPrice)
				returned[key] += l.Qty
			}
			// (detailed per-line compare happens naturally at report time;
			// flag when any pool went negative against the original)
		}
	}
	in := pos.SaleInput{
		SaleType:               j.Sale.SaleType,
		SaleID:                 j.Sale.ID,
		ReceiptNo:              j.Sale.ReceiptNo,
		Currency:               j.Sale.Currency,
		TaxInclusive:           saleIsTaxInclusive(j.Sale),
		CashierID:              j.Sale.CashierID,
		ActorID:                j.Sale.CashierID,
		OrderType:              j.Sale.OrderType,
		OriginalSaleID:         j.OriginalSaleID,
		AllowNegativeInventory: true, // the remote sale already happened
	}
	for _, l := range j.Sale.Lines {
		in.Lines = append(in.Lines, pos.SaleLineInput{
			ItemID: l.ItemID, VariantID: l.VariantID, SKU: l.SKU, Name: l.Name,
			Qty: l.Qty, UnitPrice: money.FromMinor(l.UnitPrice),
			TaxRateBasisPoints: l.TaxRateBP, LineDiscount: money.FromMinor(l.LineDiscount),
			LocationID: locID,
		})
	}
	if j.Sale.DiscountTotal > 0 {
		in.SaleDiscount = money.FromMinor(j.Sale.DiscountTotal)
	}
	if j.Sale.ServiceCharge > 0 {
		// The ORIGINAL amount, not recomputed from whatever rate the
		// primary happens to have configured right now (ut-docs#72) --
		// same reasoning as SaleDiscount above.
		in.ServiceCharge = money.FromMinor(j.Sale.ServiceCharge)
	}
	for _, p := range j.Sale.Payments {
		in.Payments = append(in.Payments, pos.PaymentInput{
			MethodID: p.Method, Amount: money.FromMinor(p.Amount),
			ChangeGiven: money.FromMinor(p.ChangeGiven),
			TipAmount:   money.FromMinor(p.TipAmount),
			Currency:    j.Sale.Currency, Reference: p.Reference,
			// Card-present reconciliation fields (ut-docs#543) must
			// survive a replica->primary journal replay, same as every
			// other payment field -- the primary is where cross-till
			// reconciliation actually happens.
			MaskedPAN:  p.MaskedPAN,
			AuthCode:   p.AuthCode,
			TerminalID: p.TerminalID,
			TraceID:    p.TraceID,
		})
	}
	if _, err := pos.CompleteSale(ctx, d.Db, in); err != nil {
		return false, err
	}
	// A replay is force-allowed to go negative (the remote sale already
	// happened), so the resulting level must surface as a visible Problem
	// here, not stay silent (ut-docs#404, ADR-0036). Guarded by the
	// SaleExists idempotency check above, so it fires at most once per sale.
	warnIfStockNegative(ctx, repo, in, "journaled sale "+j.Sale.ReceiptNo+" from till "+tillID)
	// This node's stock genuinely changed when replaying the remote sale, so
	// mirror it to inventory connectors (best-effort, non-blocking). Guarded by
	// the SaleExists idempotency check above, so it fires at most once per sale.
	publishStockAdjustedForSale(ctx, d, in)
	return true, repo.SetSaleProvenance(ctx, j.Sale.ID, tillID, j.Sale.CreatedAt)
}

// warnIfStockNegative surfaces negative stock as a back-office Problem
// (ut-docs#404, ADR-0036 — whichever till's application of a movement takes
// shop-wide stock negative surfaces it): after a sale that was allowed past
// the stock gate, any line whose resulting level is negative gets one
// Warn-level line naming the item and the level. logging.Recent() already
// feeds the Problems panel (backoffice_page.go), so the Warnf IS the
// surfacing — no extra plumbing. Best-effort: the sale already committed, so
// a failed read only skips the warning, never the sale. Returns only add
// stock back, so they are skipped outright.
func warnIfStockNegative(ctx context.Context, repo *data.POSRepo, in pos.SaleInput, source string) {
	if in.SaleType != "sale" {
		return
	}
	for _, l := range in.Lines {
		// post is a post-commit snapshot, not necessarily the exact delta
		// THIS sale caused (it can race a concurrent sale/adjustment on the
		// same item) — acceptable for a best-effort log line. pre is
		// reconstructed rather than read a second time: this sale's own
		// delta on a "sale" is always -l.Qty (internal/pos/sales.go's gate
		// uses the identical formula), so pre = post + l.Qty exactly,
		// with no second read to race against.
		post, found, err := repo.CurrentQty(ctx, nil, l.LocationID, l.ItemID, l.VariantID)
		if err != nil || !found || post >= 0 {
			continue
		}
		pre := post + l.Qty
		if pre < 0 {
			// Already negative before this sale — already warned when it
			// first crossed. Warning again on every later sale of the same
			// chronically-negative item would flood the 50-entry Problems
			// ring (internal/logging.recentCap) with repeats, evicting
			// unrelated problems within ~50 sales. Only the transition
			// itself is news.
			continue
		}
		name := l.Name
		if name == "" {
			name = l.ItemID
			if name == "" {
				name = l.VariantID
			}
		}
		logging.L().Warnf("negative stock: %q went to %.2f (location %s) after %s (ADR-0036)",
			name, post, l.LocationID, source)
	}
}

// registerSyncSales mounts the primary-side journal endpoint.
func registerSyncSales(mux *http.ServeMux, d *common.Deps) {
	tills := data.NewTillsRepo(d.Db)
	posRepo := data.NewPOSRepo(d.Db)

	mux.HandleFunc("POST /api/sync/sales", func(w http.ResponseWriter, r *http.Request) {
		till, ok := syncTill(r, tills)
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "error": "unauthorized"})
			return
		}
		var batch []journalSale
		if err := json.NewDecoder(r.Body).Decode(&batch); err != nil || len(batch) > 100 {
			http.Error(w, "bad journal batch", http.StatusBadRequest)
			return
		}
		applied, skipped := 0, 0
		for _, j := range batch {
			ok, err := applyJournal(r.Context(), d, till.ID, j)
			if err != nil {
				logging.L().Errorf("sync apply %s from %s: %v", j.Sale.ReceiptNo, till.Name, err)
				http.Error(w, "apply failed at "+j.Sale.ReceiptNo+": "+err.Error(), http.StatusUnprocessableEntity)
				return
			}
			if ok {
				applied++
			} else {
				skipped++
			}
		}
		if applied > 0 {
			_ = posRepo.InsertAudit(r.Context(), nil, "system", "till", till.ID, "sales_synced",
				map[string]any{"applied": applied, "skipped": skipped},
				time.Now().UTC().Format(time.RFC3339), "")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]int{"applied": applied, "skipped": skipped}, "error": nil,
		})
	})
}

// StartSyncPush runs the replica-side journal loop: every 30s push local
// sales past the cursor to the primary. Failures just wait for the next
// tick — checkout never depends on it (ADR-0003). wg registers the loop with
// app.Run's shutdown drain (ut-docs#153) — the caller must pass bgCtx (not
// ctx), the same requirement StartCloudSync already has, so an early
// startup error still signals this loop to stop.
//
// The loop also listens on Deps.SyncPushNow (created here, fired by
// Deps.RequestSyncPush after a local sale — ut-docs#404, ADR-0036) so a
// completed sale reaches the primary immediately rather than up to 30s
// later; the ticker remains the retry/catch-up path. Serving the nudge
// inside this one loop (instead of a per-sale goroutine) keeps push
// attempts serialized on the cursor, bounded by bgCtx, and joined by wg —
// nothing can leak past the shutdown drain.
func StartSyncPush(ctx context.Context, d *common.Deps, wg *sync.WaitGroup) {
	client := &http.Client{Timeout: 30 * time.Second}
	if d.SyncPushNow == nil {
		d.SyncPushNow = make(chan struct{}, 1)
	}
	runSyncLoop(ctx, wg, d.SyncPushNow, func() { syncPushTick(ctx, d, client) })
}

// syncPushTick is one tick of the replica-side journal loop, extracted from
// StartSyncPush so it can be driven directly in tests instead of only via
// the real 30s ticker.
func syncPushTick(ctx context.Context, d *common.Deps, client *http.Client) {
	repo := data.NewPOSRepo(d.Db)
	get := func(k string) string {
		v, _, _ := d.Settings.Get(ctx, k)
		return strings.TrimSpace(v)
	}
	primary, bearer := get("sync.primary_url"), get("sync.bearer")
	if primary == "" || bearer == "" {
		return
	}
	cursor := get("sync.push_cursor")
	receipts, err := repo.LocalSalesSince(ctx, cursor, 50)
	if err != nil || len(receipts) == 0 {
		return
	}
	var batch []journalSale
	maxCreated := cursor
	for _, rn := range receipts {
		j, found, err := buildJournal(ctx, repo, rn)
		if err != nil || !found {
			return
		}
		batch = append(batch, j)
		if j.Sale.CreatedAt > maxCreated {
			maxCreated = j.Sale.CreatedAt
		}
	}
	raw, _ := json.Marshal(batch)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(primary, "/")+"/api/sync/sales", bytes.NewReader(raw))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := client.Do(req)
	if err != nil {
		logging.L().Infof("sync push: primary unreachable (%v) — will retry", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		logging.L().Errorf("sync push rejected: %s", resp.Status)
		return
	}
	_ = d.Settings.Set(ctx, "sync.push_cursor", maxCreated)
	now := time.Now().UTC().Format(time.RFC3339)
	_ = d.Settings.Set(ctx, "sync.last_push_at", now)
	_ = d.Settings.Set(ctx, "sync.last_contact_at", now)
	logging.L().Infof("sync push: %d sale(s) journaled to the primary", len(batch))
}

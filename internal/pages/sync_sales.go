package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
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
	for _, p := range j.Sale.Payments {
		in.Payments = append(in.Payments, pos.PaymentInput{
			MethodID: p.Method, Amount: money.FromMinor(p.Amount),
			ChangeGiven: money.FromMinor(p.ChangeGiven),
			TipAmount:   money.FromMinor(p.TipAmount),
			Currency:    j.Sale.Currency, Reference: p.Reference,
		})
	}
	if _, err := pos.CompleteSale(ctx, d.Db, in); err != nil {
		return false, err
	}
	// This node's stock genuinely changed when replaying the remote sale, so
	// mirror it to inventory connectors (best-effort, non-blocking). Guarded by
	// the SaleExists idempotency check above, so it fires at most once per sale.
	publishStockAdjustedForSale(ctx, d, in)
	return true, repo.SetSaleProvenance(ctx, j.Sale.ID, tillID, j.Sale.CreatedAt)
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
// tick — checkout never depends on it (ADR-0003).
func StartSyncPush(ctx context.Context, d *common.Deps) {
	client := &http.Client{Timeout: 30 * time.Second}
	runSyncLoop(ctx, func() { syncPushTick(ctx, d, client) })
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

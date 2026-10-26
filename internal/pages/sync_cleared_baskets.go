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
)

// LAN sync D3 for baskets cleared with New sale (ut-docs#4711): a replica
// journals its own cleared_baskets rows to the primary, which stores them
// with till_id = the reporting till's tills.id, so the main till's Z report
// counts every till's clears, not just its own. Mirrors the no-sale journal
// (sync_no_sales.go, ut-docs#3562): idempotent by id, snake_case, ≤100
// entries.

// journalClearedBasket is one cleared basket on the wire. There is
// deliberately no till_id: the primary takes the till from the authenticated
// bearer and never trusts a peer-supplied one.
type journalClearedBasket struct {
	ID         string `json:"id"`
	CreatedAt  string `json:"created_at"`
	RegisterID string `json:"register_id"`
	ActorID    string `json:"actor_id"`
	LineCount  int    `json:"line_count"`
	TotalMinor int64  `json:"total_minor"`
}

const (
	// clearedBasketJournalMaxBatch caps one POST /api/sync/cleared-baskets
	// body, the /api/sync/sales limit.
	clearedBasketJournalMaxBatch = 100
	// clearedBasketPushBatch is how many clears one replica push tick sends.
	clearedBasketPushBatch = 50
	// clearedBasketJournalMaxLines bounds line_count. A basket is priced
	// lines plus add-by-hand lines; 10000 is far above anything a cashier
	// rings up by hand and only stops an absurd peer value skewing the
	// stored row.
	clearedBasketJournalMaxLines = 10000
	// clearedBasketPushCursorKey holds the replica's "<created_at>|<id>" cursor.
	clearedBasketPushCursorKey = "sync.cleared_basket_push_cursor"
)

// invalidJournalClearedBasket names what is wrong with a journaled clear
// ("" when valid). External input: every field is bounded before it is
// stored. total_minor must be ≥ 0: a basket total is floored at zero
// (pos.computeTotals), so a negative one is never legitimate.
func invalidJournalClearedBasket(j journalClearedBasket) string {
	var bad []string
	if j.ID == "" || len(j.ID) > noSaleJournalMaxIDBytes {
		bad = append(bad, "id")
	}
	if _, err := time.Parse(time.RFC3339, j.CreatedAt); err != nil {
		bad = append(bad, "created_at")
	}
	if len(j.RegisterID) > noSaleJournalMaxIDBytes {
		bad = append(bad, "register_id")
	}
	if len(j.ActorID) > noSaleJournalMaxIDBytes {
		bad = append(bad, "actor_id")
	}
	if j.LineCount < 0 || j.LineCount > clearedBasketJournalMaxLines {
		bad = append(bad, "line_count")
	}
	if j.TotalMinor < 0 {
		bad = append(bad, "total_minor")
	}
	return strings.Join(bad, ", ")
}

// registerSyncClearedBaskets mounts the primary-side cleared-basket journal
// endpoint. Failure semantics are registerSyncNoSales's: an invalid entry is
// skipped and counted as rejected, never failing the batch; a DB error
// rejects the whole batch (422) so the replica retries it next tick.
func registerSyncClearedBaskets(mux *http.ServeMux, d *common.Deps) {
	tills := data.NewTillsRepo(d.Db)
	posRepo := data.NewPOSRepo(d.Db)

	mux.HandleFunc("POST /api/sync/cleared-baskets", func(w http.ResponseWriter, r *http.Request) {
		till, ok := syncTill(r, tills)
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "error": "unauthorized"})
			return
		}
		// ut-docs#5058: refused by a replaced main till, kept queued.
		if refuseIfReplaced(w, r, d) {
			return
		}
		var batch []journalClearedBasket
		if err := json.NewDecoder(r.Body).Decode(&batch); err != nil || len(batch) > clearedBasketJournalMaxBatch {
			common.LocalizedError(w, r, http.StatusBadRequest, "sync.error.bad_batch")
			return
		}
		applied, skipped, rejected := 0, 0, 0
		for _, j := range batch {
			if bad := invalidJournalClearedBasket(j); bad != "" {
				logging.L().Warnf("cleared-basket journal entry %q from till %s (%s) rejected: invalid %s — skipping (ut-docs#4711)",
					j.ID, till.Name, till.ID, bad)
				rejected++
				continue
			}
			ok, err := posRepo.ApplyJournaledClearedBasket(r.Context(), data.ClearedBasket{
				ID: j.ID, CreatedAt: j.CreatedAt, RegisterID: j.RegisterID, TillID: till.ID,
				ActorID: j.ActorID, LineCount: j.LineCount, Total: money.FromMinor(j.TotalMinor),
			})
			if err != nil {
				logging.L().Errorf("sync cleared-basket apply %s from %s: %v", j.ID, till.Name, err)
				common.LocalizedError(w, r, http.StatusUnprocessableEntity, "sync.error.apply_failed")
				return
			}
			if ok {
				applied++
			} else {
				skipped++
			}
		}
		if applied > 0 || rejected > 0 {
			_ = posRepo.InsertAudit(r.Context(), nil, "system", "till", till.ID, "cleared_baskets_synced",
				map[string]any{"applied": applied, "skipped": skipped, "rejected": rejected},
				time.Now().UTC().Format(time.RFC3339), "")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]int{"applied": applied, "skipped": skipped, "rejected": rejected}, "error": nil,
		})
	})
}

// syncPushClearedBaskets is the replica's cleared-basket journal step of
// syncPushTick: push this till's own clears past the cursor (the same
// "<created_at>|<id>" form as the no-sale cursor, parseNoSalePushCursor) to
// the primary, and advance it only on a 200. Any other answer — including
// 404/401 from an older primary that doesn't know the route — leaves the
// cursor where it is, so the clears are retried next tick rather than dropped.
func syncPushClearedBaskets(ctx context.Context, d *common.Deps, client *http.Client, primary, bearer string) {
	repo := data.NewPOSRepo(d.Db)
	cur, _, _ := d.Settings.Get(ctx, clearedBasketPushCursorKey)
	afterAt, afterID := parseNoSalePushCursor(strings.TrimSpace(cur))
	clears, err := repo.LocalClearedBasketsSince(ctx, afterAt, afterID, clearedBasketPushBatch)
	if err != nil {
		logging.L().Errorf("sync cleared-basket push: read local clears: %v", err)
		return
	}
	if len(clears) == 0 {
		return
	}
	batch := make([]journalClearedBasket, 0, len(clears))
	for _, e := range clears {
		batch = append(batch, journalClearedBasket{
			ID: e.ID, CreatedAt: e.CreatedAt, RegisterID: e.RegisterID,
			ActorID: e.ActorID, LineCount: e.LineCount, TotalMinor: e.Total.Minor(),
		})
	}
	raw, err := json.Marshal(batch)
	if err != nil {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(primary, "/")+"/api/sync/cleared-baskets", bytes.NewReader(raw))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := client.Do(req)
	if err != nil {
		logging.L().Infof("sync cleared-basket push: primary unreachable (%v) — will retry", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Warn, not Error: a primary older than this endpoint answers 401/404
		// on every tick until it upgrades (the contract's skew window), and
		// Error lines fill the Problems panel.
		logging.L().Warnf("sync cleared-basket push rejected: %s — will retry", resp.Status)
		return
	}
	last := clears[len(clears)-1]
	_ = d.Settings.Set(ctx, clearedBasketPushCursorKey, last.CreatedAt+"|"+last.ID)
	// A successful push is contact with the main till, as for sales.
	now := recordMainContact(ctx, d, time.Now())
	_ = d.Settings.Set(ctx, "sync.last_push_at", now)
	logging.L().Infof("sync push: %d cleared basket(s) journaled to the primary", len(batch))
}

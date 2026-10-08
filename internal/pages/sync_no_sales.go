package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// LAN sync D3 for no-sale drawer opens (ut-docs#3562): a replica journals
// its own no_sale_events rows to the primary, which stores them with
// till_id = the reporting till's tills.id — the same key a journaled sale
// gets (SetSaleProvenance) — so the primary's ADR-0111 sales-aggregate
// rollup counts every till's opens, not just its own. Idempotent by event
// id. Same wire shape rules as /api/sync/sales: snake_case, ≤100 entries.

// journalNoSale is one no-sale open on the wire. There is deliberately no
// till_id: the primary takes the till from the authenticated bearer and
// never trusts a peer-supplied one.
type journalNoSale struct {
	ID         string `json:"id"`
	CreatedAt  string `json:"created_at"`
	RegisterID string `json:"register_id"`
	ActorID    string `json:"actor_id"`
	ApproverID string `json:"approver_id"`
	Reason     string `json:"reason"`
}

const (
	// noSaleJournalMaxBatch caps one POST /api/sync/no-sales body, the
	// /api/sync/sales limit.
	noSaleJournalMaxBatch = 100
	// noSalePushBatch is how many opens one replica push tick sends.
	noSalePushBatch = 50
	// noSaleJournalMaxIDBytes bounds the id-like fields of a journaled open.
	noSaleJournalMaxIDBytes = 64
	// noSalePushCursorKey holds the replica's "<created_at>|<id>" cursor.
	noSalePushCursorKey = "sync.no_sale_push_cursor"
)

// invalidJournalNoSale names what is wrong with a journaled open ("" when
// valid). External input: every field is bounded before it is stored.
func invalidJournalNoSale(j journalNoSale) string {
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
	if len(j.ApproverID) > noSaleJournalMaxIDBytes {
		bad = append(bad, "approver_id")
	}
	if utf8.RuneCountInString(j.Reason) > noSaleReasonMaxRunes {
		bad = append(bad, "reason")
	}
	return strings.Join(bad, ", ")
}

// registerSyncNoSales mounts the primary-side no-sale journal endpoint.
//
// An invalid entry is skipped and counted as rejected (with a logged
// warning), never failing the whole batch: a malformed entry would fail
// identically forever and wedge the replica's cursor (ADR-0065's reasoning
// for /api/sync/sales). A DB error rejects the whole batch (422) so the
// replica retries it next tick.
func registerSyncNoSales(mux *http.ServeMux, d *common.Deps) {
	tills := data.NewTillsRepo(d.Db)
	posRepo := data.NewPOSRepo(d.Db)

	mux.HandleFunc("POST /api/sync/no-sales", func(w http.ResponseWriter, r *http.Request) {
		till, ok := syncTill(r, tills)
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "error": "unauthorized"})
			return
		}
		var batch []journalNoSale
		if err := json.NewDecoder(r.Body).Decode(&batch); err != nil || len(batch) > noSaleJournalMaxBatch {
			common.LocalizedError(w, r, http.StatusBadRequest, "sync.error.bad_batch")
			return
		}
		applied, skipped, rejected := 0, 0, 0
		for _, j := range batch {
			if bad := invalidJournalNoSale(j); bad != "" {
				logging.L().Warnf("no-sale journal entry %q from till %s (%s) rejected: invalid %s — skipping (ut-docs#3562)",
					j.ID, till.Name, till.ID, bad)
				rejected++
				continue
			}
			ok, err := posRepo.ApplyJournaledNoSaleEvent(r.Context(), data.NoSaleEvent{
				ID: j.ID, CreatedAt: j.CreatedAt, RegisterID: j.RegisterID, TillID: till.ID,
				ActorID: j.ActorID, ApproverID: j.ApproverID, Reason: j.Reason,
			})
			if err != nil {
				logging.L().Errorf("sync no-sale apply %s from %s: %v", j.ID, till.Name, err)
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
			_ = posRepo.InsertAudit(r.Context(), nil, "system", "till", till.ID, "no_sales_synced",
				map[string]any{"applied": applied, "skipped": skipped, "rejected": rejected},
				time.Now().UTC().Format(time.RFC3339), "")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]int{"applied": applied, "skipped": skipped, "rejected": rejected}, "error": nil,
		})
	})
}

// parseNoSalePushCursor splits the stored "<created_at>|<id>" cursor; ""
// means from the beginning.
func parseNoSalePushCursor(s string) (createdAt, id string) {
	createdAt, id, _ = strings.Cut(s, "|")
	return createdAt, id
}

// syncPushNoSales is the replica's no-sale journal step of syncPushTick: push
// this till's own opens past the cursor to the primary, and advance the
// cursor only on a 200. Any other answer — including 404/401 from an older
// primary that doesn't know the route — leaves the cursor where it is, so
// the opens are retried next tick rather than dropped.
func syncPushNoSales(ctx context.Context, d *common.Deps, client *http.Client, primary, bearer string) {
	repo := data.NewPOSRepo(d.Db)
	cur, _, _ := d.Settings.Get(ctx, noSalePushCursorKey)
	afterAt, afterID := parseNoSalePushCursor(strings.TrimSpace(cur))
	events, err := repo.LocalNoSaleEventsSince(ctx, afterAt, afterID, noSalePushBatch)
	if err != nil {
		logging.L().Errorf("sync no-sale push: read local opens: %v", err)
		return
	}
	if len(events) == 0 {
		return
	}
	batch := make([]journalNoSale, 0, len(events))
	for _, e := range events {
		batch = append(batch, journalNoSale{
			ID: e.ID, CreatedAt: e.CreatedAt, RegisterID: e.RegisterID,
			ActorID: e.ActorID, ApproverID: e.ApproverID, Reason: e.Reason,
		})
	}
	raw, err := json.Marshal(batch)
	if err != nil {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(primary, "/")+"/api/sync/no-sales", bytes.NewReader(raw))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := client.Do(req)
	if err != nil {
		logging.L().Infof("sync no-sale push: primary unreachable (%v) — will retry", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Warn, not Error: a primary older than this endpoint answers 401/404
		// on every tick until it upgrades (the contract's skew window), and
		// Error lines fill the Problems panel.
		logging.L().Warnf("sync no-sale push rejected: %s — will retry", resp.Status)
		return
	}
	last := events[len(events)-1]
	_ = d.Settings.Set(ctx, noSalePushCursorKey, last.CreatedAt+"|"+last.ID)
	// A successful push is contact with the main till, as for sales.
	now := recordMainContact(ctx, d, time.Now())
	_ = d.Settings.Set(ctx, "sync.last_push_at", now)
	logging.L().Infof("sync push: %d no-sale open(s) journaled to the primary", len(batch))
}

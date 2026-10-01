package data

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// Age-restricted sales (ut-docs#3340): the items.age_restricted lookup the
// till's tender gate and the self-order kiosk's checkout refusal consult,
// and the append-only age_verifications log (056_age_verifications.sql).
// Split into its own file rather than growing pos_repo.go further, the same
// call shrinkage_repo.go made — still *POSRepo methods, not a new repo
// type: this is the same repository POSRepo already owns the sale-time
// writes on, just physically split by file.

// InsertAgeVerification records one age_verifications row. All-args-
// explicit and transaction-aware, mirroring InsertShrinkageEvent's own
// parameter style — tx may be nil (a direct, non-transactional write,
// exec(nil) resolves to r.db), but the production caller
// (pos.CompleteSale) always passes the sale's own transaction, so the
// verification and the sale it belongs to commit or roll back together.
//
// saleID is the real sales.id the check was recorded with; itemName is the
// item's name AT CHECK TIME (a snapshot, never re-read). itemID/cashierID
// empty store NULL; id empty generates a fresh uuid, same as
// InsertShrinkageEvent. outcome is persisted as given — the caller
// validates it (pos.ValidAgeVerificationOutcome) and the schema's CHECK
// constraint refuses anything outside 'accepted'/'refused' regardless.
func (r *POSRepo) InsertAgeVerification(
	ctx context.Context, tx *sql.Tx,
	saleID, itemID, itemName, outcome, cashierID string,
	createdAt, id string,
) error {
	if id == "" {
		id = uuid.NewString()
	}
	_, err := r.exec(tx).ExecContext(ctx, `
INSERT INTO age_verifications (
    id, sale_id, item_id, item_name, outcome, cashier_id, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?)
`, id, nullIfEmpty(saleID), nullIfEmpty(itemID), itemName, outcome, nullIfEmpty(cashierID), createdAt)
	if err != nil {
		return fmt.Errorf("insert age_verifications: %w", err)
	}
	return nil
}

// AgeRestrictedItemIDs reports which of itemIDs are flagged
// items.age_restricted = 1, as a set (only restricted ids are present).
// Empty and duplicate ids are ignored; an unknown id is simply absent. One
// batched query regardless of basket size. It reads the item's CURRENT
// flag, which makes it the authoritative backstop at tender/checkout time:
// a basket line restored from an older held sale (whose snapshot predates
// the flag) is still caught here.
func (r *POSRepo) AgeRestrictedItemIDs(ctx context.Context, itemIDs []string) (map[string]bool, error) {
	out := map[string]bool{}
	seen := make(map[string]bool, len(itemIDs))
	args := make([]any, 0, len(itemIDs))
	for _, id := range itemIDs {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		args = append(args, id)
	}
	if len(args) == 0 {
		return out, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")
	rows, err := r.db.QueryContext(ctx, `SELECT id FROM items WHERE age_restricted = 1 AND id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("age restricted items: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan age restricted item: %w", err)
		}
		out[id] = true
	}
	return out, rows.Err()
}

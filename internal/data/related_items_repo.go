package data

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// RelatedItemsRepo maintains the related_items co-occurrence table
// (migration 009) — the local engine behind "customers also buy"
// suggestions (docs: architecture/ai-integration.md §h). Everything here is
// plain SQL over the shop's own sales history; no model, no network.
type RelatedItemsRepo struct {
	db *sql.DB
}

func NewRelatedItemsRepo(db *sql.DB) *RelatedItemsRepo {
	return &RelatedItemsRepo{db: db}
}

var relatedItemsObs = newRepoObservability("related_items")

const (
	// relatedMinSupport: a pair must co-occur in at least this many baskets
	// before it is suggested — filters one-off coincidences.
	relatedMinSupport = 2
	// relatedKeepPerItem caps stored neighbours per item; suggestions blend
	// scores across the whole basket so a short list per item is plenty.
	relatedKeepPerItem = 12
	// relatedWindowDays bounds the history scanned so tastes stay current
	// and the rebuild stays cheap on long-lived tills.
	relatedWindowDays = 180
)

// Rebuild recomputes the whole co-occurrence table from completed sales in
// the recent window. Score is cosine²: support² / (baskets_a × baskets_b) —
// same ranking as cosine similarity without needing sqrt(), and it demotes
// items that appear in almost every basket (carrier bags).
func (r *RelatedItemsRepo) Rebuild(ctx context.Context) (int64, error) {
	var err error
	done := relatedItemsObs.trace("rebuild")
	defer func() { done(err) }()

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, relatedItemsObs.wrap("rebuild", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err = tx.ExecContext(ctx, `DELETE FROM related_items`); err != nil {
		return 0, relatedItemsObs.wrap("rebuild", err)
	}

	res, err := tx.ExecContext(ctx, `
INSERT INTO related_items (item_id, related_item_id, support, score)
WITH sale_items AS (
    SELECT DISTINCT sl.sale_id, COALESCE(sl.item_id, iv.item_id) AS item_id
    FROM sale_lines sl
    JOIN sales s ON s.id = sl.sale_id
    LEFT JOIN item_variants iv ON iv.id = sl.variant_id
    WHERE COALESCE(sl.item_id, iv.item_id) IS NOT NULL
      AND s.status = 'completed'
      AND s.created_at >= datetime('now', ?)
),
item_counts AS (
    SELECT item_id, COUNT(*) AS baskets
    FROM sale_items
    GROUP BY item_id
),
pairs AS (
    SELECT a.item_id AS item_id, b.item_id AS related_item_id, COUNT(*) AS support
    FROM sale_items a
    JOIN sale_items b ON b.sale_id = a.sale_id AND b.item_id <> a.item_id
    GROUP BY a.item_id, b.item_id
    HAVING COUNT(*) >= ?
),
ranked AS (
    SELECT p.item_id, p.related_item_id, p.support,
           (CAST(p.support AS REAL) * p.support) / (ca.baskets * cb.baskets) AS score,
           ROW_NUMBER() OVER (
               PARTITION BY p.item_id
               ORDER BY (CAST(p.support AS REAL) * p.support) / (ca.baskets * cb.baskets) DESC,
                        p.support DESC, p.related_item_id
           ) AS rn
    FROM pairs p
    JOIN item_counts ca ON ca.item_id = p.item_id
    JOIN item_counts cb ON cb.item_id = p.related_item_id
)
SELECT item_id, related_item_id, support, score
FROM ranked
WHERE rn <= ?`,
		fmt.Sprintf("-%d days", relatedWindowDays), relatedMinSupport, relatedKeepPerItem)
	if err != nil {
		return 0, relatedItemsObs.wrap("rebuild", err)
	}
	if err = tx.Commit(); err != nil {
		return 0, relatedItemsObs.wrap("rebuild", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// LastRebuilt reports when the table was last rebuilt (zero time when empty).
func (r *RelatedItemsRepo) LastRebuilt(ctx context.Context) (time.Time, error) {
	var err error
	done := relatedItemsObs.trace("last_rebuilt")
	defer func() { done(err) }()
	var ts sql.NullString
	err = r.db.QueryRowContext(ctx, `SELECT MAX(updated_at) FROM related_items`).Scan(&ts)
	if err != nil {
		return time.Time{}, relatedItemsObs.wrap("last_rebuilt", err)
	}
	if !ts.Valid || ts.String == "" {
		return time.Time{}, nil
	}
	parsed, perr := time.Parse("2006-01-02 15:04:05", ts.String)
	if perr != nil {
		return time.Time{}, nil
	}
	return parsed, nil
}

// Suggestion is one "customers also buy" candidate for the current basket.
type Suggestion struct {
	ItemID     string
	SKU        string
	Name       string
	PriceMinor int64
	ImageURL   string
	Score      float64
}

// SuggestForBasket blends related-item scores across every item in the
// basket and returns the strongest candidates that are not already in it.
// Only active items with a SKU qualify — the chip adds by SKU through the
// normal scan path, so the code must resolve deterministically.
func (r *RelatedItemsRepo) SuggestForBasket(ctx context.Context, basketItemIDs []string, limit int) ([]Suggestion, error) {
	if len(basketItemIDs) == 0 || limit <= 0 {
		return nil, nil
	}
	var err error
	done := relatedItemsObs.trace("suggest_for_basket")
	defer func() { done(err) }()

	placeholders := strings.Repeat("?,", len(basketItemIDs))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, 0, 2*len(basketItemIDs)+1)
	for _, id := range basketItemIDs {
		args = append(args, id)
	}
	for _, id := range basketItemIDs {
		args = append(args, id)
	}
	args = append(args, limit)

	query := fmt.Sprintf(`
SELECT i.id, i.sku, i.name, i.base_price,
       COALESCE((SELECT path FROM item_images
                 WHERE item_id = i.id AND role = 'thumbnail'
                 ORDER BY sort_order LIMIT 1), '') AS image_path,
       SUM(r.score) AS blended
FROM related_items r
JOIN items i ON i.id = r.related_item_id
WHERE r.item_id IN (%s)
  AND r.related_item_id NOT IN (%s)
  AND i.is_active = 1
  AND i.sku IS NOT NULL AND i.sku <> ''
GROUP BY i.id, i.sku, i.name, i.base_price
ORDER BY blended DESC, i.name
LIMIT ?`, placeholders, placeholders)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, relatedItemsObs.wrap("suggest_for_basket", err)
	}
	defer rows.Close()

	var out []Suggestion
	for rows.Next() {
		var s Suggestion
		if err = rows.Scan(&s.ItemID, &s.SKU, &s.Name, &s.PriceMinor, &s.ImageURL, &s.Score); err != nil {
			return nil, relatedItemsObs.wrap("suggest_for_basket", err)
		}
		out = append(out, s)
	}
	err = rows.Err()
	if err != nil {
		return nil, relatedItemsObs.wrap("suggest_for_basket", err)
	}
	return out, nil
}

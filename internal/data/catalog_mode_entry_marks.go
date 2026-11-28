package data

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// catalogMasterKey is the per-till entitlement setting cloudsync caches from
// the check-in block. Its values mirror entitlement.CatalogMaster* (data must
// not import entitlement): anything but cloud/adopting reads as till.
const catalogMasterKey = "entitlement.catalog_master"

// ErrCatalogModeEntryMarks marks an error from the mode-entry marking step
// (as opposed to the settings write itself), so a caller can raise a problem.
var ErrCatalogModeEntryMarks = errors.New("catalogue mode entry marks")

func catalogModeLive(v string) bool { return v == "cloud" || v == "adopting" }

// SetManyMarkingCatalogModeEntry is SetMany for the sync entitlement cache
// (ut-docs#6243). In the same transaction it reads the previously cached
// entitlement.catalog_master; when that was till (missing or unknown = till)
// and kv sets cloud or adopting, it runs MarkUnsentPromotedFieldsAtCloudDefault
// after the new mode row is written (the mark SQL's gate reads it). Returns
// the marks opened. Any error rolls the whole write back, so a till never
// caches cloud without its marks.
func (r *SettingsRepo) SetManyMarkingCatalogModeEntry(ctx context.Context, kv map[string]string) (int64, error) {
	var marked int64
	nextMode, setsMode := kv[catalogMasterKey]
	entering := false
	err := r.setMany(ctx, kv, setManyHooks{
		before: func(ctx context.Context, tx SyncQuerier) error {
			var prev string
			err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, catalogMasterKey).Scan(&prev)
			if err != nil && err != sql.ErrNoRows {
				return fmt.Errorf("read %s: %w", catalogMasterKey, err)
			}
			entering = setsMode && !catalogModeLive(prev) && catalogModeLive(nextMode)
			return nil
		},
		after: func(ctx context.Context, tx SyncQuerier) error {
			if !entering {
				return nil
			}
			n, err := NewCloudOutboxRepo().MarkUnsentPromotedFieldsAtCloudDefault(ctx, tx, time.Now().UTC().Format(time.RFC3339))
			marked = n
			if err != nil {
				return fmt.Errorf("%w: %w", ErrCatalogModeEntryMarks, err)
			}
			return nil
		},
	})
	if err != nil {
		return 0, err
	}
	return marked, nil
}

// MarkUnsentPromotedFieldsAtCloudDefault is the recipe's mark-and-seed step
// (MarkUnsentPromotedFields) narrowed to rows whose base still holds the cloud
// default or lacks the key: the cloud never held a value, so the till's value
// seeds it. A base holding a real cloud value is ADR-0162 §6's re-upgrade
// (the cloud wins) and is not marked. A mark uploads every field the row
// differs on, so a row is marked only when none of its other upload fields
// differs from the base either (otherFieldsMatchBase): a till-mode edit of
// another field stays cloud-wins and the pull resets it. Same gates as the
// outbox triggers. Merge with MarkUnsentPromotedFields once #6242 lands.
func (*CloudOutboxRepo) MarkUnsentPromotedFieldsAtCloudDefault(ctx context.Context, q SyncQuerier, createdAt string) (int64, error) {
	var total int64
	for _, f := range promotedUploadFields {
		key := "$." + f.baseKey
		cand := fmt.Sprintf(`
SELECT t.id, l.base
  FROM %[1]s t
  JOIN cloud_links l ON l.entity = ? AND l.local_id = t.id
 WHERE %[2]s <> ?
   AND l.base IS NOT NULL
   AND json_valid(l.base)
   AND COALESCE(json_extract(l.base, ?), ?) = ?
   AND (SELECT value FROM settings WHERE key = 'entitlement.catalog_master') IN ('cloud', 'adopting')
   AND COALESCE((SELECT TRIM(value) FROM settings WHERE key = 'sync.primary_url'), '') = ''
   AND NOT EXISTS (SELECT 1 FROM cloud_outbox_suppress)
   AND NOT EXISTS (SELECT 1 FROM cloud_outbox o
                    WHERE o.entity = ? AND o.local_id = t.id AND o.sealed_at IS NULL)`, f.table, f.localExpr)
		rows, err := q.QueryContext(ctx, cand, f.entity, f.cloudDefault, key, f.cloudDefault, f.cloudDefault, f.entity)
		if err != nil {
			return total, fmt.Errorf("mark unsent %s %s: %w", f.entity, f.baseKey, err)
		}
		type candidate struct{ id, base string }
		var cands []candidate
		for rows.Next() {
			var c candidate
			if err := rows.Scan(&c.id, &c.base); err != nil {
				_ = rows.Close()
				return total, fmt.Errorf("mark unsent %s %s: %w", f.entity, f.baseKey, err)
			}
			cands = append(cands, c)
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return total, fmt.Errorf("mark unsent %s %s: %w", f.entity, f.baseKey, err)
		}
		for _, c := range cands {
			ok, err := otherFieldsMatchBase(ctx, q, f.entity, c.id, c.base, f.baseKey)
			if err != nil {
				return total, fmt.Errorf("mark unsent %s %s: %w", f.entity, f.baseKey, err)
			}
			if !ok {
				continue
			}
			if _, err := q.ExecContext(ctx, `INSERT INTO cloud_outbox (entity, local_id, created_at) VALUES (?, ?, ?)`, f.entity, c.id, createdAt); err != nil {
				return total, fmt.Errorf("mark unsent %s %s: %w", f.entity, f.baseKey, err)
			}
			total++
		}
		if _, err := q.ExecContext(ctx, `
UPDATE cloud_links
   SET base = json_set(base, ?, ?)
 WHERE entity = ?
   AND base IS NOT NULL
   AND json_valid(base)
   AND json_type(base, ?) IS NULL`, key, f.cloudDefault, f.entity, key); err != nil {
			return total, fmt.Errorf("seed %s %s base: %w", f.entity, f.baseKey, err)
		}
	}
	return total, nil
}

// otherFieldsMatchBase reports whether every upload field of the row except
// skip equals its link base, mirroring catalogpull's changedFields: a field
// the base lacks is not compared, an item's sku compares with the kept SKU
// when the base has one, barcodes compare as the key set (primary first,
// rest sorted), and category/tax/parent are the linked cloud ids. KEEP IN
// SYNC with catalogpull.uploadFields / readLocal (the field names below).
func otherFieldsMatchBase(ctx context.Context, q SyncQuerier, entity, id, baseJSON, skip string) (bool, error) {
	var base map[string]json.RawMessage
	if err := json.Unmarshal([]byte(baseJSON), &base); err != nil {
		return false, nil // damaged base: leave to seedBases, no mark
	}
	ob := NewCloudOutboxRepo()
	links := NewCloudLinkRepo()
	cloudRef := func(kind, localID string) (any, error) {
		if localID == "" {
			return nil, nil
		}
		st, ok, err := links.StateByLocal(ctx, q, kind, localID)
		if err != nil || !ok {
			return nil, err
		}
		return st.CloudID, nil
	}
	vals := map[string]any{}
	var keys []string
	switch entity {
	case "item":
		it, bcs, ok, err := ob.OutboxItemValues(ctx, q, id)
		if err != nil || !ok {
			return false, err
		}
		cat, err := cloudRef("category", it.CategoryID)
		if err != nil {
			return false, err
		}
		tax, err := cloudRef("tax_code", it.TaxCodeID)
		if err != nil {
			return false, err
		}
		for _, b := range bcs {
			keys = append(keys, b.Key)
		}
		vals = map[string]any{
			"name": it.Name, "description": it.Description, "sku": it.SKU, "price_minor": it.PriceMinor,
			"category_cloud_id": cat, "tax_code_cloud_id": tax, "active": it.Active,
			"is_weighed": it.IsWeighed, "age_restricted": it.AgeRestricted,
			"net_quantity_value": it.NetQuantityValue, "net_quantity_unit": it.NetQuantityUnit,
		}
		if kept, ok := base["sku_kept"]; ok {
			base["sku"] = kept
		}
		if bb, ok := base["barcodes"]; ok {
			var list []struct {
				Key string `json:"key"`
			}
			_ = json.Unmarshal(bb, &list)
			var bk []string
			for _, b := range list {
				bk = append(bk, b.Key)
			}
			if barcodeKeyList(keys) != barcodeKeyList(bk) {
				return false, nil
			}
		}
	case "category":
		c, ok, err := ob.OutboxCategoryValues(ctx, q, id)
		if err != nil || !ok {
			return false, err
		}
		parent, err := cloudRef("category", c.ParentID)
		if err != nil {
			return false, err
		}
		vals = map[string]any{"name": c.Name, "parent_cloud_id": parent, "sort_order": c.Sort, "active": c.Active}
	default:
		return false, nil
	}
	for field, v := range vals {
		if field == skip {
			continue
		}
		b, ok := base[field]
		if !ok {
			continue
		}
		raw, _ := json.Marshal(v)
		if normJSON(raw) != normJSON(b) {
			return false, nil
		}
	}
	return true, nil
}

func barcodeKeyList(keys []string) string {
	if len(keys) == 0 {
		return "[]"
	}
	rest := append([]string(nil), keys[1:]...)
	sort.Strings(rest)
	return strings.Join(append([]string{keys[0]}, rest...), "\x00")
}

// normJSON is a JSON value in one canonical spelling (numbers kept exact).
func normJSON(raw []byte) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "null"
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var x any
	if dec.Decode(&x) != nil {
		return string(raw)
	}
	out, _ := json.Marshal(x)
	return string(out)
}

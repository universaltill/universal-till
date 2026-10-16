package catalogpull

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// The kinds this applier writes, by feed kind. Each is also the cloud_links
// entity and the audit entity type. Every other kind is skipped.
const (
	kindTaxCode  = "tax_code"
	kindCategory = "category"
	kindItem     = "item"
)

var appliedKinds = map[string]bool{kindTaxCode: true, kindCategory: true, kindItem: true}

// auditActor is the audit_log actor of a pulled row: the seeded 'system'
// user, as every cloud directive's audit row (auditCloudDirective).
const auditActor = "system"

// maxReasonRunes bounds a refusal reason kept in cloud_apply_failures.
const maxReasonRunes = 300

// applyRow applies one row inside its own savepoint of tx. A refusal is
// recorded and is not an error; an error return is fatal for the run (the
// chunk rolls back).
func (r *runner) applyRow(ctx context.Context, tx *sql.Tx, row Row, gen int64, res *Result) error {
	if !appliedKinds[row.Kind] {
		res.Skipped++
		return nil
	}
	cloudID, idErr := canonicalID(row.CloudID)
	if idErr != nil {
		// Recorded under the raw id (bounded): the cloud must hear of it.
		res.Refused++
		return r.recordRefusal(ctx, tx, row.Kind, boundID(row.CloudID), row.Version, idErr)
	}
	row.CloudID = cloudID
	var photos []replacedPhoto
	err := data.WithSavepoint(ctx, tx, "cloud_pull_row", func() error {
		var err error
		photos, err = r.apply(ctx, tx, row, gen)
		return err
	})
	switch {
	case isInfra(ctx, err):
		return fmt.Errorf("catalogpull: %w", errors.Join(err, ctx.Err()))
	case err != nil:
		res.Refused++
		logging.L().Warnf("catalogpull: %s %s version %d refused, the till keeps its previous version: %s", row.Kind, row.CloudID, row.Version, reason(err))
		if ferr := r.recordRefusal(ctx, tx, row.Kind, row.CloudID, row.Version, err); ferr != nil {
			return ferr
		}
		// The row still exists in the cloud: a resync must not prune it.
		if terr := r.links.Touch(ctx, tx, row.Kind, row.CloudID, gen); terr != nil {
			return fmt.Errorf("catalogpull: %w", terr)
		}
		return nil
	}
	res.Applied++
	res.Changed = true
	r.photos = append(r.photos, photos...)
	return nil
}

// infraError marks a failure that is not the till refusing the row — a
// caller hook failing — so it aborts the chunk instead of being recorded.
type infraError struct{ err error }

func (e *infraError) Error() string { return e.err.Error() }
func (e *infraError) Unwrap() error { return e.err }

// isInfra reports whether err is the run failing rather than the row being
// refused: the context ended, the database failed (data.IsStorageError,
// which includes a failed savepoint), or a hook failed. Such an error
// aborts the chunk with the cursor unchanged — recording it as a refusal
// would move the cursor past a row the till never judged. Every other
// error comes from the repositories' validation and refuses the row.
func isInfra(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	var ie *infraError
	return ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		data.IsStorageError(err) || errors.As(err, &ie)
}

func (r *runner) recordRefusal(ctx context.Context, tx *sql.Tx, kind, cloudID string, version int64, cause error) error {
	if err := r.links.RecordFailure(ctx, tx, kind, cloudID, version, reason(cause)); err != nil {
		return fmt.Errorf("catalogpull: %w", err)
	}
	return nil
}

// reason is a refusal's owner-readable text, bounded.
func reason(err error) string {
	s := err.Error()
	if utf8.RuneCountInString(s) <= maxReasonRunes {
		return s
	}
	return string([]rune(s)[:maxReasonRunes])
}

func boundID(id string) string {
	if len(id) > 64 {
		id = id[:64]
	}
	if id == "" || !utf8.ValidString(id) {
		return "invalid"
	}
	return id
}

// canonicalID checks a row's cloud_id is a UUID (it becomes a local
// primary key for a cloud-created row) and returns its canonical form.
func canonicalID(id string) (string, error) {
	u, err := uuid.Parse(id)
	if err != nil || len(id) != 36 {
		return "", fmt.Errorf("cloud_id %q is not a UUID", boundID(id))
	}
	return u.String(), nil
}

// apply writes one row (in the caller's savepoint) and its link.
func (r *runner) apply(ctx context.Context, tx *sql.Tx, row Row, gen int64) ([]replacedPhoto, error) {
	local, err := r.localID(ctx, tx, row.Kind, row.CloudID)
	if err != nil {
		return nil, err
	}
	if row.Deleted {
		return nil, r.tombstone(ctx, tx, row.Kind, row.CloudID, local, row.Version)
	}
	f, err := decodeFields(row.Data)
	if err != nil {
		return nil, err
	}
	var created bool
	var changed []string
	var photos []replacedPhoto
	switch row.Kind {
	case kindTaxCode:
		res, err := r.applyTaxCode(ctx, tx, local, f)
		if err != nil {
			return nil, err
		}
		created, changed = res.Created, res.Changed
	case kindCategory:
		res, err := r.applyCategory(ctx, tx, local, f)
		if err != nil {
			return nil, err
		}
		created, changed = res.Created, res.Changed
	case kindItem:
		res, err := r.applyItem(ctx, tx, local, f)
		if err != nil {
			return nil, err
		}
		created, changed = res.Created, res.Changed
		if res.ClearedImagePath != "" {
			photos = append(photos, replacedPhoto{itemID: local, path: res.ClearedImagePath})
		}
	}
	if err := r.links.Upsert(ctx, tx, data.CloudLink{Entity: row.Kind, CloudID: row.CloudID, LocalID: local, Version: row.Version, Gen: gen}); err != nil {
		return nil, err
	}
	if err := r.links.ClearFailure(ctx, tx, row.Kind, row.CloudID); err != nil {
		return nil, err
	}
	if err := r.audit(ctx, tx, row.Kind, local, "saved", map[string]any{
		"cloud_id": row.CloudID, "version": row.Version, "created": created, "changed": changed,
	}); err != nil {
		return nil, err
	}
	return photos, nil
}

func (r *runner) audit(ctx context.Context, tx *sql.Tx, kind, localID, what string, payload map[string]any) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if err := r.pos.InsertAudit(ctx, tx, auditActor, kind, localID, "cloud_catalog_"+kind+"_"+what, payload, now, ""); err != nil {
		return fmt.Errorf("catalogpull: audit: %w", err)
	}
	return nil
}

// localID is the local record of (kind, cloudID): its link, else the
// cloud_id itself — a cloud-created row is created under its cloud_id, so
// a crash between a write and its link can only ever find the same row.
//
// So an unlinked local row whose id already equals the cloud_id is
// adopted, not duplicated: the save updates it (Create only creates a
// missing row) and links it. That is intentional — it is exactly the
// crash case above, and a till-made row the cloud learned under the same
// id is the same row — and observable: its audit row says created:false.
func (r *runner) localID(ctx context.Context, tx *sql.Tx, kind, cloudID string) (string, error) {
	local, ok, err := r.links.LocalID(ctx, tx, kind, cloudID)
	if err != nil {
		return "", err
	}
	if ok {
		return local, nil
	}
	return cloudID, nil
}

// resolve maps a cross-row reference to the local id of an already pulled
// row. Rows arrive in version order, so a parent normally precedes its
// child; a reference with no link refuses the row.
func (r *runner) resolve(ctx context.Context, tx *sql.Tx, kind, cloudID string) (string, error) {
	id, err := canonicalID(cloudID)
	if err != nil {
		return "", err
	}
	local, ok, err := r.links.LocalID(ctx, tx, kind, id)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("%s %s has not reached this till", kindLabel(kind), id)
	}
	return local, nil
}

func kindLabel(kind string) string {
	switch kind {
	case kindTaxCode:
		return "tax code"
	default:
		return kind
	}
}

// tombstone applies a delete through the till's own delete rules and
// removes the link — tombstones are final (contract), so nothing needs the
// link afterwards; a row this till never had is a no-op.
//
//   - item: deleted for good only when never sold and not in use
//     (DeleteUnusedItemTx, delete_item's rule, ut-docs#3317, plus the live
//     baskets); anything else is deactivated (DeactivateItemTx);
//   - category: DeleteCategoryMovingTx (subcategories move up, items become
//     uncategorised, soft-deleted so past sales keep its name);
//   - tax code: deactivated — the till has no tax-code delete (items and
//     history reference it).
func (r *runner) tombstone(ctx context.Context, tx *sql.Tx, kind, cloudID, local string, version int64) error {
	what := ""
	switch kind {
	case kindItem:
		deactivate := false
		if r.opt.ItemInLiveBasket != nil {
			in, err := r.opt.ItemInLiveBasket(ctx, local)
			if err != nil {
				return &infraError{fmt.Errorf("live basket check: %w", err)}
			}
			deactivate = in
		}
		if !deactivate {
			res, err := r.pos.DeleteUnusedItemTx(ctx, tx, local)
			var inUse *data.ItemInUseError
			switch {
			case errors.As(err, &inUse):
				deactivate = true
			case err != nil:
				return err
			case !res.AlreadyDeleted:
				what = "deleted"
			}
		}
		if deactivate {
			err := r.cat.DeactivateItemTx(ctx, tx, local)
			switch {
			case errors.Is(err, data.ErrItemNotFound):
				// Already inactive (or gone): nothing to do.
			case err != nil:
				return err
			default:
				what = "deactivated"
			}
		}
	case kindCategory:
		res, err := r.cat.DeleteCategoryMovingTx(ctx, tx, local, "")
		switch {
		case errors.Is(err, data.ErrCategoryNotFound):
		case err != nil:
			return err
		case !res.AlreadyDeleted:
			what = "deleted"
		}
	case kindTaxCode:
		off := false
		_, err := r.cat.SaveTaxCodeTx(ctx, tx, data.TaxCodeSave{ID: local, Active: &off}, nil)
		switch {
		case errors.Is(err, data.ErrTaxCodeNotFound):
		case err != nil:
			return err
		default:
			what = "deactivated"
		}
	}
	if err := r.links.Delete(ctx, tx, kind, cloudID); err != nil {
		return err
	}
	if err := r.links.ClearFailure(ctx, tx, kind, cloudID); err != nil {
		return err
	}
	if what == "" {
		return nil
	}
	return r.audit(ctx, tx, kind, local, what, map[string]any{"cloud_id": cloudID, "version": version})
}

// fields is a row's data object, key → raw JSON, so the applier can tell
// an absent field (keep) from a null one (clear, where the till can).
type fields map[string]json.RawMessage

func decodeFields(raw json.RawMessage) (fields, error) {
	var f fields
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, errors.New("the row has no data")
	}
	if err := json.Unmarshal(raw, &f); err != nil || f == nil {
		return nil, errors.New("the row's data is not an object")
	}
	return f, nil
}

// get returns a field's raw value; present is false when absent, null true
// for JSON null.
func (f fields) get(key string) (raw json.RawMessage, present, null bool) {
	raw, present = f[key]
	if !present {
		return nil, false, false
	}
	return raw, true, bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func (f fields) str(key string) (v string, present, null bool, err error) {
	raw, present, null := f.get(key)
	if !present || null {
		return "", present, null, nil
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", true, false, fmt.Errorf("%s must be a string", key)
	}
	return v, true, false, nil
}

func (f fields) int64(key string) (v int64, present, null bool, err error) {
	raw, present, null := f.get(key)
	if !present || null {
		return 0, present, null, nil
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, true, false, fmt.Errorf("%s must be a whole number", key)
	}
	return v, true, false, nil
}

// boolPtr: absent → nil; null or not a bool → error.
func (f fields) boolPtr(key string) (*bool, error) {
	raw, present, null := f.get(key)
	if !present {
		return nil, nil
	}
	var v bool
	if null || json.Unmarshal(raw, &v) != nil {
		return nil, fmt.Errorf("%s must be true or false", key)
	}
	return &v, nil
}

// requiredStr: absent → nil; null → error.
func (f fields) requiredStr(key string) (*string, error) {
	v, present, null, err := f.str(key)
	switch {
	case err != nil:
		return nil, err
	case !present:
		return nil, nil
	case null:
		return nil, fmt.Errorf("%s must not be null", key)
	}
	return &v, nil
}

// clearableStr: absent → nil; null → "" (clear).
func (f fields) clearableStr(key string) (*string, error) {
	v, present, _, err := f.str(key)
	if err != nil || !present {
		return nil, err
	}
	return &v, nil
}

// keepIfBlankStr: absent, null or "" → nil (keep the till's value).
func (f fields) keepIfBlankStr(key string) (*string, error) {
	v, _, _, err := f.str(key)
	if err != nil || v == "" {
		return nil, err
	}
	return &v, nil
}

// intField: absent → nil; null → error; outside int → error.
func (f fields) intField(key string) (*int, error) {
	v, present, null, err := f.int64(key)
	switch {
	case err != nil:
		return nil, err
	case !present:
		return nil, nil
	case null:
		return nil, fmt.Errorf("%s must not be null", key)
	case v < math.MinInt32 || v > math.MaxInt32:
		return nil, fmt.Errorf("%s is out of range", key)
	}
	n := int(v)
	return &n, nil
}

// ref maps a *_cloud_id reference: absent → nil; null → "" (clear);
// else the linked local id.
func (r *runner) ref(ctx context.Context, tx *sql.Tx, f fields, key, kind string) (*string, error) {
	v, present, null, err := f.str(key)
	switch {
	case err != nil:
		return nil, err
	case !present:
		return nil, nil
	case null:
		empty := ""
		return &empty, nil
	}
	local, err := r.resolve(ctx, tx, kind, v)
	if err != nil {
		return nil, err
	}
	return &local, nil
}

// applyTaxCode maps a tax_code row: name, rate_basis_points,
// takeaway_rate_basis_points (null = no takeaway override), active.
func (r *runner) applyTaxCode(ctx context.Context, tx *sql.Tx, local string, f fields) (data.TaxCodeSaveResult, error) {
	s := data.TaxCodeSave{ID: local, Create: true}
	var err error
	if s.Name, err = f.requiredStr("name"); err != nil {
		return data.TaxCodeSaveResult{}, err
	}
	if s.RateBP, err = f.intField("rate_basis_points"); err != nil {
		return data.TaxCodeSaveResult{}, err
	}
	if _, present, null := f.get("takeaway_rate_basis_points"); present && null {
		s.ClearTakeaway = true
	} else if s.TakeawayBP, err = f.intField("takeaway_rate_basis_points"); err != nil {
		return data.TaxCodeSaveResult{}, err
	}
	if s.Active, err = f.boolPtr("active"); err != nil {
		return data.TaxCodeSaveResult{}, err
	}
	return r.cat.SaveTaxCodeTx(ctx, tx, s, r.opt.TaxRateRefused)
}

// applyCategory maps a category row: name, parent_cloud_id (null = top
// level), sort_order, color, icon (null = clear), sell_screen_hidden,
// active (SetCategoryActive's rules: deactivating refuses while active
// items are in it). modifier_group_cloud_ids is not applied (follow-up).
func (r *runner) applyCategory(ctx context.Context, tx *sql.Tx, local string, f fields) (data.CategorySaveResult, error) {
	p := data.CategorySave{ID: local, Create: true}
	var err error
	if p.Name, err = f.requiredStr("name"); err != nil {
		return data.CategorySaveResult{}, err
	}
	if p.ParentID, err = r.ref(ctx, tx, f, "parent_cloud_id", kindCategory); err != nil {
		return data.CategorySaveResult{}, err
	}
	if p.SortOrder, err = f.intField("sort_order"); err != nil {
		return data.CategorySaveResult{}, err
	}
	if p.Color, err = f.clearableStr("color"); err != nil {
		return data.CategorySaveResult{}, err
	}
	if p.Icon, err = f.clearableStr("icon"); err != nil {
		return data.CategorySaveResult{}, err
	}
	hidden, err := f.boolPtr("sell_screen_hidden")
	if err != nil {
		return data.CategorySaveResult{}, err
	}
	if hidden != nil {
		show := !*hidden
		p.ShowOnSaleScreen = &show
	}
	active, err := f.boolPtr("active")
	if err != nil {
		return data.CategorySaveResult{}, err
	}
	res, err := r.cat.SaveCategoryTx(ctx, tx, p)
	if err != nil {
		return res, err
	}
	if active != nil {
		if err := r.cat.SetCategoryActiveTx(ctx, tx, local, *active); err != nil {
			return res, err
		}
		res.Changed = append(res.Changed, "active")
	}
	return res, nil
}

// applyItem maps an item row onto data.ItemPatch, field by field (the
// field table is in docs/data-model.md, "Cloud catalogue pull"). A blank
// sku or unit keeps the till's (a blank SKU is generated on create). The
// modifier-group, opt-out and option-set id lists are not applied
// (follow-ups ut-docs#4491/#4492).
func (r *runner) applyItem(ctx context.Context, tx *sql.Tx, local string, f fields) (data.ItemSaveResult, error) {
	p := data.ItemPatch{ID: local, Create: true}
	var err error
	fail := func(e error) (data.ItemSaveResult, error) { return data.ItemSaveResult{}, e }
	if p.Name, err = f.requiredStr("name"); err != nil {
		return fail(err)
	}
	if p.SKU, err = f.keepIfBlankStr("sku"); err != nil {
		return fail(err)
	}
	if p.Description, err = f.clearableStr("description"); err != nil {
		return fail(err)
	}
	if p.Unit, err = f.keepIfBlankStr("unit"); err != nil {
		return fail(err)
	}
	price, present, null, err := f.int64("price_minor")
	switch {
	case err != nil:
		return fail(err)
	case present && null:
		return fail(errors.New("price_minor must not be null"))
	case present:
		p.PriceMinor = &price
	}
	if p.CategoryID, err = r.ref(ctx, tx, f, "category_cloud_id", kindCategory); err != nil {
		return fail(err)
	}
	if p.TaxCodeID, err = r.ref(ctx, tx, f, "tax_code_cloud_id", kindTaxCode); err != nil {
		return fail(err)
	}
	for _, b := range []struct {
		key string
		dst **bool
	}{
		{"active", &p.Active}, {"is_weighed", &p.IsWeighed}, {"stock_untracked", &p.StockUntracked},
		{"age_restricted", &p.AgeRestricted}, {"sell_screen_hidden", &p.SellScreenHidden},
		{"sell_screen_removed", &p.SellScreenRemoved},
	} {
		if *b.dst, err = f.boolPtr(b.key); err != nil {
			return fail(err)
		}
	}
	if p.Color, err = f.clearableStr("color"); err != nil {
		return fail(err)
	}
	if p.Icon, err = f.clearableStr("icon"); err != nil {
		return fail(err)
	}
	if t, present, null, err := f.str("item_type"); err != nil {
		return fail(err)
	} else if present && !null {
		p.ItemType = &t
	}
	_, vPresent, _ := f.get("net_quantity_value")
	_, uPresent, _ := f.get("net_quantity_unit")
	if vPresent || uPresent {
		v, _, vNull, err := f.int64("net_quantity_value")
		if err != nil {
			return fail(err)
		}
		u, _, uNull, err := f.str("net_quantity_unit")
		if err != nil {
			return fail(err)
		}
		switch {
		case (vNull || !vPresent) && (uNull || !uPresent):
			p.ClearNetQuantity = true
		default:
			if vPresent && !vNull {
				p.NetQuantityValue = &v
			}
			if uPresent && !uNull {
				p.NetQuantityUnit = &u
			}
			if p.NetQuantityValue == nil || p.NetQuantityUnit == nil {
				return fail(data.ErrInvalidNetQuantity)
			}
		}
	}
	return r.cat.SaveItemTx(ctx, tx, p)
}

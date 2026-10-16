package catalogpull

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// The kinds this applier writes, by feed kind. Each is also the cloud_links
// entity and the audit entity type. Every other kind is skipped.
const (
	kindTaxCode       = "tax_code"
	kindCategory      = "category"
	kindItem          = "item"
	kindModifierGroup = "modifier_group"
	kindOptionSet     = "option_set"
	kindItemVariant   = "item_variant"
	kindItemBarcode   = "item_barcode"
)

var appliedKinds = map[string]bool{
	kindTaxCode: true, kindCategory: true, kindItem: true, kindModifierGroup: true, kindOptionSet: true,
	kindItemVariant: true, kindItemBarcode: true,
}

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
	local, linked, err := r.localID(ctx, tx, row.Kind, row.CloudID)
	if err != nil {
		return nil, err
	}
	if row.Kind == kindItemBarcode && !linked {
		// A barcode's local record is its owner and stored key
		// (barcodeLocalID), never its cloud_id: an unlinked barcode row has
		// none yet.
		local = ""
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
	case kindModifierGroup:
		res, err := r.applyModifierGroup(ctx, tx, local, f)
		if err != nil {
			return nil, err
		}
		created, changed = res.Created, res.Changed
	case kindOptionSet:
		res, err := r.applyOptionSet(ctx, tx, local, f)
		if err != nil {
			return nil, err
		}
		created, changed = res.Created, res.Changed
	case kindItemVariant:
		res, err := r.applyVariant(ctx, tx, local, f)
		if err != nil {
			return nil, err
		}
		created, changed = res.Created, res.Changed
	case kindItemBarcode:
		newLocal, made, err := r.applyBarcode(ctx, tx, row.CloudID, local, f)
		if err != nil {
			return nil, err
		}
		created, changed = made, []string{"barcode", "item_cloud_id", "variant_cloud_id", "is_primary"}
		local = newLocal
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
// linked reports whether the id came from a link.
func (r *runner) localID(ctx context.Context, tx *sql.Tx, kind, cloudID string) (local string, linked bool, err error) {
	local, ok, err := r.links.LocalID(ctx, tx, kind, cloudID)
	if err != nil {
		return "", false, err
	}
	if ok {
		return local, true, nil
	}
	return cloudID, false, nil
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
	case kindModifierGroup:
		return "modifier group"
	case kindOptionSet:
		return "option set"
	case kindItemVariant:
		return "variant"
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
//     history reference it);
//   - modifier group: deleted (DeleteGroupIfExistsTx — delete_modifier_
//     group's cascade to its options, item/category links and opt-outs;
//     past sales keep their sale_line_modifiers snapshots);
//   - option set: deleted when no active item has it applied
//     (DeleteOptionSetIfUnusedTx, delete_option_set's rule), else
//     deactivated;
//   - item variant: deactivated (DeactivateVariantTx) — a variant is never
//     deleted (save_item_variant's rule; sales and stock reference it);
//   - item barcode: removed from the owner its link stored
//     (DeleteOwnedBarcodeTx); one never pulled, or whose key another
//     barcode row's link also names, is left alone.
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
	case kindModifierGroup:
		deleted, err := r.mods.DeleteGroupIfExistsTx(ctx, tx, local)
		if err != nil {
			return err
		}
		if deleted {
			what = "deleted"
		}
	case kindOptionSet:
		deleted, err := r.sets.DeleteOptionSetIfUnusedTx(ctx, tx, local)
		switch {
		case errors.Is(err, data.ErrOptionSetInUse):
			off := false
			_, err := r.sets.SaveOptionSetTx(ctx, tx, data.OptionSetSave{ID: local, Active: &off})
			if err != nil {
				return err
			}
			what = "deactivated"
		case err != nil:
			return err
		case deleted:
			what = "deleted"
		}
	case kindItemVariant:
		off, err := r.cat.DeactivateVariantTx(ctx, tx, local)
		if err != nil {
			return err
		}
		if off {
			what = "deactivated"
		}
	case kindItemBarcode:
		deleted, err := r.removeBarcode(ctx, tx, cloudID, local)
		if err != nil {
			return err
		}
		if deleted {
			what = "deleted"
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

// refList maps a *_cloud_ids list: absent → nil (keep); [] → an empty
// list (clear); null → refused (the contract's lists are never null);
// every id must be linked to an already pulled row of kind.
func (r *runner) refList(ctx context.Context, tx *sql.Tx, f fields, key, kind string) (*[]string, error) {
	raw, present, null := f.get(key)
	switch {
	case !present:
		return nil, nil
	case null:
		return nil, fmt.Errorf("%s must be a list, not null", key)
	}
	var ids []string
	if err := json.Unmarshal(raw, &ids); err != nil {
		return nil, fmt.Errorf("%s must be a list of ids", key)
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		local, err := r.resolve(ctx, tx, kind, id)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		out = append(out, local)
	}
	return &out, nil
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
// items are in it), modifier_group_cloud_ids (the category's whole ordered
// group set, SetCategoryModifierGroupsTx).
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
	groups, err := r.refList(ctx, tx, f, "modifier_group_cloud_ids", kindModifierGroup)
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
	if groups != nil {
		if err := r.mods.SetCategoryModifierGroupsTx(ctx, tx, local, *groups); err != nil {
			return res, err
		}
		res.Changed = append(res.Changed, "modifier_group_ids")
	}
	return res, nil
}

// applyItem maps an item row onto data.ItemPatch, field by field (the
// field table is in docs/data-model.md, "Cloud catalogue pull"). A blank
// sku or unit keeps the till's (a blank SKU is generated on create). The
// modifier-group and opt-out lists are the item's whole sets
// (ItemPatch.ModifierGroupIDs / ModifierOptOutIDs); option_set_cloud_ids
// is its whole ordered set of option sets (ApplyOptionSetsToItemTx — no
// variants are generated: they arrive as their own rows, ut-docs#4521).
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
	if p.ModifierGroupIDs, err = r.refList(ctx, tx, f, "modifier_group_cloud_ids", kindModifierGroup); err != nil {
		return fail(err)
	}
	if p.ModifierOptOutIDs, err = r.refList(ctx, tx, f, "modifier_opt_out_cloud_ids", kindModifierGroup); err != nil {
		return fail(err)
	}
	sets, err := r.refList(ctx, tx, f, "option_set_cloud_ids", kindOptionSet)
	if err != nil {
		return fail(err)
	}
	res, err := r.cat.SaveItemTx(ctx, tx, p)
	if err != nil {
		return res, err
	}
	if sets != nil {
		if err := r.sets.ApplyOptionSetsToItemTx(ctx, tx, local, *sets); err != nil {
			return res, err
		}
		res.Changed = append(res.Changed, "option_set_ids")
	}
	return res, nil
}

// applyModifierGroup maps a modifier_group row onto data.ModifierGroupSave:
// name, required, min_select, max_select, sort_order, active, and options
// — the group's FULL ordered option set (an option left out is deleted),
// each {id, name, price_delta_minor, active}; an option id must be a UUID
// (it becomes the local option's primary key) and an absent option active
// means active. The selection rules, bounds and option rules are
// SaveGroup's.
func (r *runner) applyModifierGroup(ctx context.Context, tx *sql.Tx, local string, f fields) (data.ModifierGroupSaveResult, error) {
	p := data.ModifierGroupSave{ID: local, Create: true}
	var err error
	fail := func(e error) (data.ModifierGroupSaveResult, error) { return data.ModifierGroupSaveResult{}, e }
	if p.Name, err = f.requiredStr("name"); err != nil {
		return fail(err)
	}
	if p.Required, err = f.boolPtr("required"); err != nil {
		return fail(err)
	}
	if p.MinSelect, err = f.intField("min_select"); err != nil {
		return fail(err)
	}
	if p.MaxSelect, err = f.intField("max_select"); err != nil {
		return fail(err)
	}
	if p.SortOrder, err = f.intField("sort_order"); err != nil {
		return fail(err)
	}
	if p.Active, err = f.boolPtr("active"); err != nil {
		return fail(err)
	}
	raw, present, null := f.get("options")
	switch {
	case present && null:
		return fail(errors.New("options must be a list, not null"))
	case present:
		var in []struct {
			ID              string `json:"id"`
			Name            string `json:"name"`
			PriceDeltaMinor int64  `json:"price_delta_minor"`
			Active          *bool  `json:"active"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return fail(errors.New("options must be a list of {id, name, price_delta_minor, active}"))
		}
		opts := make([]data.ModifierOption, 0, len(in))
		for _, o := range in {
			id, err := canonicalID(o.ID)
			if err != nil {
				return fail(fmt.Errorf("option %w", err))
			}
			opts = append(opts, data.ModifierOption{ID: id, Name: o.Name, PriceDeltaMinor: o.PriceDeltaMinor, IsActive: o.Active == nil || *o.Active})
		}
		p.Options = &opts
	}
	return r.mods.SaveGroupTx(ctx, tx, p)
}

// applyOptionSet maps an option_set row onto data.OptionSetSave: name,
// active, and values — the set's FULL ordered value list ({id, value}; an
// id must be a UUID, a value left out is deleted). SaveOptionSet's rules
// apply (unique name, 1–128 characters per value, no value twice).
func (r *runner) applyOptionSet(ctx context.Context, tx *sql.Tx, local string, f fields) (data.OptionSetSaveResult, error) {
	p := data.OptionSetSave{ID: local, Create: true}
	var err error
	fail := func(e error) (data.OptionSetSaveResult, error) { return data.OptionSetSaveResult{}, e }
	if p.Name, err = f.requiredStr("name"); err != nil {
		return fail(err)
	}
	if p.Active, err = f.boolPtr("active"); err != nil {
		return fail(err)
	}
	raw, present, null := f.get("values")
	switch {
	case present && null:
		return fail(errors.New("values must be a list, not null"))
	case present:
		var in []struct {
			ID    string `json:"id"`
			Value string `json:"value"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return fail(errors.New("values must be a list of {id, value}"))
		}
		vals := make([]data.OptionSetValueInput, 0, len(in))
		for _, v := range in {
			id, err := canonicalID(v.ID)
			if err != nil {
				return fail(fmt.Errorf("option set value %w", err))
			}
			vals = append(vals, data.OptionSetValueInput{ID: id, Value: v.Value})
		}
		p.Values = &vals
	}
	return r.sets.SaveOptionSetTx(ctx, tx, p)
}

// applyVariant maps an item_variant row onto data.VariantSave:
// item_cloud_id (required — a variant names its item on every row), name,
// sku (blank = keep; a blank on create generates a VAR- code), price_minor
// (not null), active, retired_with_item (only holds on an inactive
// variant), and option_value_cloud_ids — the variant's FULL set of option
// values (item_variant_options; absent keeps, [] clears, null refuses).
// An option value's id is its cloud id (applyOptionSet), so it must be a
// value already on this till. SaveVariant's rules apply: a variant never
// moves to another item, at most 100 per item, one SKU namespace with
// items.
func (r *runner) applyVariant(ctx context.Context, tx *sql.Tx, local string, f fields) (data.VariantSaveResult, error) {
	p := data.VariantSave{ID: local, Create: true}
	var err error
	fail := func(e error) (data.VariantSaveResult, error) { return data.VariantSaveResult{}, e }
	item, present, null, err := f.str("item_cloud_id")
	switch {
	case err != nil:
		return fail(err)
	case !present || null:
		return fail(errors.New("item_cloud_id is required"))
	}
	if p.ItemID, err = r.resolve(ctx, tx, kindItem, item); err != nil {
		return fail(err)
	}
	if p.Name, err = f.requiredStr("name"); err != nil {
		return fail(err)
	}
	if p.SKU, err = f.keepIfBlankStr("sku"); err != nil {
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
	if p.Active, err = f.boolPtr("active"); err != nil {
		return fail(err)
	}
	if p.RetiredWithItem, err = f.boolPtr("retired_with_item"); err != nil {
		return fail(err)
	}
	raw, present, null := f.get("option_value_cloud_ids")
	switch {
	case present && null:
		return fail(errors.New("option_value_cloud_ids must be a list, not null"))
	case present:
		var ids []string
		if err := json.Unmarshal(raw, &ids); err != nil {
			return fail(errors.New("option_value_cloud_ids must be a list of ids"))
		}
		vals := make([]string, 0, len(ids))
		for _, id := range ids {
			v, err := canonicalID(id)
			if err != nil {
				return fail(fmt.Errorf("option value %w", err))
			}
			vals = append(vals, v)
		}
		p.OptionValueIDs = &vals
	}
	return r.cat.SaveVariantTx(ctx, tx, p)
}

// barcodeResolution is one feed barcode resolved before the chunk's
// transaction (resolveBarcodes).
type barcodeResolution struct {
	res data.ResolvedBarcode
	err error
}

// resolveBarcodes resolves every item_barcode code on a page before any
// of its chunks begins: resolving reads the shop's enabled symbologies
// through the pool, which must not run inside the held write transaction
// (resolveBarcodeSet's rule). The pull itself never changes those
// settings (the setting kind is not applied, ut-docs#4492), so a page's
// resolutions hold for all its chunks. The code is read exactly as
// applyBarcode reads it (decodeFields, "barcode"), so the two agree.
func (r *runner) resolveBarcodes(ctx context.Context, rows []Row) map[string]barcodeResolution {
	out := map[string]barcodeResolution{}
	for _, row := range rows {
		if row.Kind != kindItemBarcode || row.Deleted {
			continue
		}
		f, err := decodeFields(row.Data)
		if err != nil {
			continue // applyBarcode refuses it
		}
		code, err := f.requiredStr("barcode")
		if err != nil || code == nil {
			continue
		}
		if _, ok := out[*code]; ok {
			continue
		}
		res, err := r.cat.ResolveBarcode(ctx, *code)
		out[*code] = barcodeResolution{res: res, err: err}
	}
	return out
}

// barcodeLocalID is a barcode link's local id: its owner and stored key,
// "item:<item id>:<key>" or "variant:<variant id>:<key>". Item and variant
// ids never contain ':' (UUIDs); the key may, and parseBarcodeLocalID
// splits at the first two only. Keeping the
// owner means a changed, moved or tombstoned row removes the barcode only
// from the owner it put it on — never one another item or variant holds
// now.
func barcodeLocalID(itemID, variantID, key string) string {
	if variantID != "" {
		return "variant:" + variantID + ":" + key
	}
	return "item:" + itemID + ":" + key
}

// parseBarcodeLocalID splits a barcodeLocalID; ok is false for anything
// else.
func parseBarcodeLocalID(local string) (itemID, variantID, key string, ok bool) {
	parts := strings.SplitN(local, ":", 3)
	if len(parts) != 3 || parts[1] == "" || parts[2] == "" {
		return "", "", "", false
	}
	switch parts[0] {
	case "item":
		return parts[1], "", parts[2], true
	case "variant":
		return "", parts[1], parts[2], true
	}
	return "", "", "", false
}

// removeBarcode deletes the barcode a link's local id names from its own
// owner, unless another barcode row's link names the same record. ""
// (never pulled) or an unreadable id is a no-op.
func (r *runner) removeBarcode(ctx context.Context, tx *sql.Tx, cloudID, local string) (bool, error) {
	itemID, variantID, key, ok := parseBarcodeLocalID(local)
	if !ok {
		return false, nil
	}
	held, err := r.links.HeldByOther(ctx, tx, kindItemBarcode, local, cloudID)
	if err != nil || held {
		return false, err
	}
	return r.cat.DeleteOwnedBarcodeTx(ctx, tx, key, itemID, variantID)
}

// applyBarcode maps an item_barcode row onto data.BarcodePut and returns
// its new local id (barcodeLocalID) and whether the barcode row was
// created. A barcode row is written whole: barcode and item_cloud_id are
// required, variant_cloud_id absent or null is an item barcode,
// is_primary absent is false (true demotes the owner's other primary).
// barcode_type is not read: the till resolves the code against the shop's
// enabled symbologies exactly as save_item does (ADR-0059 §3 — the stored
// type is the till's own, never trusted from the sender). prev is the
// local id the row had before ("" = none): a changed code or a move
// removes the old barcode from its old owner. A key another barcode row's
// link already names refuses the row (its tombstone would delete a code
// the other row still has).
func (r *runner) applyBarcode(ctx context.Context, tx *sql.Tx, cloudID, prev string, f fields) (string, bool, error) {
	code, err := f.requiredStr("barcode")
	switch {
	case err != nil:
		return "", false, err
	case code == nil:
		return "", false, errors.New("barcode is required")
	}
	item, present, null, err := f.str("item_cloud_id")
	switch {
	case err != nil:
		return "", false, err
	case !present || null:
		return "", false, errors.New("item_cloud_id is required")
	}
	var p data.BarcodePut
	if p.ItemID, err = r.resolve(ctx, tx, kindItem, item); err != nil {
		return "", false, err
	}
	if variant, present, null, err := f.str("variant_cloud_id"); err != nil {
		return "", false, err
	} else if present && !null {
		if p.VariantID, err = r.resolve(ctx, tx, kindItemVariant, variant); err != nil {
			return "", false, err
		}
	}
	if primary, err := f.boolPtr("is_primary"); err != nil {
		return "", false, err
	} else if primary != nil {
		p.IsPrimary = *primary
	}
	resolved, ok := r.barcodes[*code]
	switch {
	case !ok:
		// Cannot happen (resolveBarcodes reads every row the same way);
		// refused, never an infra error, so it cannot wedge the feed.
		return "", false, errors.New("the barcode was not resolved before the chunk began")
	case resolved.err != nil:
		return "", false, resolved.err
	}
	p.Barcode = resolved.res
	local := barcodeLocalID(p.ItemID, p.VariantID, p.Barcode.Key)
	held, err := r.links.HeldByOther(ctx, tx, kindItemBarcode, local, cloudID)
	if err != nil {
		return "", false, err
	}
	if held {
		return "", false, fmt.Errorf("barcode %s is already another cloud barcode row's", p.Barcode.Raw)
	}
	if prev != "" && prev != local {
		if _, err := r.removeBarcode(ctx, tx, cloudID, prev); err != nil {
			return "", false, err
		}
	}
	created, err := r.cat.PutBarcodeTx(ctx, tx, p)
	if err != nil {
		return "", false, err
	}
	return local, created, nil
}

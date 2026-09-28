package pages

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// The till-side hooks behind the manage-shop catalog directives (ut-docs
// reference/manage-shop-catalog-api.md §3; ADR-0095 Decision 1). Each one:
//
//   - refuses on a satellite till (requirePrimaryDirective) — Tick already
//     skips these types there; this is the second line;
//   - applies its whole change in ONE transaction through the repository
//     write path the till's own admin screens share (data.CatalogRepo /
//     data.ModifierRepo) — never a second write path;
//   - writes one auditCloudDirective row per apply that changed something;
//   - is idempotent: the cloud re-serves a directive whose result post was
//     lost, and re-applying leaves the same state and reports success.
//
// Result and error texts are plain, owner-readable English naming the
// object: the cloud shows them verbatim inside a translated frame.

func cloudSaveItem(ctx context.Context, d *common.Deps, p data.ItemPatch) (string, error) {
	if err := requirePrimaryDirective(ctx, d); err != nil {
		return "", err
	}
	repo := data.NewCatalogRepo(d.Db)
	res, err := repo.SaveItem(ctx, p)
	if err != nil {
		var conflict *data.BarcodeConflictError
		if errors.As(err, &conflict) {
			return "", barcodeTakenError(ctx, repo, conflict)
		}
		return "", err
	}
	auditCloudDirective(ctx, d, "item", p.ID, "cloud_item_saved", map[string]any{"created": res.Created, "changed": res.Changed})
	if res.Created {
		return "created " + res.Name, nil
	}
	return "updated " + res.Name, nil
}

// barcodeTakenError turns a barcode conflict into "barcode X is already
// used by Croissant", naming the other item or variant.
func barcodeTakenError(ctx context.Context, repo *data.CatalogRepo, conflict *data.BarcodeConflictError) error {
	owner := ""
	if conflict.TargetType == "variant" {
		if l, ok, err := repo.GetVariantLabel(ctx, conflict.TargetID); err == nil && ok {
			owner = l.Name
		}
	} else if l, ok, err := repo.GetItemLabel(ctx, conflict.TargetID); err == nil && ok {
		owner = l.Name
	}
	if owner == "" {
		owner = "another item"
	}
	code := conflict.Barcode
	if code == "" {
		return fmt.Errorf("a barcode is already used by %s", owner)
	}
	return fmt.Errorf("barcode %s is already used by %s", code, owner)
}

func cloudSaveCategory(ctx context.Context, d *common.Deps, p data.CategorySave) (string, error) {
	if err := requirePrimaryDirective(ctx, d); err != nil {
		return "", err
	}
	res, err := data.NewCatalogRepo(d.Db).SaveCategory(ctx, p)
	if err != nil {
		return "", err
	}
	// ut-docs#2717: an icon from my. replaced the category's uploaded
	// photo — remove the file, as the till's own editor does when an icon
	// replaces an upload. A replaced library tile has no file of its own.
	if res.ClearedImagePath != "" && safeCategoryID(p.ID) && res.ClearedImagePath == categoryThumbURL(p.ID) {
		removeCategoryUpload(p.ID)
	}
	auditCloudDirective(ctx, d, "category", p.ID, "cloud_category_saved", map[string]any{"created": res.Created, "changed": res.Changed})
	if res.Created {
		return "created category " + res.Name, nil
	}
	return "updated category " + res.Name, nil
}

// cloudSetCategoryOrder is the set_category_order hook (contract §3.8,
// ut-docs#3075): the owner's category order from my., as the full ordered
// id list. Primary-gated like every catalog directive — categories sync
// primary-wins (ADR-0011), and sort_order travels with the row, so the
// order reaches satellite tills through that sync.
//
// The cloud-side decode already refuses a blank or duplicate id; this
// re-checks, because nothing else validates a directive against the till.
// An id with no categories row (active or inactive) refuses the whole list
// before anything is written (SetCategorySortOrderKnown, in the write
// transaction) — a plain UPDATE would match no row and report success for
// an order it didn't set.
// Every category the list leaves out keeps its relative order after the
// listed ones (SetCategorySortOrder, ut-docs#2482). Idempotent: when the
// till already shows exactly the order this list produces (a lost-result
// replay), nothing is written and no audit row added — an UPDATE would
// still bump the LAN-sync generation for no change — and the result is
// the same "applied" text.
func cloudSetCategoryOrder(ctx context.Context, d *common.Deps, ids []string) (string, error) {
	if len(ids) == 0 {
		return "", errors.New("missing category_ids")
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			return "", errors.New("blank category id")
		}
		if seen[id] {
			return "", fmt.Errorf("duplicate category id %s", id)
		}
		seen[id] = true
	}
	if err := requirePrimaryDirective(ctx, d); err != nil {
		return "", err
	}
	repo := data.NewCatalogRepo(d.Db)
	msg := fmt.Sprintf("category order applied to %d categories", len(ids))
	current, err := repo.ListCategories(ctx)
	if err != nil {
		return "", err
	}
	if categoryOrderAlreadyApplied(current, ids) {
		return msg, nil
	}
	// The unknown-id refusal happens inside the write transaction
	// (SetCategorySortOrderKnown), so the check and the UPDATEs can't be
	// split by a concurrent write (review of ut-docs#3075).
	if err := repo.SetCategorySortOrderKnown(ctx, ids); err != nil {
		if errors.Is(err, data.ErrCategoryNotFound) {
			id := strings.TrimPrefix(err.Error(), data.ErrCategoryNotFound.Error()+": ")
			return "", fmt.Errorf("category %s is not on this till", id)
		}
		return "", err
	}
	auditCloudDirective(ctx, d, "category", "-", "cloud_category_order_set", map[string]any{"category_ids": ids})
	return msg, nil
}

// categoryOrderAlreadyApplied reports whether current (ListCategories:
// sort_order, name) is exactly what SetCategorySortOrder(ids) would write:
// the listed ids first, then every other category in its current relative
// order, numbered 0..n-1 with no gaps or ties.
func categoryOrderAlreadyApplied(current []data.CategoryNode, ids []string) bool {
	listed := make(map[string]bool, len(ids))
	for _, id := range ids {
		listed[id] = true
	}
	want := append([]string{}, ids...)
	for _, c := range current {
		if !listed[c.ID] {
			want = append(want, c.ID)
		}
	}
	if len(want) != len(current) {
		return false
	}
	for i, c := range current {
		if c.ID != want[i] || c.SortOrder != i {
			return false
		}
	}
	return true
}

func cloudDeleteCategory(ctx context.Context, d *common.Deps, id, moveItemsTo string) (string, error) {
	if err := requirePrimaryDirective(ctx, d); err != nil {
		return "", err
	}
	res, err := data.NewCatalogRepo(d.Db).DeleteCategoryMoving(ctx, id, moveItemsTo)
	if err != nil {
		return "", err
	}
	if res.AlreadyDeleted {
		return "already deleted", nil
	}
	auditCloudDirective(ctx, d, "category", id, "cloud_category_deleted", map[string]any{
		"moved_items": res.MovedItems, "moved_children": res.MovedChildren, "move_items_to": moveItemsTo,
	})
	return fmt.Sprintf("deleted category %s (%d items moved, %d subcategories moved up)", res.Name, res.MovedItems, res.MovedChildren), nil
}

func cloudSaveModifierGroup(ctx context.Context, d *common.Deps, p data.ModifierGroupSave) (string, error) {
	if err := requirePrimaryDirective(ctx, d); err != nil {
		return "", err
	}
	res, err := data.NewModifierRepo(d.Db).SaveGroup(ctx, p)
	if err != nil {
		return "", err
	}
	auditCloudDirective(ctx, d, "modifier_group", p.ID, "cloud_modifier_group_saved", map[string]any{"created": res.Created, "changed": res.Changed})
	if res.Created {
		return "created modifier group " + res.Name, nil
	}
	return "updated modifier group " + res.Name, nil
}

func cloudDeleteModifierGroup(ctx context.Context, d *common.Deps, id string) (string, error) {
	if err := requirePrimaryDirective(ctx, d); err != nil {
		return "", err
	}
	found, err := data.NewModifierRepo(d.Db).DeleteGroupIfExists(ctx, id)
	if err != nil {
		return "", err
	}
	if !found {
		return "already deleted", nil
	}
	auditCloudDirective(ctx, d, "modifier_group", id, "cloud_modifier_group_deleted", nil)
	return "deleted modifier group", nil
}

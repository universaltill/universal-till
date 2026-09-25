package pages

import (
	"context"
	"errors"
	"fmt"

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
	auditCloudDirective(ctx, d, "category", p.ID, "cloud_category_saved", map[string]any{"created": res.Created, "changed": res.Changed})
	if res.Created {
		return "created category " + res.Name, nil
	}
	return "updated category " + res.Name, nil
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

package pages

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/imaging"
	"github.com/universaltill/universal-till/internal/pages/catalog"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
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
	// ut-docs#3584: an icon from my. replaced the item's uploaded photo —
	// remove the file, as the till's own icon picker does
	// (removeUploadedThumbnail). A replaced library tile has no file.
	if res.ClearedImagePath != "" && safeCategoryID(p.ID) && res.ClearedImagePath == catalog.ItemThumbURL(p.ID) {
		catalog.RemoveItemUpload(p.ID)
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
	// An icon from my. no longer replaces an uploaded photo (ut-docs#3585:
	// SaveCategory keeps it), so there is no superseded file to remove.
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

// cloudDeleteItem is the delete_item hook (ut-docs#3317): the till's own
// data decides, never the cloud's "never sold" flag that made my. offer
// Delete. The live cashier and kiosk baskets are memory-only, so they are
// checked here first (the catalog cleanup's own guard shape, one apply
// wide); the repository then re-checks sales, stock history, quick buttons
// and parked sales and deletes in one transaction. A refusal is the
// directive's failure text ("sold 14 times — deactivate it instead").
func cloudDeleteItem(ctx context.Context, d *common.Deps, id string) (string, error) {
	if err := requirePrimaryDirective(ctx, d); err != nil {
		return "", err
	}
	inBasket, err := itemInLiveBasket(ctx, d, id)
	if err != nil {
		return "", err
	}
	if inBasket {
		return "", &data.ItemInUseError{Reason: data.ItemUseOpenBasket}
	}
	res, err := data.NewPOSRepo(d.Db).DeleteUnusedItem(ctx, id)
	if err != nil {
		return "", err
	}
	if res.AlreadyDeleted {
		return "already deleted", nil
	}
	auditCloudDirective(ctx, d, "item", id, "cloud_item_deleted", map[string]any{"name": res.Name})
	return "deleted item " + res.Name, nil
}

// itemInLiveBasket reports whether the cashier or the self-order kiosk
// basket holds a line for itemID or one of its variants.
func itemInLiveBasket(ctx context.Context, d *common.Deps, itemID string) (bool, error) {
	repo := data.NewCatalogRepo(d.Db)
	for _, e := range []*pos.Service{d.Engine, d.KioskEngine} {
		if e == nil {
			continue
		}
		for _, l := range e.Lines() {
			if l.ItemID == itemID {
				return true, nil
			}
			if l.VariantID == "" {
				continue
			}
			parent, ok, err := repo.ItemIDForVariant(ctx, l.VariantID)
			if err != nil {
				return false, err
			}
			if ok && parent == itemID {
				return true, nil
			}
		}
	}
	return false, nil
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

// cloudSaveItemVariant is the save_item_variant hook (ut-docs#3477): one
// CatalogRepo.SaveVariant transaction — create with the cloud-minted id,
// edit, barcode-set replace, deactivate/reactivate. A barcode conflict
// names the other item or variant, as save_item's does.
func cloudSaveItemVariant(ctx context.Context, d *common.Deps, p data.VariantSave) (string, error) {
	if err := requirePrimaryDirective(ctx, d); err != nil {
		return "", err
	}
	repo := data.NewCatalogRepo(d.Db)
	res, err := repo.SaveVariant(ctx, p)
	if err != nil {
		var conflict *data.BarcodeConflictError
		if errors.As(err, &conflict) {
			return "", barcodeTakenError(ctx, repo, conflict)
		}
		return "", err
	}
	auditCloudDirective(ctx, d, "item_variant", p.ID, "cloud_variant_saved", map[string]any{"item_id": p.ItemID, "created": res.Created, "changed": res.Changed})
	label := strings.TrimSpace(res.ItemName + " " + res.Name)
	if res.Created {
		return "created variant " + label, nil
	}
	return "updated variant " + label, nil
}

// optionSetInUseMaxNames caps how many item names the delete_option_set
// refusal lists before "and N more": the result is one line the cloud shows
// verbatim, and the full list already rides the option_sets report.
const optionSetInUseMaxNames = 10

// cloudSaveOptionSet is the save_option_set hook (ut-docs#3319): the same
// OptionSetRepo.SaveOptionSet the till's own /catalog/option-sets screen
// writes through.
func cloudSaveOptionSet(ctx context.Context, d *common.Deps, p data.OptionSetSave) (string, error) {
	if err := requirePrimaryDirective(ctx, d); err != nil {
		return "", err
	}
	res, err := data.NewOptionSetRepo(d.Db).SaveOptionSet(ctx, p)
	if err != nil {
		return "", err
	}
	auditCloudDirective(ctx, d, "option_set", p.ID, "cloud_option_set_saved", map[string]any{"created": res.Created, "changed": res.Changed})
	if res.Created {
		return "created option set " + res.Name, nil
	}
	return "updated option set " + res.Name, nil
}

// cloudDeleteOptionSet is the delete_option_set hook (ut-docs#3319). A set
// still applied to an item is refused — the error names those items — and
// nothing is written; my. disables Delete on a used set already, so this is
// the guard for a view that went stale between load and click.
func cloudDeleteOptionSet(ctx context.Context, d *common.Deps, id string) (string, error) {
	if err := requirePrimaryDirective(ctx, d); err != nil {
		return "", err
	}
	repo := data.NewOptionSetRepo(d.Db)
	set, err := repo.GetOptionSet(ctx, id)
	if errors.Is(err, data.ErrOptionSetNotFound) {
		return "already deleted", nil
	}
	if err != nil {
		return "", err
	}
	found, err := repo.DeleteOptionSetIfUnused(ctx, id)
	if errors.Is(err, data.ErrOptionSetInUse) {
		items, lerr := repo.ItemsUsingOptionSet(ctx, id)
		if lerr != nil {
			return "", fmt.Errorf("option set %s is used by at least one item; remove it from those items first", set.Name)
		}
		return "", errors.New(optionSetInUseMessage(set.Name, items))
	}
	if err != nil {
		return "", err
	}
	if !found {
		return "already deleted", nil
	}
	auditCloudDirective(ctx, d, "option_set", id, "cloud_option_set_deleted", map[string]any{"name": set.Name})
	return "deleted option set " + set.Name, nil
}

// optionSetInUseMessage is the delete_option_set refusal: the set, how many
// items use it, and up to optionSetInUseMaxNames of their names.
func optionSetInUseMessage(setName string, items []data.AssignedItem) string {
	names := make([]string, 0, min(len(items), optionSetInUseMaxNames))
	for i, it := range items {
		if i == optionSetInUseMaxNames {
			break
		}
		names = append(names, it.Name)
	}
	noun := "items"
	if len(items) == 1 {
		noun = "item"
	}
	list := strings.Join(names, ", ")
	if more := len(items) - len(names); more > 0 {
		list += fmt.Sprintf(" and %d more", more)
	}
	return fmt.Sprintf("option set %s is used by %d %s: %s; remove it from those items first", setName, len(items), noun, list)
}

// cloudSetCatalogImage is the set_catalog_image hook (contract §3.9,
// ut-docs#3076/#3139): an item or category image set or removed in my.
// cloudsync has already checked the payload's shape; this checks the id is
// on this till BEFORE fetching anything, then writes through the same
// functions the till's own upload and Remove use (catalog.StoreItemPhoto /
// ClearItemPicture, storeCategoryPhoto / clearCategoryPicture), so the file,
// the item_images row or categories.image_path, and what the sale screen
// shows are exactly what a local upload produces. Satellites get the file
// through the existing asset sync (/api/sync/assets, #2566/#2785); the
// admin nudge makes them pull now.
//
// Idempotent: a replay of the same image rewrites the same file and row and
// reports the same text; when the served photo is unchanged (or a clear
// finds no picture) no audit row is added.
func cloudSetCatalogImage(ctx context.Context, d *common.Deps, img cloudsync.CatalogImage) (string, error) {
	if img.Entity != "item" && img.Entity != "category" {
		return "", fmt.Errorf("unknown entity %s", img.Entity)
	}
	if err := requirePrimaryDirective(ctx, d); err != nil {
		return "", err
	}
	repo := data.NewCatalogRepo(d.Db)
	notHere := fmt.Errorf("%s %s is not on this till", img.Entity, img.ID)
	// An id that could escape the asset directory can't be a row the
	// till's own screens created; refuse it before it reaches a path.
	if !safeCategoryID(img.ID) {
		return "", notHere
	}
	// current is what the entity shows now: its item_images path or its
	// categories.image_path/icon ("" = no picture at all).
	var name, current string
	if img.Entity == "item" {
		l, ok, err := repo.GetItemLabel(ctx, img.ID)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", notHere
		}
		name = l.Name
		if current, _, err = repo.ItemThumbnailPath(ctx, img.ID); err != nil {
			return "", err
		}
		// ut-docs#3584: an icon id is the item's picture too.
		icon, err := repo.ItemIcon(ctx, img.ID)
		if err != nil {
			return "", err
		}
		current += icon
	} else {
		c, ok, err := repo.CategoryPicture(ctx, img.ID)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", notHere
		}
		name, current = c.Name, c.ImagePath+c.Icon
	}
	target := catalog.ItemThumbURL(img.ID)
	if img.Entity == "category" {
		target = categoryThumbURL(img.ID)
	}
	audit := func(action string, payload map[string]any) {
		auditCloudDirective(ctx, d, img.Entity, img.ID, action, payload)
		// A photo is a file on /api/sync/assets, not only a row: tell
		// linked tills to pull now, as the till's own upload does.
		d.NudgeLink(fleetlink.ScopeAdmin)
	}

	if img.Clear {
		msg := fmt.Sprintf("image removed from %s %s", img.Entity, name)
		// Nothing shown and no stray photo file: a replay, or never set.
		if current == "" && cloudsync.ServedImageSHA256(target) == "" {
			return msg, nil
		}
		var err error
		if img.Entity == "item" {
			err = catalog.ClearItemPicture(ctx, repo, img.ID)
		} else {
			err = clearCategoryPicture(ctx, repo, img.ID)
		}
		if err != nil {
			return "", err
		}
		audit("cloud_catalog_image_cleared", nil)
		return msg, nil
	}

	if img.Fetch == nil {
		return "", errors.New("this till cannot download images right now; save the image again to retry")
	}
	raw, err := img.Fetch(ctx)
	if err != nil {
		return "", err
	}
	pic, err := imaging.PrepareThumb(raw)
	if err != nil {
		if errors.Is(err, imaging.ErrTooManyPixels) {
			return "", errors.New("the image from the cloud has too many pixels for this till")
		}
		return "", errors.New("the image from the cloud could not be read as a PNG or JPEG")
	}
	before := ""
	if current == target {
		before = cloudsync.ServedImageSHA256(target)
	}
	if img.Entity == "item" {
		err = catalog.StoreItemPhoto(ctx, repo, img.ID, pic)
	} else {
		err = storeCategoryPhoto(ctx, repo, img.ID, pic)
	}
	if err != nil {
		return "", err
	}
	if after := cloudsync.ServedImageSHA256(target); before == "" || after != before {
		audit("cloud_catalog_image_set", map[string]any{"sha256": img.SHA256, "size": img.Size})
	}
	return fmt.Sprintf("image set on %s %s", img.Entity, name), nil
}

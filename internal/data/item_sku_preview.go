package data

import (
	"context"
	"strings"
)

// PreviewItemSKU predicts, without writing anything, the SKU a blank-SKU
// import row will get at commit (ut-docs#3098) so the preview can show it
// and the operator can edit it first.
//
// department/category follow the import commit's nesting exactly
// (import_page.go: the department is a top-level category, the row's
// category nests under it; with no category the item sits in the
// department itself) and are resolved with the same read-only,
// parent-scoped, case-insensitive lookup ItemExistsByNameAndCategory uses.
//
// A category that exists → nextItemSKU's full rule 1 then rule 2. One
// that does not exist yet (it would be created at commit) has no items, so
// rule 1 can never apply: straight to the prefix scheme from its name —
// the same skuPrefixFromName(name) nextItemSKU derives once commit creates
// it, since EnsureCategoryUnder stores the trimmed name it was given.
//
// reserved holds this preview's earlier predictions (scoped to one request
// by the caller); pass the result back in before the next row so sibling
// rows predict distinct SKUs. The real insert still enforces uniqueness at
// commit, so a prediction that went stale is never a correctness problem.
func (r *CatalogRepo) PreviewItemSKU(ctx context.Context, department, category string, reserved map[string]bool) (string, error) {
	department = strings.TrimSpace(department)
	category = strings.TrimSpace(category)
	name := category
	if name == "" {
		name = department
	}
	if name == "" {
		return nextItemSKU(ctx, r.db, nil, reserved)
	}

	deptID, found := "", true
	if department != "" {
		id, ok, err := r.findCategoryIDUnder(ctx, department, "")
		if err != nil {
			return "", err
		}
		deptID, found = id, ok
	}
	catID := deptID
	if found && category != "" {
		id, ok, err := r.findCategoryIDUnder(ctx, category, deptID)
		if err != nil {
			return "", err
		}
		catID, found = id, ok
	}
	if found {
		return nextItemSKU(ctx, r.db, &catID, reserved)
	}
	return nextPrefixedSKU(ctx, r.db, skuPrefixFromName(name), reserved)
}

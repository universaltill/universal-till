package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// ModifierGroup is a SHOP-WIDE customization choice (e.g. "Extras",
// "Bread") — ADR-0020 / spec 011, shareable across items since ADR-0090
// (ut-docs#2013) via item_modifier_group_links, offered to whole categories
// since ADR-0094 via category_modifier_group_links, and since ADR-0101
// (ut-docs#2399) owned by nobody but the shop: the group row carries no
// item at all (migration 034 dropped the item_id anchor), and a group with
// zero assignments is a valid, persistent state. Options are loaded
// alongside it.
//
// SortOrder is the LINK row's, not the group row's, on every per-item /
// per-category read: the item's (or category's) own position for the
// group. The group row's own sort_order column is a legacy no read path
// consults (see UpdateGroup).
type ModifierGroup struct {
	ID        string
	Name      string
	Required  bool
	MinSelect int
	MaxSelect int
	SortOrder int
	IsActive  bool
	Options   []ModifierOption
	// OptedOut is only populated by ListInheritedGroupsForItem (ADR-0094,
	// ut-docs#1915) — true when the item has declined this category-
	// inherited group via item_modifier_group_opt_outs, for #2284's
	// "inherited, greyed, can override" item panel. The sale-time resolver
	// (ResolveGroupsForItem) drops an opted-out group entirely rather than
	// flagging it, and no other query knows about an item at all. Left false
	// by every other query.
	OptedOut bool
}

// ModifierOption is one selectable choice within a ModifierGroup. Price
// deltas are additive-only in v1 (never negative — see migration 017).
type ModifierOption struct {
	ID              string
	GroupID         string
	Name            string
	PriceDeltaMinor int64
	SortOrder       int
	IsActive        bool
}

type ModifierRepo struct {
	db *sql.DB
}

func NewModifierRepo(db *sql.DB) *ModifierRepo {
	return &ModifierRepo{db: db}
}

// ListGroupsForItem returns an item's active modifier groups (with their
// active options nested), ordered for stable UI rendering. Used by both the
// cashier POS customization step and the (future) kiosk flow.
func (r *ModifierRepo) ListGroupsForItem(ctx context.Context, itemID string) ([]ModifierGroup, error) {
	return r.listGroupsForItem(ctx, itemID, false)
}

// ListAllGroupsForItem returns EVERY modifier group and option for an item,
// active or not — the admin catalog editor needs to show (and let a
// manager reactivate) a deactivated group/option, unlike the sale-time
// ListGroupsForItem which must only ever offer what's currently sellable.
func (r *ModifierRepo) ListAllGroupsForItem(ctx context.Context, itemID string) ([]ModifierGroup, error) {
	return r.listGroupsForItem(ctx, itemID, true)
}

// Membership is read through item_modifier_group_links (ADR-0090): the link
// row says which items use a group and where it sits in each item's list,
// the group row carries the shared rule set.
func (r *ModifierRepo) listGroupsForItem(ctx context.Context, itemID string, includeInactive bool) ([]ModifierGroup, error) {
	groupQuery := `
SELECT g.id, g.name, g.required, g.min_select, g.max_select, l.sort_order, g.is_active
FROM item_modifier_groups g
JOIN item_modifier_group_links l ON l.group_id = g.id
WHERE l.item_id = ?`
	if !includeInactive {
		groupQuery += ` AND g.is_active = 1`
	}
	groupQuery += ` ORDER BY l.sort_order, g.name`

	groupRows, err := r.db.QueryContext(ctx, groupQuery, itemID)
	if err != nil {
		return nil, fmt.Errorf("list modifier groups: %w", err)
	}
	defer groupRows.Close()

	var groups []ModifierGroup
	for groupRows.Next() {
		var g ModifierGroup
		var required, active int
		if err := groupRows.Scan(&g.ID, &g.Name, &required, &g.MinSelect, &g.MaxSelect, &g.SortOrder, &active); err != nil {
			return nil, fmt.Errorf("scan modifier group: %w", err)
		}
		g.Required = required == 1
		g.IsActive = active == 1
		groups = append(groups, g)
	}
	if err := groupRows.Err(); err != nil {
		return nil, fmt.Errorf("list modifier groups: %w", err)
	}
	if len(groups) == 0 {
		return nil, nil
	}
	if err := r.attachOptions(ctx, groups, includeInactive); err != nil {
		return nil, err
	}
	return groups, nil
}

// attachOptions loads the options for every group in groups (one batch
// query, not N+1) and appends them to each group's Options in place. Shared
// by every query here whose result holds each group id AT MOST ONCE
// (listGroupsForItem, listGroupsForCategory, the ADR-0094 inherited/resolver
// paths) — a pointer per id is safe there because each of those reads
// through a link table whose PRIMARY KEY includes group_id. (The shop-wide
// admin read, ListAllModifierGroupsWithAssignments, keys by the group
// table's own PRIMARY KEY and loads every option itself.) Callers guard
// the empty slice themselves (SQLite rejects "IN ()").
//
// Options belong to a group, not to an item or category: scope by the group
// ids already fetched rather than joining through a link table a second
// time.
func (r *ModifierRepo) attachOptions(ctx context.Context, groups []ModifierGroup, includeInactive bool) error {
	byID := make(map[string]*ModifierGroup, len(groups))
	groupIDs := make([]string, len(groups))
	for i := range groups {
		byID[groups[i].ID] = &groups[i]
		groupIDs[i] = groups[i].ID
	}
	placeholders, args := inPlaceholders(groupIDs)
	optQuery := `
SELECT o.id, o.group_id, o.name, o.price_delta_minor, o.sort_order, o.is_active
FROM item_modifier_options o
WHERE o.group_id IN (` + placeholders + `)`
	if !includeInactive {
		optQuery += ` AND o.is_active = 1`
	}
	optQuery += ` ORDER BY o.sort_order, o.name`

	optRows, err := r.db.QueryContext(ctx, optQuery, args...)
	if err != nil {
		return fmt.Errorf("list modifier options: %w", err)
	}
	defer optRows.Close()
	for optRows.Next() {
		var o ModifierOption
		var active int
		if err := optRows.Scan(&o.ID, &o.GroupID, &o.Name, &o.PriceDeltaMinor, &o.SortOrder, &active); err != nil {
			return fmt.Errorf("scan modifier option: %w", err)
		}
		o.IsActive = active == 1
		if g, ok := byID[o.GroupID]; ok {
			g.Options = append(g.Options, o)
		}
	}
	if err := optRows.Err(); err != nil {
		return fmt.Errorf("list modifier options: %w", err)
	}
	return nil
}

// ResolveGroupsForItem is the SALE-TIME resolver behind every add-to-basket
// customization step (the cashier picker and submit path in
// pos_modifiers_api.go, the self-order kiosk in self_order_shop.go, and the
// barcode/variant path in pos_api.go) since ADR-0094 (ut-docs#1915):
//
//	resolved(item) = item's own directly-linked ACTIVE groups
//	               ∪ ( item.category's directly-linked ACTIVE groups
//	                   \ item's opt-out set )
//
// The item's own groups come first, in exactly ListGroupsForItem's order
// (that query is unchanged and still serves any caller wanting only the
// narrower direct-link scope); the surviving category-inherited groups are
// appended after them in the category link's own sort_order/name order —
// two ordered runs, never one merged sort. A group both directly linked and
// category-linked appears once, as the item's OWN copy (its own link's
// SortOrder — the more specific attachment wins for display position,
// ADR-0094 §3). An item with no category, or no surviving
// inherited group, resolves to exactly what ListGroupsForItem returns.
//
// This is a read-time join, deliberately not a per-item snapshot or cache
// (ADR-0094 §3): editing a category's groups, an item's category or an
// opt-out takes effect on the very next add-to-basket, with no backfill.
// Active options are loaded for every returned group, same as
// ListGroupsForItem.
func (r *ModifierRepo) ResolveGroupsForItem(ctx context.Context, itemID string) ([]ModifierGroup, error) {
	own, err := r.listGroupsForItem(ctx, itemID, false)
	if err != nil {
		return nil, err
	}
	inherited, err := r.inheritedGroupsForItem(ctx, itemID)
	if err != nil {
		return nil, err
	}
	if len(inherited) == 0 {
		return own, nil
	}
	ownIDs := make(map[string]bool, len(own))
	for _, g := range own {
		ownIDs[g.ID] = true
	}
	// Opt-out wins over inheritance; a directly-linked copy wins over an
	// inherited one. OptedOut itself stays false on everything returned —
	// it is ListInheritedGroupsForItem's field, not this resolver's.
	var extra []ModifierGroup
	for _, g := range inherited {
		if g.OptedOut || ownIDs[g.ID] {
			continue
		}
		extra = append(extra, g)
	}
	if len(extra) == 0 {
		return own, nil
	}
	if err := r.attachOptions(ctx, extra, false); err != nil {
		return nil, err
	}
	return append(own, extra...), nil
}

// ListInheritedGroupsForItem returns the ACTIVE modifier groups an item
// inherits from its category — every group linked to the item's category
// via category_modifier_group_links, whether or not the item has opted out —
// with OptedOut set on each from item_modifier_group_opt_outs and active
// options loaded. This is the read behind #2284's item-panel "inherited,
// greyed, can override" display (ADR-0094 §5): the admin needs to see an
// opted-out group to be able to opt back IN, which is exactly why this is a
// separate method from ResolveGroupsForItem (which drops opted-out groups).
// An item with no category (items.category_id IS NULL), or one that doesn't
// exist, inherits nothing: nil, nil. SortOrder is the CATEGORY link's own
// position — an inherited group is category-attached, not item-scoped.
func (r *ModifierRepo) ListInheritedGroupsForItem(ctx context.Context, itemID string) ([]ModifierGroup, error) {
	if itemID == "" {
		return nil, errors.New("item_id required")
	}
	groups, err := r.inheritedGroupsForItem(ctx, itemID)
	if err != nil || len(groups) == 0 {
		return nil, err
	}
	if err := r.attachOptions(ctx, groups, false); err != nil {
		return nil, err
	}
	return groups, nil
}

// inheritedGroupsForItem is the shared read under ResolveGroupsForItem and
// ListInheritedGroupsForItem: the item's category's ACTIVE linked groups,
// in the category link's sort_order/name order, each with OptedOut set from
// item_modifier_group_opt_outs — options NOT yet loaded, because the two
// callers want them on different subsets (the resolver first drops the
// opted-out rows). One query: the category comes from joining items on
// category_id, so an item with a NULL category_id (or no items row at all)
// simply matches no rows — nil, nil — with no separate lookup round trip.
// (category_id, group_id) is the link table's PRIMARY KEY and an item has
// one category, so each group id appears at most once here (attachOptions'
// precondition).
func (r *ModifierRepo) inheritedGroupsForItem(ctx context.Context, itemID string) ([]ModifierGroup, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT g.id, g.name, g.required, g.min_select, g.max_select, l.sort_order, g.is_active,
       EXISTS (
         SELECT 1 FROM item_modifier_group_opt_outs oo
         WHERE oo.item_id = i.id AND oo.group_id = g.id
       )
FROM items i
JOIN category_modifier_group_links l ON l.category_id = i.category_id
JOIN item_modifier_groups g ON g.id = l.group_id
WHERE i.id = ? AND g.is_active = 1
ORDER BY l.sort_order, g.name`, itemID)
	if err != nil {
		return nil, fmt.Errorf("list inherited modifier groups: %w", err)
	}
	defer rows.Close()
	var groups []ModifierGroup
	for rows.Next() {
		var g ModifierGroup
		var required, active, optedOut int
		if err := rows.Scan(&g.ID, &g.Name, &required, &g.MinSelect, &g.MaxSelect, &g.SortOrder, &active, &optedOut); err != nil {
			return nil, fmt.Errorf("scan inherited modifier group: %w", err)
		}
		g.Required = required == 1
		g.IsActive = active == 1
		g.OptedOut = optedOut == 1
		groups = append(groups, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list inherited modifier groups: %w", err)
	}
	return groups, nil
}

// ListGroupsForCategory returns a category's ACTIVE linked modifier groups
// (with their active options nested), in the category link's own
// sort_order/name order — ADR-0094 (ut-docs#1915). This is the category-side
// twin of ListGroupsForItem for #2284's category editor; sale-time
// resolution goes through ResolveGroupsForItem, which applies the item's
// opt-outs on top of this set.
func (r *ModifierRepo) ListGroupsForCategory(ctx context.Context, categoryID string) ([]ModifierGroup, error) {
	return r.listGroupsForCategory(ctx, categoryID, false)
}

// ListAllGroupsForCategory returns EVERY modifier group and option linked
// to a category, active or not — the admin category editor (#2284) needs to
// show (and let a manager unlink) a deactivated group, same reason
// ListAllGroupsForItem exists beside the sale-time ListGroupsForItem.
func (r *ModifierRepo) ListAllGroupsForCategory(ctx context.Context, categoryID string) ([]ModifierGroup, error) {
	return r.listGroupsForCategory(ctx, categoryID, true)
}

// Membership is read through category_modifier_group_links (ADR-0094),
// the exact mirror of listGroupsForItem over item_modifier_group_links:
// the link row says which categories offer a group and where it sits in
// each category's list, the group row carries the shared rule set.
func (r *ModifierRepo) listGroupsForCategory(ctx context.Context, categoryID string, includeInactive bool) ([]ModifierGroup, error) {
	if categoryID == "" {
		return nil, errors.New("category_id required")
	}
	groupQuery := `
SELECT g.id, g.name, g.required, g.min_select, g.max_select, l.sort_order, g.is_active
FROM item_modifier_groups g
JOIN category_modifier_group_links l ON l.group_id = g.id
WHERE l.category_id = ?`
	if !includeInactive {
		groupQuery += ` AND g.is_active = 1`
	}
	groupQuery += ` ORDER BY l.sort_order, g.name`

	groupRows, err := r.db.QueryContext(ctx, groupQuery, categoryID)
	if err != nil {
		return nil, fmt.Errorf("list category modifier groups: %w", err)
	}
	defer groupRows.Close()

	var groups []ModifierGroup
	for groupRows.Next() {
		var g ModifierGroup
		var required, active int
		if err := groupRows.Scan(&g.ID, &g.Name, &required, &g.MinSelect, &g.MaxSelect, &g.SortOrder, &active); err != nil {
			return nil, fmt.Errorf("scan category modifier group: %w", err)
		}
		g.Required = required == 1
		g.IsActive = active == 1
		groups = append(groups, g)
	}
	if err := groupRows.Err(); err != nil {
		return nil, fmt.Errorf("list category modifier groups: %w", err)
	}
	if len(groups) == 0 {
		return nil, nil
	}
	if err := r.attachOptions(ctx, groups, includeInactive); err != nil {
		return nil, err
	}
	return groups, nil
}

// inPlaceholders renders a "?, ?, ?" list and its matching args for an SQL
// IN (...) clause over ids. Callers must guard against an empty slice
// themselves (SQLite rejects "IN ()").
func inPlaceholders(ids []string) (string, []any) {
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	return strings.Join(placeholders, ","), args
}

// AssignedCategory is one category a modifier group is linked to, as the
// shop-wide admin read reports it (ADR-0101 §3).
type AssignedCategory struct {
	ID   string
	Name string
}

// AssignedItem is one item a modifier group is DIRECTLY linked to (an
// item_modifier_group_links row — never a category inheritance), as the
// shop-wide admin read reports it. IsActive is the ITEM's flag: /modifiers
// still lists a link to a deactivated item so the merchant can see and
// remove it, rather than silently hiding a row that ResolveGroupsForItem
// will never reach.
type AssignedItem struct {
	ID       string
	Name     string
	IsActive bool
}

// ModifierGroupAdmin is one modifier group as the shop-wide /modifiers
// screen needs it (ADR-0101 §3): the group with EVERY option (active or
// not, so a deactivated one can be reactivated), plus where it is
// currently assigned. Both assignment lists are in the link rows' own
// sort_order/name order. A group with neither is the "not assigned yet"
// state the screen calls out.
type ModifierGroupAdmin struct {
	ModifierGroup
	Categories []AssignedCategory
	Items      []AssignedItem
}

// ListAllModifierGroupsWithAssignments returns every modifier group in the
// shop ONCE — active or not, assigned or not — ordered by name then id,
// each with all of its options and its category/item assignments — the
// read behind the /modifiers screen (ADR-0101 §3, ut-docs#2399). It
// replaces the per-link fan-out of the old ListShopModifierGroups /
// ListAllShopModifierGroups (a group linked to three items came back as
// three rows, and a group linked to nothing never came back at all, which
// is exactly the "a modifier can only exist attached to an item" premise
// ADR-0101 withdraws). Four queries in total — groups, options, category
// links JOIN categories, item links JOIN items — joined in Go by group id,
// never one query per group.
func (r *ModifierRepo) ListAllModifierGroupsWithAssignments(ctx context.Context) ([]ModifierGroupAdmin, error) {
	groupRows, err := r.db.QueryContext(ctx, `
SELECT g.id, g.name, g.required, g.min_select, g.max_select, g.sort_order, g.is_active
FROM item_modifier_groups g
ORDER BY g.name, g.id`)
	if err != nil {
		return nil, fmt.Errorf("list modifier groups with assignments: %w", err)
	}
	defer groupRows.Close()
	var groups []ModifierGroupAdmin
	byID := map[string]int{}
	for groupRows.Next() {
		var g ModifierGroupAdmin
		var required, active int
		if err := groupRows.Scan(&g.ID, &g.Name, &required, &g.MinSelect, &g.MaxSelect, &g.SortOrder, &active); err != nil {
			return nil, fmt.Errorf("scan modifier group: %w", err)
		}
		g.Required = required == 1
		g.IsActive = active == 1
		byID[g.ID] = len(groups)
		groups = append(groups, g)
	}
	if err := groupRows.Err(); err != nil {
		return nil, fmt.Errorf("list modifier groups with assignments: %w", err)
	}
	if len(groups) == 0 {
		return nil, nil
	}

	optRows, err := r.db.QueryContext(ctx, `
SELECT o.id, o.group_id, o.name, o.price_delta_minor, o.sort_order, o.is_active
FROM item_modifier_options o
ORDER BY o.sort_order, o.name`)
	if err != nil {
		return nil, fmt.Errorf("list modifier options: %w", err)
	}
	defer optRows.Close()
	for optRows.Next() {
		var o ModifierOption
		var active int
		if err := optRows.Scan(&o.ID, &o.GroupID, &o.Name, &o.PriceDeltaMinor, &o.SortOrder, &active); err != nil {
			return nil, fmt.Errorf("scan modifier option: %w", err)
		}
		o.IsActive = active == 1
		if i, ok := byID[o.GroupID]; ok {
			groups[i].Options = append(groups[i].Options, o)
		}
	}
	if err := optRows.Err(); err != nil {
		return nil, fmt.Errorf("list modifier options: %w", err)
	}

	catRows, err := r.db.QueryContext(ctx, `
SELECT l.group_id, c.id, c.name
FROM category_modifier_group_links l
JOIN categories c ON c.id = l.category_id
ORDER BY l.sort_order, c.name`)
	if err != nil {
		return nil, fmt.Errorf("list modifier group category links: %w", err)
	}
	defer catRows.Close()
	for catRows.Next() {
		var groupID string
		var c AssignedCategory
		if err := catRows.Scan(&groupID, &c.ID, &c.Name); err != nil {
			return nil, fmt.Errorf("scan modifier group category link: %w", err)
		}
		if i, ok := byID[groupID]; ok {
			groups[i].Categories = append(groups[i].Categories, c)
		}
	}
	if err := catRows.Err(); err != nil {
		return nil, fmt.Errorf("list modifier group category links: %w", err)
	}

	itemRows, err := r.db.QueryContext(ctx, `
SELECT l.group_id, i.id, i.name, i.is_active
FROM item_modifier_group_links l
JOIN items i ON i.id = l.item_id
ORDER BY l.sort_order, i.name`)
	if err != nil {
		return nil, fmt.Errorf("list modifier group item links: %w", err)
	}
	defer itemRows.Close()
	for itemRows.Next() {
		var groupID string
		var it AssignedItem
		var active int
		if err := itemRows.Scan(&groupID, &it.ID, &it.Name, &active); err != nil {
			return nil, fmt.Errorf("scan modifier group item link: %w", err)
		}
		it.IsActive = active == 1
		if i, ok := byID[groupID]; ok {
			groups[i].Items = append(groups[i].Items, it)
		}
	}
	if err := itemRows.Err(); err != nil {
		return nil, fmt.Errorf("list modifier group item links: %w", err)
	}
	return groups, nil
}

// ItemIDsWithModifiers reports which of the given item IDs have at least
// one active modifier group AVAILABLE AT SALE TIME — direct links, or a
// non-opted-out category inheritance (ADR-0094, ut-docs#1915) — one batch
// query, not N+1, for rendering a button grid where each tile needs to know
// whether tapping it should open the customization step first. This MUST
// stay in lockstep with ResolveGroupsForItem's own resolution rule
// (independent-review finding on ut-docs#1915: shipped once reading only
// item_modifier_group_links, which left every category-only item's tile
// skipping the picker and adding straight to the basket — the tile-tap gate
// disagreeing with the sale-time resolver is a correctness bug, not a
// cosmetic one). Items with no active/reachable group are simply absent
// from the returned set (not present == false).
func (r *ModifierRepo) ItemIDsWithModifiers(ctx context.Context, itemIDs []string) (map[string]bool, error) {
	result := map[string]bool{}
	if len(itemIDs) == 0 {
		return result, nil
	}
	placeholders, directArgs := inPlaceholders(itemIDs)
	_, inheritedArgs := inPlaceholders(itemIDs)
	args := append(directArgs, inheritedArgs...)
	query := `
SELECT DISTINCT l.item_id
FROM item_modifier_group_links l
JOIN item_modifier_groups g ON g.id = l.group_id
WHERE g.is_active = 1 AND l.item_id IN (` + placeholders + `)
UNION
SELECT DISTINCT i.id
FROM items i
JOIN category_modifier_group_links cl ON cl.category_id = i.category_id
JOIN item_modifier_groups g ON g.id = cl.group_id
WHERE g.is_active = 1 AND i.id IN (` + placeholders + `)
  AND NOT EXISTS (
    SELECT 1 FROM item_modifier_group_opt_outs oo
    WHERE oo.item_id = i.id AND oo.group_id = g.id
  )`
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("item ids with modifiers: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan item id with modifiers: %w", err)
		}
		result[id] = true
	}
	return result, rows.Err()
}

// CreateGroup adds a SHOP-WIDE modifier group — a name and its rule set,
// no item (ADR-0101 Decision 2, ut-docs#2399). Returns the new group id.
// It is offered at checkout only once something links it: a caller that
// has an item or category in hand follows this with LinkGroupToItem /
// LinkGroupToCategory (two calls, deliberately — see cloudUpsertModifierGroup
// and the /api/catalog/modifier-group create branch), and a group with no
// link at all is a valid, persistent state that /modifiers lists and
// ResolveGroupsForItem simply never reaches.
func (r *ModifierRepo) CreateGroup(ctx context.Context, id, name string, required bool, minSelect, maxSelect, sortOrder int) (string, error) {
	if id == "" {
		return "", errors.New("id required")
	}
	if name == "" {
		return "", errors.New("name required")
	}
	req := 0
	if required {
		req = 1
	}
	if _, err := r.db.ExecContext(ctx, `
INSERT INTO item_modifier_groups (id, name, required, min_select, max_select, sort_order)
VALUES (?, ?, ?, ?, ?, ?)
`, id, name, req, minSelect, maxSelect, sortOrder); err != nil {
		return "", fmt.Errorf("insert modifier group: %w", err)
	}
	return id, nil
}

// CreateGroupWithOptions creates a modifier group, links it to itemID (when
// non-empty, at sortOrder), and inserts every option — all inside ONE
// transaction, so a real infra failure partway through (SQLITE_BUSY under a
// concurrent sale write, disk I/O — not a validation failure, since callers
// pre-validate options) leaves nothing behind instead of a half-created
// group (ut-docs#2375, follow-up from the ut-docs#2322 review's
// compensating-delete workaround, which this replaces in
// cloudUpsertModifierGroup). Each option's ID is generated here when left
// blank. Returns the new group id.
func (r *ModifierRepo) CreateGroupWithOptions(ctx context.Context, groupID, itemID, name string, required bool, minSelect, maxSelect, sortOrder int, options []ModifierOption) (string, error) {
	if groupID == "" {
		return "", errors.New("id required")
	}
	if name == "" {
		return "", errors.New("name required")
	}
	req := 0
	if required {
		req = 1
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("create group with options: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit

	if _, err := tx.ExecContext(ctx, `
INSERT INTO item_modifier_groups (id, name, required, min_select, max_select, sort_order)
VALUES (?, ?, ?, ?, ?, ?)
`, groupID, name, req, minSelect, maxSelect, sortOrder); err != nil {
		return "", fmt.Errorf("create group with options: insert group: %w", err)
	}

	if itemID != "" {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO item_modifier_group_links (item_id, group_id, sort_order)
VALUES (?, ?, ?)
ON CONFLICT(item_id, group_id) DO UPDATE SET sort_order = excluded.sort_order
`, itemID, groupID, sortOrder); err != nil {
			return "", fmt.Errorf("create group with options: link item: %w", err)
		}
	}

	for _, opt := range options {
		if opt.Name == "" {
			return "", errors.New("option name required")
		}
		if opt.PriceDeltaMinor < 0 {
			return "", errors.New("price_delta_minor must be >= 0 (additive-only in v1)")
		}
		optID := opt.ID
		if optID == "" {
			optID = uuid.NewString()
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO item_modifier_options (id, group_id, name, price_delta_minor, sort_order)
VALUES (?, ?, ?, ?, ?)
`, optID, groupID, opt.Name, opt.PriceDeltaMinor, opt.SortOrder); err != nil {
			return "", fmt.Errorf("create group with options: insert option: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("create group with options: commit: %w", err)
	}
	return groupID, nil
}

// LinkGroupToItem attaches an existing modifier group to (another) item, or
// updates the group's per-item sort order if the link already exists
// (ON CONFLICT DO UPDATE) — ADR-0090's many-to-many write path, wired up by
// POST /api/catalog/modifier-group/attach's "attach an existing group"
// picker (ut-docs#2046). The ON CONFLICT branch exists so a resubmit
// settles safely; the handler itself re-validates against
// ListAttachableModifierGroups before calling this, so a stale picker can't
// use it to silently re-order an already-linked group.
func (r *ModifierRepo) LinkGroupToItem(ctx context.Context, itemID, groupID string, sortOrder int) error {
	if itemID == "" {
		return errors.New("item_id required")
	}
	if groupID == "" {
		return errors.New("group_id required")
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO item_modifier_group_links (item_id, group_id, sort_order)
VALUES (?, ?, ?)
ON CONFLICT(item_id, group_id) DO UPDATE SET sort_order = excluded.sort_order
`, itemID, groupID, sortOrder)
	if err != nil {
		return fmt.Errorf("link modifier group to item: %w", err)
	}
	return nil
}

// UnlinkGroupFromItem detaches a modifier group from one item — including
// its last remaining link. It never deletes the group ROW itself
// (DeleteGroup is the separate, explicit "remove everywhere" action): a
// group with no link left is simply unassigned, still listed and editable
// on /modifiers (ListAllModifierGroupsWithAssignments reads the group
// table directly, not through a link), and not offered at checkout until
// it is assigned again. This is POST /api/catalog/modifier-group/detach's
// write since ADR-0101 (ut-docs#2399); the earlier last-link refusal
// (UnlinkGroupFromItemUnlessLastLink, ut-docs#2046) existed only because
// the old per-item /modifiers listing could not show an orphan, and went
// with that listing.
func (r *ModifierRepo) UnlinkGroupFromItem(ctx context.Context, itemID, groupID string) error {
	if itemID == "" {
		return errors.New("item_id required")
	}
	if groupID == "" {
		return errors.New("group_id required")
	}
	_, err := r.db.ExecContext(ctx, `DELETE FROM item_modifier_group_links WHERE item_id = ? AND group_id = ?`, itemID, groupID)
	if err != nil {
		return fmt.Errorf("unlink modifier group from item: %w", err)
	}
	return nil
}

// LinkGroupToCategory attaches an existing modifier group to a category, so
// every item in that category inherits it at sale time (ResolveGroupsForItem)
// unless the item opts out — ADR-0094 (ut-docs#1915), the category-side
// twin of LinkGroupToItem, to be wired up by #2284's category editor. Same
// ON CONFLICT DO UPDATE shape: re-linking an already-linked group just
// updates its per-category sort order, so a resubmit settles safely. A
// category never becomes a group's owner through this (ADR-0094 Decision
// 1, and since ADR-0101 nothing does): the group's item_modifier_group_links
// rows are untouched.
func (r *ModifierRepo) LinkGroupToCategory(ctx context.Context, categoryID, groupID string, sortOrder int) error {
	if categoryID == "" {
		return errors.New("category_id required")
	}
	if groupID == "" {
		return errors.New("group_id required")
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO category_modifier_group_links (category_id, group_id, sort_order)
VALUES (?, ?, ?)
ON CONFLICT(category_id, group_id) DO UPDATE SET sort_order = excluded.sort_order
`, categoryID, groupID, sortOrder)
	if err != nil {
		return fmt.Errorf("link modifier group to category: %w", err)
	}
	return nil
}

// UnlinkGroupFromCategory detaches a modifier group from one category —
// ADR-0094 (ut-docs#1915). A plain unconditional DELETE: a category is
// never a group's owner (ADR-0094 Decision 1), so removing a category link
// only ever changes what that category's items inherit. Existing opt-out
// rows for (item, group) are left alone: they are keyed by item and group,
// not by category, and simply become dormant until the group is inherited
// again.
func (r *ModifierRepo) UnlinkGroupFromCategory(ctx context.Context, categoryID, groupID string) error {
	if categoryID == "" {
		return errors.New("category_id required")
	}
	if groupID == "" {
		return errors.New("group_id required")
	}
	_, err := r.db.ExecContext(ctx, `DELETE FROM category_modifier_group_links WHERE category_id = ? AND group_id = ?`, categoryID, groupID)
	if err != nil {
		return fmt.Errorf("unlink modifier group from category: %w", err)
	}
	return nil
}

// SetCategoryModifierGroups replaces a category's linked modifier groups
// with exactly the given set, in one transaction — the category editor's
// multi-select Save (ut-docs#2284). The same replace-all shape as
// POSRepo.SetCategoryStationRoutes, and for the same reason: a diff-based
// sequence of LinkGroupToCategory/UnlinkGroupFromCategory calls could
// half-apply if a second save raced it. It manages the exact same
// category_modifier_group_links rows those two primitives do (both still
// serve a caller that wants one link at a time), so every ADR-0094
// consequence holds unchanged: a category never becomes a group's owner,
// item links and opt-out rows are untouched, and the sale-time resolver
// sees the new set on the very next add-to-basket. Submitted order becomes
// each link's sort_order (the order the category's items are asked in);
// blank and repeated ids are dropped; an empty set clears every link.
func (r *ModifierRepo) SetCategoryModifierGroups(ctx context.Context, categoryID string, groupIDs []string) error {
	if categoryID == "" {
		return errors.New("category_id required")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("set category modifier groups: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if _, err := tx.ExecContext(ctx, `DELETE FROM category_modifier_group_links WHERE category_id = ?`, categoryID); err != nil {
		return fmt.Errorf("set category modifier groups: delete: %w", err)
	}
	seen := map[string]bool{}
	sortOrder := 0
	for _, gid := range groupIDs {
		gid = strings.TrimSpace(gid)
		if gid == "" || seen[gid] {
			continue
		}
		seen[gid] = true
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO category_modifier_group_links (category_id, group_id, sort_order) VALUES (?, ?, ?)`,
			categoryID, gid, sortOrder); err != nil {
			return fmt.Errorf("set category modifier groups: insert: %w", err)
		}
		sortOrder++
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("set category modifier groups: commit: %w", err)
	}
	return nil
}

// AllCategoryModifierGroupLinks returns every category's linked group ids
// (active or not — this is the admin editor's configured state, same as
// ListAllGroupsForCategory) in one query, keyed by category id, each list
// in the category link's own sort_order/name order — the categories admin
// list's per-row prefill (ut-docs#2284), mirroring
// POSRepo.AllCategoryStationRoutes so rendering N category rows never
// costs N per-category queries.
func (r *ModifierRepo) AllCategoryModifierGroupLinks(ctx context.Context) (map[string][]string, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT l.category_id, l.group_id
FROM category_modifier_group_links l
JOIN item_modifier_groups g ON g.id = l.group_id
ORDER BY l.category_id, l.sort_order, g.name`)
	if err != nil {
		return nil, fmt.Errorf("all category modifier group links: %w", err)
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var catID, groupID string
		if err := rows.Scan(&catID, &groupID); err != nil {
			return nil, fmt.Errorf("scan category modifier group link: %w", err)
		}
		out[catID] = append(out[catID], groupID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate category modifier group links: %w", err)
	}
	return out, nil
}

// ListActiveModifierGroups returns every ACTIVE modifier group in the shop,
// by name, with no item scoping and no options loaded — the category
// editor's multi-select source (ut-docs#2284), the category-side twin of
// ListAttachableModifierGroups' item-scoped picker and with the same
// active-only rule (offering a group nobody can currently sell would only
// confuse). Deliberately NOT ListShopModifierGroups' per-link fan-out: a
// group linked to three items must appear once here, not three times.
func (r *ModifierRepo) ListActiveModifierGroups(ctx context.Context) ([]ModifierGroup, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT g.id, g.name, g.required, g.min_select, g.max_select
FROM item_modifier_groups g
WHERE g.is_active = 1
ORDER BY g.name`)
	if err != nil {
		return nil, fmt.Errorf("list active modifier groups: %w", err)
	}
	defer rows.Close()
	var groups []ModifierGroup
	for rows.Next() {
		var g ModifierGroup
		var required int
		if err := rows.Scan(&g.ID, &g.Name, &required, &g.MinSelect, &g.MaxSelect); err != nil {
			return nil, fmt.Errorf("scan active modifier group: %w", err)
		}
		g.Required = required == 1
		g.IsActive = true
		groups = append(groups, g)
	}
	return groups, rows.Err()
}

// OptOutItemFromGroup records that an item declines a category-inherited
// modifier group — ADR-0094 Decision 2 (ut-docs#1915): a presence-only row
// in item_modifier_group_opt_outs, which ResolveGroupsForItem subtracts from
// the item's inherited set and ListInheritedGroupsForItem reports as
// OptedOut. INSERT OR IGNORE, so opting out twice is a no-op rather than a
// PK violation. This only ever suppresses a CATEGORY-inherited group: a
// group the item is DIRECTLY linked to via item_modifier_group_links is
// unaffected by an opt-out row — detaching that is UnlinkGroupFromItem's
// job, keeping the two mechanisms non-overlapping (direct links are
// added/removed; inheritance is accepted/opted-out). No FK or existence check beyond the row's own
// foreign keys: an opt-out for a group the category doesn't (yet) link is
// simply dormant, not an error.
func (r *ModifierRepo) OptOutItemFromGroup(ctx context.Context, itemID, groupID string) error {
	if itemID == "" {
		return errors.New("item_id required")
	}
	if groupID == "" {
		return errors.New("group_id required")
	}
	_, err := r.db.ExecContext(ctx, `INSERT OR IGNORE INTO item_modifier_group_opt_outs (item_id, group_id) VALUES (?, ?)`, itemID, groupID)
	if err != nil {
		return fmt.Errorf("opt item out of modifier group: %w", err)
	}
	return nil
}

// OptInItemToGroup reverses OptOutItemFromGroup: deleting the opt-out row
// is the whole of "opting back in" (ADR-0094 Decision 2 — there is no
// separate "explicit yes" state to model, symmetric with how
// item_modifier_group_links has no "explicit no" row for a group an item
// was never linked to). Deleting a row that isn't there is a no-op.
func (r *ModifierRepo) OptInItemToGroup(ctx context.Context, itemID, groupID string) error {
	if itemID == "" {
		return errors.New("item_id required")
	}
	if groupID == "" {
		return errors.New("group_id required")
	}
	_, err := r.db.ExecContext(ctx, `DELETE FROM item_modifier_group_opt_outs WHERE item_id = ? AND group_id = ?`, itemID, groupID)
	if err != nil {
		return fmt.Errorf("opt item in to modifier group: %w", err)
	}
	return nil
}

// ListAttachableModifierGroups returns every ACTIVE modifier group in the
// shop not already linked to itemID — backs the "attach an existing group"
// picker (ut-docs#2046). Only active groups are offered: putting a group
// nobody can currently sell in front of a merchant picking what to add would
// be confusing, the same reasoning ListGroupsForItem already applies at sale
// time. No options are loaded — the picker only needs id/name/rules to
// render a dropdown, not a group's full option list.
func (r *ModifierRepo) ListAttachableModifierGroups(ctx context.Context, itemID string) ([]ModifierGroup, error) {
	if itemID == "" {
		return nil, errors.New("item_id required")
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT g.id, g.name, g.required, g.min_select, g.max_select
FROM item_modifier_groups g
WHERE g.is_active = 1
  AND NOT EXISTS (
    SELECT 1 FROM item_modifier_group_links l
    WHERE l.group_id = g.id AND l.item_id = ?
  )
ORDER BY g.name`, itemID)
	if err != nil {
		return nil, fmt.Errorf("list attachable modifier groups: %w", err)
	}
	defer rows.Close()
	var groups []ModifierGroup
	for rows.Next() {
		var g ModifierGroup
		var required int
		if err := rows.Scan(&g.ID, &g.Name, &required, &g.MinSelect, &g.MaxSelect); err != nil {
			return nil, fmt.Errorf("scan attachable modifier group: %w", err)
		}
		g.Required = required == 1
		g.IsActive = true
		groups = append(groups, g)
	}
	return groups, rows.Err()
}

// NextGroupSortOrderForItem reports the sort_order an item's NEXT attached
// group should get, so it's appended after the item's own existing groups
// rather than colliding with one of them (ut-docs#2046, independent-review
// finding). MAX(sort_order)+1, not a plain COUNT of the item's existing
// links: sort_order is per-item positional (ModifierGroup.SortOrder's own
// doc comment — "that item's own position for the group"), and a prior
// detach can leave it sparse (e.g. an item with two groups at sort_order 0
// and 2, after its middle one was detached) — a count would then compute 2
// for the new link, colliding with the existing sort_order-2 row and
// falling back to alphabetical ordering (listGroupsForItem's own
// `ORDER BY l.sort_order, g.name`) instead of appending last as intended.
func (r *ModifierRepo) NextGroupSortOrderForItem(ctx context.Context, itemID string) (int, error) {
	if itemID == "" {
		return 0, errors.New("item_id required")
	}
	var next int
	err := r.db.QueryRowContext(ctx, `
SELECT COALESCE(MAX(sort_order) + 1, 0) FROM item_modifier_group_links WHERE item_id = ?
`, itemID).Scan(&next)
	if err != nil {
		return 0, fmt.Errorf("next group sort order for item: %w", err)
	}
	return next, nil
}

// NextGroupSortOrderForCategory is NextGroupSortOrderForItem's category-side
// twin (ADR-0101 §3, ut-docs#2399): the sort_order a category's NEXT
// linked group should get so /modifiers' per-card "assign to category"
// checkbox appends after the category's existing groups instead of
// colliding with one — MAX(sort_order)+1 over the category's own link
// rows, for the same sparse-after-unlink reason as the item variant.
func (r *ModifierRepo) NextGroupSortOrderForCategory(ctx context.Context, categoryID string) (int, error) {
	if categoryID == "" {
		return 0, errors.New("category_id required")
	}
	var next int
	err := r.db.QueryRowContext(ctx, `
SELECT COALESCE(MAX(sort_order) + 1, 0) FROM category_modifier_group_links WHERE category_id = ?
`, categoryID).Scan(&next)
	if err != nil {
		return 0, fmt.Errorf("next group sort order for category: %w", err)
	}
	return next, nil
}

// UpdateGroup edits an existing modifier group's fields.
//
// sortOrder here writes ONLY item_modifier_groups.sort_order — a legacy
// column no per-item/per-category read path consults (they read the LINK
// row's own sort_order). No template in web/ui submits a sortOrder field
// to this call today, so this is a harmless no-op in practice. Per-item
// ordering is LinkGroupToItem's ON CONFLICT DO UPDATE SET sort_order;
// per-category ordering is LinkGroupToCategory's.
func (r *ModifierRepo) UpdateGroup(ctx context.Context, id, name string, required bool, minSelect, maxSelect, sortOrder int, isActive bool) error {
	if id == "" {
		return errors.New("id required")
	}
	if name == "" {
		return errors.New("name required")
	}
	req, active := 0, 0
	if required {
		req = 1
	}
	if isActive {
		active = 1
	}
	_, err := r.db.ExecContext(ctx, `
UPDATE item_modifier_groups
SET name = ?, required = ?, min_select = ?, max_select = ?, sort_order = ?, is_active = ?
WHERE id = ?
`, name, req, minSelect, maxSelect, sortOrder, active, id)
	if err != nil {
		return fmt.Errorf("update modifier group: %w", err)
	}
	return nil
}

// DeleteGroup removes a group EVERYWHERE, deliberately: its options, every
// item_modifier_group_links row (every item using it), every
// category_modifier_group_links row and every opt-out all cascade away
// (migration 034's FKs). Wired to POST /api/catalog/modifier-group/delete
// since ADR-0101 (ut-docs#2399) behind a confirm; UnlinkGroupFromItem /
// UnlinkGroupFromCategory are the narrower "just this one" actions, and
// deactivating (UpdateGroup isActive=false) the reversible one. Past sales
// keep their sale_line_modifiers snapshots — that table carries no FK onto
// this one.
func (r *ModifierRepo) DeleteGroup(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("id required")
	}
	_, err := r.db.ExecContext(ctx, `DELETE FROM item_modifier_groups WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete modifier group: %w", err)
	}
	return nil
}

// DeleteUnassignedGroups deletes every modifier group that has NEITHER a
// category link nor an item link (ut-docs#2406, follow-up from the
// ADR-0101/#2399 review's finding L5) — the same "unassigned" definition
// modifiersPageData's UnassignedCount and modifiers.html's own
// .modifier-unassigned hint already use, so a group this touches can never
// be one still offered anywhere. A group with either kind of assignment is
// left completely alone. Same cascade as DeleteGroup (options, and any
// stray opt-out row — there are no links to cascade for an unassigned group
// by definition) and the same past-sales guarantee: sale_line_modifiers
// carries no FK onto this table. Returns how many groups were deleted, so
// the caller can report the count back to the operator; zero is a valid,
// non-error result when nothing is unassigned.
func (r *ModifierRepo) DeleteUnassignedGroups(ctx context.Context) (int, error) {
	res, err := r.db.ExecContext(ctx, `
DELETE FROM item_modifier_groups
WHERE id NOT IN (SELECT group_id FROM item_modifier_group_links)
  AND id NOT IN (SELECT group_id FROM category_modifier_group_links)`)
	if err != nil {
		return 0, fmt.Errorf("delete unassigned modifier groups: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("delete unassigned modifier groups: %w", err)
	}
	return int(n), nil
}

// CreateOption adds a selectable option to a modifier group.
func (r *ModifierRepo) CreateOption(ctx context.Context, id, groupID, name string, priceDeltaMinor int64, sortOrder int) (string, error) {
	if groupID == "" {
		return "", errors.New("group_id required")
	}
	if name == "" {
		return "", errors.New("name required")
	}
	if priceDeltaMinor < 0 {
		return "", errors.New("price_delta_minor must be >= 0 (additive-only in v1)")
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO item_modifier_options (id, group_id, name, price_delta_minor, sort_order)
VALUES (?, ?, ?, ?, ?)
`, id, groupID, name, priceDeltaMinor, sortOrder)
	if err != nil {
		return "", fmt.Errorf("insert modifier option: %w", err)
	}
	return id, nil
}

// UpdateOption edits an existing option's fields.
func (r *ModifierRepo) UpdateOption(ctx context.Context, id, name string, priceDeltaMinor int64, sortOrder int, isActive bool) error {
	if id == "" {
		return errors.New("id required")
	}
	if name == "" {
		return errors.New("name required")
	}
	if priceDeltaMinor < 0 {
		return errors.New("price_delta_minor must be >= 0 (additive-only in v1)")
	}
	active := 0
	if isActive {
		active = 1
	}
	_, err := r.db.ExecContext(ctx, `
UPDATE item_modifier_options
SET name = ?, price_delta_minor = ?, sort_order = ?, is_active = ?
WHERE id = ?
`, name, priceDeltaMinor, sortOrder, active, id)
	if err != nil {
		return fmt.Errorf("update modifier option: %w", err)
	}
	return nil
}

// DeleteOption removes a single option.
func (r *ModifierRepo) DeleteOption(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("id required")
	}
	_, err := r.db.ExecContext(ctx, `DELETE FROM item_modifier_options WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete modifier option: %w", err)
	}
	return nil
}

// SelectedModifier is one chosen option, snapshotted at sale time (name +
// price delta as they were when the sale happened — never re-read from the
// option row later). GroupID/OptionID are kept for reporting only.
type SelectedModifier struct {
	GroupID         string
	OptionID        string
	GroupName       string
	OptionName      string
	PriceDeltaMinor int64
}

// InsertSaleLineModifiers persists a sale line's chosen modifiers. Called
// inside the same transaction as InsertSaleLine, after it, using the same
// lineID.
func (r *POSRepo) InsertSaleLineModifiers(ctx context.Context, tx *sql.Tx, lineID string, mods []SelectedModifier) error {
	if len(mods) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO sale_line_modifiers (id, sale_line_id, group_id, option_id, group_name_snapshot, option_name_snapshot, price_delta_minor)
VALUES (?, ?, ?, ?, ?, ?, ?)
`)
	if err != nil {
		return fmt.Errorf("prepare insert sale_line_modifiers: %w", err)
	}
	defer stmt.Close()
	for _, m := range mods {
		if _, err := stmt.ExecContext(ctx, uuid.NewString(), lineID, nullableString(m.GroupID), nullableString(m.OptionID), m.GroupName, m.OptionName, m.PriceDeltaMinor); err != nil {
			return fmt.Errorf("insert sale_line_modifier: %w", err)
		}
	}
	return nil
}

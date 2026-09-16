package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// ModifierGroup is a customization choice attached to one or more catalog
// items (e.g. "Extras", "Bread") — ADR-0020 / spec 011, shareable across
// items since ADR-0090 (ut-docs#2013) via item_modifier_group_links. Options
// are loaded alongside it.
//
// ItemID and SortOrder come from the LINK row, not the group row: ItemID is
// the item this listing is for (a shared group appears once per linked item
// in the shop-wide queries, each with that item's own ItemName), and
// SortOrder is that item's own position for the group. The group row's own
// item_id column is only the legacy ADR-0090 §2 "anchor" and is never
// exposed here.
type ModifierGroup struct {
	ID        string
	ItemID    string
	Name      string
	Required  bool
	MinSelect int
	MaxSelect int
	SortOrder int
	IsActive  bool
	Options   []ModifierOption
	// ItemName is only populated by ListShopModifierGroups (ut-docs#1899) —
	// every other query already scopes to one caller-known item, so it
	// would just be redundant there. Left "" by every other query.
	ItemName string
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
SELECT g.id, l.item_id, g.name, g.required, g.min_select, g.max_select, l.sort_order, g.is_active
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
		if err := groupRows.Scan(&g.ID, &g.ItemID, &g.Name, &required, &g.MinSelect, &g.MaxSelect, &g.SortOrder, &active); err != nil {
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
// through a link table whose PRIMARY KEY includes group_id. It is NOT used
// by listShopModifierGroups, where the same group recurs once per linked
// item and needs its own fan-out. Callers guard the empty slice themselves
// (SQLite rejects "IN ()").
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
// ItemID/SortOrder — the more specific attachment wins for display
// position, ADR-0094 §3). An item with no category, or no surviving
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
// exist, inherits nothing: nil, nil. ItemID is left "" — an inherited group
// is category-attached, not item-scoped identity, and SortOrder is the
// CATEGORY link's own position.
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
// opt-outs on top of this set. ItemID is left "" on every result — a
// category link is not item-scoped identity.
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

// ListShopModifierGroups returns every ACTIVE modifier group belonging to an
// ACTIVE item in the shop, across every item, with active options nested —
// the query behind the /modifiers browse screen (ut-docs#1899). Every
// existing query here is scoped to one item because every existing caller
// already knows which item it's editing (the catalog admin panel) or
// selling (the sale-time picker); this is the first caller that needs to
// see the whole shop at once, so it also carries the item's name (ItemName)
// since nothing else identifies which item a row belongs to once groups
// from many items are mixed together. The i.is_active filter matters here
// specifically (independent review, ut-docs#1899): DeactivateItem never
// touches item_modifier_groups.is_active, and ListItems already filters
// deactivated items off /catalog — the only place a group can be edited or
// deactivated — so without this filter a deactivated item's groups would
// render on this screen forever, labelled with a name the merchant can no
// longer find or act on. Same convention as the repo's other item-joining
// browse queries (ListItems, ItemsWithoutBarcode, ListActiveVariants).
// Ordered by item name, then item id (a tie-break so two items sharing a
// name don't interleave their groups), then group sort_order/name, matching
// the per-item queries' own group ordering.
func (r *ModifierRepo) ListShopModifierGroups(ctx context.Context) ([]ModifierGroup, error) {
	return r.listShopModifierGroups(ctx, false)
}

// ListAllShopModifierGroups is the admin equivalent of ListShopModifierGroups
// — it returns EVERY modifier group and option shop-wide, active or not
// (ut-docs#1957): once /modifiers became the full CRUD home for modifier
// groups (moved out of the per-item catalog panel), the admin page needs to
// show a deactivated group/option too so a manager can reactivate it, same
// reason ListAllGroupsForItem exists beside the per-item ListGroupsForItem.
// The read-only browse behavior of ListShopModifierGroups above is
// unchanged — this is a separate method, not a widened filter on that one.
func (r *ModifierRepo) ListAllShopModifierGroups(ctx context.Context) ([]ModifierGroup, error) {
	return r.listShopModifierGroups(ctx, true)
}

// listShopModifierGroups is shared by both exported shop-wide queries above.
// i.is_active is ALWAYS required (independent review, ut-docs#1899): once an
// item is deactivated, ListItems already hides it from /catalog — the only
// place its groups could be reached or edited — so without this a
// deactivated item's groups would linger on a shop-wide screen forever,
// labelled with a name the merchant can no longer find or act on. That
// holds for the admin variant too, since a deactivated item's detail panel
// is unreachable there just the same. includeInactive only ever widens the
// GROUP/OPTION is_active filter, never the item one.
//
// Since ADR-0090 a group linked to N active items comes back as N rows (one
// per link, each with that item's ItemID/ItemName and per-link SortOrder),
// so the item filter and the ordering both go through the link row.
func (r *ModifierRepo) listShopModifierGroups(ctx context.Context, includeInactive bool) ([]ModifierGroup, error) {
	groupQuery := `
SELECT g.id, l.item_id, i.name, g.name, g.required, g.min_select, g.max_select, l.sort_order, g.is_active
FROM item_modifier_groups g
JOIN item_modifier_group_links l ON l.group_id = g.id
JOIN items i ON i.id = l.item_id
WHERE i.is_active = 1`
	if !includeInactive {
		groupQuery += ` AND g.is_active = 1`
	}
	groupQuery += ` ORDER BY i.name, l.item_id, l.sort_order, g.name`

	groupRows, err := r.db.QueryContext(ctx, groupQuery)
	if err != nil {
		return nil, fmt.Errorf("list shop modifier groups: %w", err)
	}
	defer groupRows.Close()

	var groups []ModifierGroup
	for groupRows.Next() {
		var g ModifierGroup
		var required, active int
		if err := groupRows.Scan(&g.ID, &g.ItemID, &g.ItemName, &g.Name, &required, &g.MinSelect, &g.MaxSelect, &g.SortOrder, &active); err != nil {
			return nil, fmt.Errorf("scan shop modifier group: %w", err)
		}
		g.Required = required == 1
		g.IsActive = active == 1
		groups = append(groups, g)
	}
	if err := groupRows.Err(); err != nil {
		return nil, fmt.Errorf("list shop modifier groups: %w", err)
	}
	if len(groups) == 0 {
		return nil, nil
	}
	// One group id can now map to SEVERAL rows (one per linked item). A
	// map[string]*ModifierGroup keyed by group id — the pre-ADR-0090 shape —
	// would silently keep only the last row per id and attach the options
	// to that one item's copy, leaving every other item's copy of the same
	// group with an empty option list. Index every row index per group id
	// and fan each option out to all of them. Pinned by
	// TestModifierRepo_ListShopModifierGroups_SharedGroupCarriesFullOptionsUnderEveryItem.
	byGroupID := map[string][]int{}
	for i := range groups {
		byGroupID[groups[i].ID] = append(byGroupID[groups[i].ID], i)
	}

	// Options for every group that has at least one link to an active item
	// (the same set the group query above returned).
	optQuery := `
SELECT o.id, o.group_id, o.name, o.price_delta_minor, o.sort_order, o.is_active
FROM item_modifier_options o
JOIN item_modifier_groups g ON g.id = o.group_id
WHERE EXISTS (
    SELECT 1 FROM item_modifier_group_links l
    JOIN items i ON i.id = l.item_id
    WHERE l.group_id = g.id AND i.is_active = 1
)`
	if !includeInactive {
		optQuery += ` AND g.is_active = 1 AND o.is_active = 1`
	}
	optQuery += ` ORDER BY o.sort_order, o.name`

	optRows, err := r.db.QueryContext(ctx, optQuery)
	if err != nil {
		return nil, fmt.Errorf("list shop modifier options: %w", err)
	}
	defer optRows.Close()
	for optRows.Next() {
		var o ModifierOption
		var active int
		if err := optRows.Scan(&o.ID, &o.GroupID, &o.Name, &o.PriceDeltaMinor, &o.SortOrder, &active); err != nil {
			return nil, fmt.Errorf("scan shop modifier option: %w", err)
		}
		o.IsActive = active == 1
		for _, idx := range byGroupID[o.GroupID] {
			groups[idx].Options = append(groups[idx].Options, o)
		}
	}
	if err := optRows.Err(); err != nil {
		return nil, fmt.Errorf("list shop modifier options: %w", err)
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

// CreateGroup adds a modifier group to an item. Returns the new group id.
//
// Writes the group row AND its first item_modifier_group_links row in one
// transaction (ADR-0090). item_modifier_groups.item_id is still populated
// with itemID: it is NOT NULL and remains the legacy "anchor" (the item the
// group was first created against) — see ReanchorGroupsBeforeItemDelete for
// how it is kept from dangling. The link row is what every read path
// consults for membership.
func (r *ModifierRepo) CreateGroup(ctx context.Context, id string, itemID, name string, required bool, minSelect, maxSelect, sortOrder int) (string, error) {
	if itemID == "" {
		return "", errors.New("item_id required")
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
		return "", fmt.Errorf("begin create modifier group: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO item_modifier_groups (id, item_id, name, required, min_select, max_select, sort_order)
VALUES (?, ?, ?, ?, ?, ?, ?)
`, id, itemID, name, req, minSelect, maxSelect, sortOrder); err != nil {
		return "", fmt.Errorf("insert modifier group: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO item_modifier_group_links (item_id, group_id, sort_order)
VALUES (?, ?, ?)
`, itemID, id, sortOrder); err != nil {
		return "", fmt.Errorf("insert modifier group link: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit create modifier group: %w", err)
	}
	return id, nil
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

// UnlinkGroupFromItem detaches a modifier group from one item
// unconditionally, including its last remaining link — it never deletes the
// group ROW itself (DeleteGroup, unwired to any handler, is the separate,
// explicit full-delete action), but an orphaned (zero-link) group IS
// unreachable everywhere in the UI: every list query (ListShopModifierGroups/
// ListAllShopModifierGroups, and so /modifiers and the item-scoped panel)
// only ever surfaces a group THROUGH a link row. POST
// /api/catalog/modifier-group/detach (ut-docs#2046) never calls this
// directly for that reason — see UnlinkGroupFromItemUnlessLastLink below,
// which is the handler's actual guard against reaching that state. This
// method remains the direct, unconditional primitive (used by internal
// data-repair paths and its own regression test), not itself a UI action.
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

// UnlinkGroupFromItemUnlessLastLink detaches a modifier group from one item
// UNLESS this is the group's only remaining link, in which case it does
// nothing and returns false — the handler-level guard behind POST
// /api/catalog/modifier-group/detach (ut-docs#2046) that keeps a group from
// ever losing its last link through the UI (see UnlinkGroupFromItem's own
// doc comment on why a zero-link group is a real problem, not a cosmetic
// one). One atomic conditional DELETE, not a separate GroupLinkCount call
// followed by UnlinkGroupFromItem (independent review): a count-then-delete
// has a TOCTOU race — two concurrent detaches against the same group's two
// different items could both observe count==2, both pass, and both delete,
// orphaning the group anyway. The subquery here is evaluated as part of the
// same statement SQLite executes under its writer lock, so a second
// concurrent call against the same group_id is serialized behind the
// first's effect rather than racing it.
func (r *ModifierRepo) UnlinkGroupFromItemUnlessLastLink(ctx context.Context, itemID, groupID string) (bool, error) {
	if itemID == "" {
		return false, errors.New("item_id required")
	}
	if groupID == "" {
		return false, errors.New("group_id required")
	}
	res, err := r.db.ExecContext(ctx, `
DELETE FROM item_modifier_group_links
WHERE item_id = ? AND group_id = ?
  AND (SELECT COUNT(*) FROM item_modifier_group_links WHERE group_id = ?) > 1
`, itemID, groupID, groupID)
	if err != nil {
		return false, fmt.Errorf("unlink modifier group from item unless last link: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("unlink modifier group from item unless last link: %w", err)
	}
	return n > 0, nil
}

// LinkGroupToCategory attaches an existing modifier group to a category, so
// every item in that category inherits it at sale time (ResolveGroupsForItem)
// unless the item opts out — ADR-0094 (ut-docs#1915), the category-side
// twin of LinkGroupToItem, to be wired up by #2284's category editor. Same
// ON CONFLICT DO UPDATE shape: re-linking an already-linked group just
// updates its per-category sort order, so a resubmit settles safely. A
// category never becomes a group's owner through this (ADR-0094 Decision
// 1): the group's item anchor and its item_modifier_group_links rows are
// untouched, and UnlinkGroupFromItemUnlessLastLink's "at least one ITEM
// link" invariant still counts item links only.
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
// ADR-0094 (ut-docs#1915). A plain unconditional DELETE, deliberately
// without UnlinkGroupFromItemUnlessLastLink's "last link" guard: a category
// is never a group's sole owner (ADR-0094 Decision 1 — the group keeps its
// item anchor and item links regardless), so removing a category link can
// never orphan the group or make it unreachable in the UI. Existing opt-out
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

// OptOutItemFromGroup records that an item declines a category-inherited
// modifier group — ADR-0094 Decision 2 (ut-docs#1915): a presence-only row
// in item_modifier_group_opt_outs, which ResolveGroupsForItem subtracts from
// the item's inherited set and ListInheritedGroupsForItem reports as
// OptedOut. INSERT OR IGNORE, so opting out twice is a no-op rather than a
// PK violation. This only ever suppresses a CATEGORY-inherited group: a
// group the item is DIRECTLY linked to via item_modifier_group_links is
// unaffected by an opt-out row — detaching that is
// UnlinkGroupFromItemUnlessLastLink's job, keeping the two mechanisms
// non-overlapping (direct links are added/removed; inheritance is
// accepted/opted-out). No FK or existence check beyond the row's own
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

// GroupLinkCount reports how many items a modifier group is currently linked
// to. Used to block detaching a group's last remaining link (ut-docs#2046):
// UnlinkGroupFromItem never deletes the group row itself, but a zero-link
// group is invisible everywhere in the UI — ListShopModifierGroups/
// ListAllShopModifierGroups (and so /modifiers and the item-scoped panel)
// only ever return a group THROUGH one of its links, and no hard-delete UI
// is wired up either — so losing its last link would make it permanently
// unreachable rather than merely "unattached from this item."
func (r *ModifierRepo) GroupLinkCount(ctx context.Context, groupID string) (int, error) {
	if groupID == "" {
		return 0, errors.New("group_id required")
	}
	var n int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM item_modifier_group_links WHERE group_id = ?`, groupID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count modifier group links: %w", err)
	}
	return n, nil
}

// ReanchorGroupsBeforeItemDelete re-points item_modifier_groups.item_id
// (ADR-0090's legacy "anchor" column) off itemID, onto another item still
// linked to the same group via item_modifier_group_links, before itemID is
// hard-deleted — so the anchor column's own ON DELETE CASCADE does not
// destroy a group still shared with a surviving item. A group with no
// surviving link is left anchored to itemID and correctly cascades away
// with it: it is genuinely orphaned, not shared, so today's behaviour (no
// data loss for a real single-item group) is unchanged. Call this inside
// the SAME transaction as the item delete, immediately before it — it only
// ever touches tx, never the receiver's own connection.
func (r *ModifierRepo) ReanchorGroupsBeforeItemDelete(ctx context.Context, tx *sql.Tx, itemID string) error {
	_, err := tx.ExecContext(ctx, `
UPDATE item_modifier_groups
SET item_id = (
    SELECT l.item_id FROM item_modifier_group_links l
    WHERE l.group_id = item_modifier_groups.id AND l.item_id != ?
    LIMIT 1
)
WHERE item_id = ?
  AND EXISTS (
    SELECT 1 FROM item_modifier_group_links l
    WHERE l.group_id = item_modifier_groups.id AND l.item_id != ?
  )
`, itemID, itemID, itemID)
	if err != nil {
		return fmt.Errorf("reanchor modifier groups before item delete: %w", err)
	}
	return nil
}

// ReanchorGroupsBeforeBulkItemDelete is the batch form for a caller that
// deletes items matching a WHERE predicate rather than one known id (e.g.
// POSRepo.CleanupObsoleteItems' obsoleteItemsWhere). itemIDSubquery must be
// a complete, parameter-free "SELECT id FROM items WHERE ..." SQL string
// selecting exactly the item ids about to be deleted — pass the SAME
// subquery text the caller's own DELETE FROM items uses (see pos_repo.go's
// own itemSet for the identical string-composition pattern), so the two can
// never silently drift apart. No caller-supplied user input reaches this
// string in this codebase — it is always a compile-time constant predicate,
// same as itemSet. Same same-transaction contract as
// ReanchorGroupsBeforeItemDelete.
func (r *ModifierRepo) ReanchorGroupsBeforeBulkItemDelete(ctx context.Context, tx *sql.Tx, itemIDSubquery string) error {
	_, err := tx.ExecContext(ctx, `
UPDATE item_modifier_groups
SET item_id = (
    SELECT l.item_id FROM item_modifier_group_links l
    WHERE l.group_id = item_modifier_groups.id
      AND l.item_id NOT IN (`+itemIDSubquery+`)
    LIMIT 1
)
WHERE item_id IN (`+itemIDSubquery+`)
  AND EXISTS (
    SELECT 1 FROM item_modifier_group_links l
    WHERE l.group_id = item_modifier_groups.id
      AND l.item_id NOT IN (`+itemIDSubquery+`)
  )
`)
	if err != nil {
		return fmt.Errorf("reanchor modifier groups before bulk item delete: %w", err)
	}
	return nil
}

// UpdateGroup edits an existing modifier group's fields.
//
// sortOrder here writes ONLY item_modifier_groups.sort_order — the legacy
// ADR-0090 §2 anchor column, which no read path consults any more
// (listGroupsForItem/listShopModifierGroups both read the LINK row's own
// sort_order). No template in web/ui submits a sortOrder field to this
// call today, so this is currently a harmless no-op in practice, not a
// live bug — but the deferred "attach an existing group" UI card (ADR-0090
// §5) will want PER-ITEM ordering, which is what LinkGroupToItem's
// ON CONFLICT DO UPDATE SET sort_order already provides. Whoever builds
// that UI should route ordering changes through LinkGroupToItem, not
// through this parameter.
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

// DeleteGroup removes a group and its options (ON DELETE CASCADE) — and,
// since ADR-0090, every item_modifier_group_links row for it too, i.e.
// EVERY item currently using it, not just one. Neither this nor DeleteOption
// is wired to any handler today (sync_admin_repo.go's own comment already
// notes this) — whoever wires one up next needs UnlinkGroupFromItem as the
// separate, narrower "detach from just this item" action, and should
// reserve this one for "remove this group everywhere, deliberately."
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

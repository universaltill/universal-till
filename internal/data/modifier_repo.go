package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// ModifierGroup is a customization choice attached to a catalog item (e.g.
// "Extras", "Bread") — ADR-0020 / spec 011. Options are loaded alongside it.
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
	groupRows, err := r.db.QueryContext(ctx, `
SELECT id, item_id, name, required, min_select, max_select, sort_order, is_active
FROM item_modifier_groups
WHERE item_id = ? AND is_active = 1
ORDER BY sort_order, name
`, itemID)
	if err != nil {
		return nil, fmt.Errorf("list modifier groups: %w", err)
	}
	defer groupRows.Close()

	var groups []ModifierGroup
	byID := map[string]*ModifierGroup{}
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
	for i := range groups {
		byID[groups[i].ID] = &groups[i]
	}
	if len(groups) == 0 {
		return nil, nil
	}

	optRows, err := r.db.QueryContext(ctx, `
SELECT o.id, o.group_id, o.name, o.price_delta_minor, o.sort_order, o.is_active
FROM item_modifier_options o
JOIN item_modifier_groups g ON g.id = o.group_id
WHERE g.item_id = ? AND g.is_active = 1 AND o.is_active = 1
ORDER BY o.sort_order, o.name
`, itemID)
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
		if g, ok := byID[o.GroupID]; ok {
			g.Options = append(g.Options, o)
		}
	}
	if err := optRows.Err(); err != nil {
		return nil, fmt.Errorf("list modifier options: %w", err)
	}
	return groups, nil
}

// CreateGroup adds a modifier group to an item. Returns the new group id.
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
	_, err := r.db.ExecContext(ctx, `
INSERT INTO item_modifier_groups (id, item_id, name, required, min_select, max_select, sort_order)
VALUES (?, ?, ?, ?, ?, ?, ?)
`, id, itemID, name, req, minSelect, maxSelect, sortOrder)
	if err != nil {
		return "", fmt.Errorf("insert modifier group: %w", err)
	}
	return id, nil
}

// UpdateGroup edits an existing modifier group's fields.
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

// DeleteGroup removes a group and its options (ON DELETE CASCADE).
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

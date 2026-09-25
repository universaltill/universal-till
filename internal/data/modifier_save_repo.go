package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Bounds for save_modifier_group (contract §2.11 / §3.4), the same the
// local admin creator and upsert_modifier_group use — but invalid values
// are REJECTED here, never coerced.
const (
	MaxModifierSelect        = 50
	maxGroupOptions          = 50
	maxOptionNameRunes       = 128
	maxOptionPriceDeltaMinor = 999_999_999
	maxGroupAttachIDs        = 200
)

// ModifierGroupSave is a save_modifier_group directive (contract §3.4):
// nil = keep. Options, when non-nil, is the FULL ordered set (an option
// left out is deleted; sort_order = index). The attach/detach lists are
// deltas.
type ModifierGroupSave struct {
	ID                string
	Create            bool
	Name              *string
	Required          *bool
	MinSelect         *int
	MaxSelect         *int
	Options           *[]ModifierOption
	AttachCategoryIDs []string
	DetachCategoryIDs []string
	AttachItemIDs     []string
	DetachItemIDs     []string
}

// ModifierGroupSaveResult reports what SaveGroup did.
type ModifierGroupSaveResult struct {
	Created bool
	Name    string
	Changed []string
}

// SaveGroup applies a save_modifier_group directive in one transaction:
// create-with-id or update of the group's name and selection rules, the
// options' full ordered set (known ids updated, unknown ids created with
// that id, unlisted ones deleted — sale_line_modifiers has no FK onto
// options, so past sales keep theirs), and category/item attach/detach.
// Idempotent: re-applying the same patch leaves the same state.
func (r *ModifierRepo) SaveGroup(ctx context.Context, p ModifierGroupSave) (ModifierGroupSaveResult, error) {
	var res ModifierGroupSaveResult
	p.ID = strings.TrimSpace(p.ID)
	if p.ID == "" {
		return res, errors.New("missing id")
	}
	if p.Name != nil {
		n := strings.TrimSpace(*p.Name)
		if n == "" {
			return res, errors.New("the modifier group name must not be blank")
		}
		p.Name = &n
	}
	if p.Options != nil {
		if err := validateOptions(*p.Options); err != nil {
			return res, err
		}
	}
	for _, pair := range [][2][]string{{p.AttachCategoryIDs, p.DetachCategoryIDs}, {p.AttachItemIDs, p.DetachItemIDs}} {
		if len(pair[0]) > maxGroupAttachIDs || len(pair[1]) > maxGroupAttachIDs {
			return res, fmt.Errorf("at most %d ids per attach or detach list", maxGroupAttachIDs)
		}
		detach := map[string]bool{}
		for _, id := range pair[1] {
			detach[strings.TrimSpace(id)] = true
		}
		for _, id := range pair[0] {
			if detach[strings.TrimSpace(id)] {
				return res, fmt.Errorf("%s is both attached and detached", strings.TrimSpace(id))
			}
		}
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return res, fmt.Errorf("save modifier group: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var name string
	var required, active bool
	var minSel, maxSel int
	err = tx.QueryRowContext(ctx, `SELECT name, required, min_select, max_select, is_active FROM item_modifier_groups WHERE id = ?`, p.ID).
		Scan(&name, &required, &minSel, &maxSel, &active)
	exists := true
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if !p.Create {
			return res, ErrModifierGroupNotFound
		}
		if p.Name == nil {
			return res, errors.New("a new modifier group needs a name")
		}
		exists, active, minSel, maxSel = false, true, 0, 1
	case err != nil:
		return res, fmt.Errorf("save modifier group: load: %w", err)
	}
	if p.Name != nil {
		if active {
			var other string
			err := tx.QueryRowContext(ctx, `SELECT name FROM item_modifier_groups WHERE is_active = 1 AND id <> ? AND lower(name) = lower(?) LIMIT 1`, p.ID, *p.Name).Scan(&other)
			if err == nil {
				return res, fmt.Errorf("a modifier group named %s already exists", *p.Name)
			} else if !errors.Is(err, sql.ErrNoRows) {
				return res, fmt.Errorf("save modifier group: name check: %w", err)
			}
		}
		name = *p.Name
		res.Changed = append(res.Changed, "name")
	}
	if p.Required != nil {
		required = *p.Required
		res.Changed = append(res.Changed, "required")
	}
	if p.MinSelect != nil {
		minSel = *p.MinSelect
		res.Changed = append(res.Changed, "min_select")
	}
	if p.MaxSelect != nil {
		maxSel = *p.MaxSelect
		res.Changed = append(res.Changed, "max_select")
	}
	if minSel < 0 || maxSel < 1 || minSel > maxSel || maxSel > MaxModifierSelect {
		return res, fmt.Errorf("choose at least %d and at most %d is not allowed: 0 ≤ at least ≤ at most ≤ %d, and at most ≥ 1", minSel, maxSel, MaxModifierSelect)
	}
	if required && minSel < 1 {
		return res, errors.New("a required modifier group must ask for at least 1 choice")
	}
	if exists {
		if _, err := tx.ExecContext(ctx, `UPDATE item_modifier_groups SET name = ?, required = ?, min_select = ?, max_select = ? WHERE id = ?`,
			name, boolToInt(required), minSel, maxSel, p.ID); err != nil {
			return res, fmt.Errorf("save modifier group: update: %w", err)
		}
	} else {
		if _, err := tx.ExecContext(ctx, `INSERT INTO item_modifier_groups (id, name, required, min_select, max_select, sort_order) VALUES (?, ?, ?, ?, ?, 0)`,
			p.ID, name, boolToInt(required), minSel, maxSel); err != nil {
			return res, fmt.Errorf("save modifier group: insert: %w", err)
		}
		res.Created = true
	}

	if p.Options != nil {
		if err := replaceOptionsTx(ctx, tx, p.ID, *p.Options); err != nil {
			return res, err
		}
		res.Changed = append(res.Changed, "options")
	}
	for _, id := range dedupeIDs(p.AttachCategoryIDs) {
		if err := requireRowTx(ctx, tx, `SELECT 1 FROM categories WHERE id = ? AND is_active = 1`, id, "category %s does not exist on this till or is deleted"); err != nil {
			return res, err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO category_modifier_group_links (category_id, group_id, sort_order)
SELECT ?, ?, COALESCE(MAX(sort_order) + 1, 0) FROM category_modifier_group_links WHERE category_id = ?
ON CONFLICT(category_id, group_id) DO NOTHING`, id, p.ID, id); err != nil {
			return res, fmt.Errorf("save modifier group: attach category: %w", err)
		}
	}
	for _, id := range dedupeIDs(p.DetachCategoryIDs) {
		if err := requireRowTx(ctx, tx, `SELECT 1 FROM categories WHERE id = ?`, id, "category %s does not exist on this till"); err != nil {
			return res, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM category_modifier_group_links WHERE category_id = ? AND group_id = ?`, id, p.ID); err != nil {
			return res, fmt.Errorf("save modifier group: detach category: %w", err)
		}
	}
	for _, id := range dedupeIDs(p.AttachItemIDs) {
		if err := requireRowTx(ctx, tx, `SELECT 1 FROM items WHERE id = ?`, id, "item %s does not exist on this till"); err != nil {
			return res, err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO item_modifier_group_links (item_id, group_id, sort_order)
SELECT ?, ?, COALESCE(MAX(sort_order) + 1, 0) FROM item_modifier_group_links WHERE item_id = ?
ON CONFLICT(item_id, group_id) DO NOTHING`, id, p.ID, id); err != nil {
			return res, fmt.Errorf("save modifier group: attach item: %w", err)
		}
	}
	for _, id := range dedupeIDs(p.DetachItemIDs) {
		if err := requireRowTx(ctx, tx, `SELECT 1 FROM items WHERE id = ?`, id, "item %s does not exist on this till"); err != nil {
			return res, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM item_modifier_group_links WHERE item_id = ? AND group_id = ?`, id, p.ID); err != nil {
			return res, fmt.Errorf("save modifier group: detach item: %w", err)
		}
	}
	if len(p.AttachCategoryIDs)+len(p.DetachCategoryIDs) > 0 {
		res.Changed = append(res.Changed, "categories")
	}
	if len(p.AttachItemIDs)+len(p.DetachItemIDs) > 0 {
		res.Changed = append(res.Changed, "items")
	}
	if err := tx.Commit(); err != nil {
		return res, fmt.Errorf("save modifier group: commit: %w", err)
	}
	res.Name = name
	return res, nil
}

// validateOptions checks a full option set before any write.
func validateOptions(opts []ModifierOption) error {
	if len(opts) > maxGroupOptions {
		return fmt.Errorf("a modifier group can have at most %d options", maxGroupOptions)
	}
	seen := map[string]bool{}
	for _, o := range opts {
		id, name := strings.TrimSpace(o.ID), strings.TrimSpace(o.Name)
		switch {
		case id == "":
			return errors.New("every option needs an id")
		case seen[id]:
			return fmt.Errorf("option id %s is listed twice", id)
		case name == "" || runeLen(name) > maxOptionNameRunes:
			return fmt.Errorf("option name %q must be 1–%d characters", name, maxOptionNameRunes)
		case o.PriceDeltaMinor < 0 || o.PriceDeltaMinor > maxOptionPriceDeltaMinor:
			return fmt.Errorf("option %s: the price change must be between 0 and %d", name, maxOptionPriceDeltaMinor)
		}
		seen[id] = true
	}
	return nil
}

// replaceOptionsTx makes opts the group's full ordered option set.
func replaceOptionsTx(ctx context.Context, tx *sql.Tx, groupID string, opts []ModifierOption) error {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM item_modifier_options WHERE group_id = ?`, groupID)
	if err != nil {
		return fmt.Errorf("save modifier group: read options: %w", err)
	}
	stored := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("save modifier group: read options: %w", err)
		}
		stored[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("save modifier group: read options: %w", err)
	}
	listed := map[string]bool{}
	for i, o := range opts {
		id, name := strings.TrimSpace(o.ID), strings.TrimSpace(o.Name)
		listed[id] = true
		if stored[id] {
			if _, err := tx.ExecContext(ctx, `UPDATE item_modifier_options SET name = ?, price_delta_minor = ?, sort_order = ?, is_active = ? WHERE id = ?`,
				name, o.PriceDeltaMinor, i, boolToInt(o.IsActive), id); err != nil {
				return fmt.Errorf("save modifier group: update option: %w", err)
			}
			continue
		}
		// An id that exists under ANOTHER group must never be moved here.
		if taken, err := rowExists(ctx, tx, `SELECT 1 FROM item_modifier_options WHERE id = ?`, id); err != nil {
			return fmt.Errorf("save modifier group: check option: %w", err)
		} else if taken {
			return fmt.Errorf("option id %s belongs to another modifier group", id)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO item_modifier_options (id, group_id, name, price_delta_minor, sort_order, is_active) VALUES (?, ?, ?, ?, ?, ?)`,
			id, groupID, name, o.PriceDeltaMinor, i, boolToInt(o.IsActive)); err != nil {
			return fmt.Errorf("save modifier group: insert option: %w", err)
		}
	}
	for id := range stored {
		if !listed[id] {
			if _, err := tx.ExecContext(ctx, `DELETE FROM item_modifier_options WHERE id = ?`, id); err != nil {
				return fmt.Errorf("save modifier group: delete option: %w", err)
			}
		}
	}
	return nil
}

// requireRowTx fails with msg (formatted with arg) unless query finds a row.
func requireRowTx(ctx context.Context, tx *sql.Tx, query, arg, msg string) error {
	ok, err := rowExists(ctx, tx, query, arg)
	if err != nil {
		return fmt.Errorf("save modifier group: %w", err)
	}
	if !ok {
		return fmt.Errorf(msg, arg)
	}
	return nil
}

// DeleteGroupIfExists is DeleteGroup (the same cascade to options, links
// and opt-outs; past sales unaffected) reporting whether a group was there
// to delete — delete_modifier_group's "already deleted" replay answer.
func (r *ModifierRepo) DeleteGroupIfExists(ctx context.Context, id string) (bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return false, errors.New("id required")
	}
	res, err := r.db.ExecContext(ctx, `DELETE FROM item_modifier_groups WHERE id = ?`, id)
	if err != nil {
		return false, fmt.Errorf("delete modifier group: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("delete modifier group: %w", err)
	}
	return n > 0, nil
}

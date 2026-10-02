package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// Option-set manage parity (ut-docs#3319): rename, the values' full ordered
// replace, the active flag, delete-when-unused and the used-by read. One
// write path — SaveOptionSet — serves both the till's own /catalog/option-sets
// screen (through the thin RenameOptionSet / ReplaceOptionSetValues /
// SetOptionSetActive wrappers) and the save_option_set cloud directive, the
// same double duty ModifierRepo.SaveGroup does for modifier groups.

// Bounds for an option set's values: the same numbers the modifier-group
// options payload uses (maxGroupOptions / maxOptionNameRunes, and ut-cloud's
// maxModifierGroupOptions / maxModifierGroupOptionNameLen), so my. and the
// till agree on one limit rather than inventing a second.
const (
	MaxOptionSetValues     = maxGroupOptions
	MaxOptionSetValueRunes = maxOptionNameRunes
)

// ErrOptionSetNotFound reports that an option-set id is not on this till.
var ErrOptionSetNotFound = errors.New("option set not found")

// ErrOptionSetValueInvalid / ErrOptionSetTooManyValues name the two bound
// violations of a values replace, so the till screen can say which one
// (a blank or over-long value, or more than MaxOptionSetValues).
var (
	ErrOptionSetValueInvalid  = errors.New("option set value must not be blank or too long")
	ErrOptionSetTooManyValues = errors.New("too many values in one option set")
)

// ErrOptionSetInUse reports that DeleteOptionSetIfUnused refused: at least
// one active item has the set applied (item_option_sets). The schema would cascade
// those links away silently, so the refusal is explicit; callers name the
// items with ItemsUsingOptionSet.
var ErrOptionSetInUse = errors.New("option set is applied to at least one item")

// OptionSetValueInput is one value in a full ordered replace: a known id is
// updated (keeping the id, so generated variants keep their
// item_variant_options link), an unknown id is created with that id.
type OptionSetValueInput struct {
	ID    string
	Value string
}

// OptionSetSave is a save_option_set patch: nil = keep. Values, when non-nil,
// is the FULL ordered set (a value left out is deleted; sort_order = index).
type OptionSetSave struct {
	ID     string
	Create bool
	Name   *string
	Active *bool
	Values *[]OptionSetValueInput
}

// OptionSetSaveResult reports what SaveOptionSet did.
type OptionSetSaveResult struct {
	Created bool
	Name    string
	Changed []string
}

// OptionSetAdmin is one option set plus the items it is applied to — the
// read behind the shop-wide screen's "used by" list and the cloud report.
type OptionSetAdmin struct {
	OptionSetView
	Items []AssignedItem
}

// SaveOptionSet applies a patch in one transaction: create-with-id or update
// of the name (option_sets.name UNIQUE → ErrOptionSetExists, the same rule as
// CreateOptionSet) and active flag, then the values' full ordered replace.
// Anything refused writes nothing. Idempotent: re-applying the same patch
// leaves the same state.
func (r *OptionSetRepo) SaveOptionSet(ctx context.Context, p OptionSetSave) (OptionSetSaveResult, error) {
	var res OptionSetSaveResult
	p.ID = strings.TrimSpace(p.ID)
	if p.ID == "" {
		return res, errors.New("missing id")
	}
	if p.Name != nil {
		n := strings.TrimSpace(*p.Name)
		if n == "" {
			return res, errors.New("the option set name must not be blank")
		}
		p.Name = &n
	}
	var values []OptionSetValueInput
	if p.Values != nil {
		var err error
		if values, err = normalizeOptionSetValues(*p.Values); err != nil {
			return res, err
		}
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return res, fmt.Errorf("save option set: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var name string
	var active int
	exists := true
	err = tx.QueryRowContext(ctx, `SELECT name, is_active FROM option_sets WHERE id = ?`, p.ID).Scan(&name, &active)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if !p.Create {
			return res, ErrOptionSetNotFound
		}
		if p.Name == nil {
			return res, errors.New("a new option set needs a name")
		}
		exists, active = false, 1
	case err != nil:
		return res, fmt.Errorf("save option set: load: %w", err)
	}
	if p.Name != nil {
		if taken, err := rowExistsArgs(ctx, tx, `SELECT 1 FROM option_sets WHERE name = ? AND id <> ?`, *p.Name, p.ID); err != nil {
			return res, fmt.Errorf("save option set: name check: %w", err)
		} else if taken {
			return res, ErrOptionSetExists
		}
		name = *p.Name
		res.Changed = append(res.Changed, "name")
	}
	if p.Active != nil {
		active = boolToInt(*p.Active)
		res.Changed = append(res.Changed, "active")
	}
	if exists {
		_, err = tx.ExecContext(ctx, `UPDATE option_sets SET name = ?, is_active = ? WHERE id = ?`, name, active, p.ID)
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO option_sets (id, name, is_active) VALUES (?, ?, ?)`, p.ID, name, active)
		res.Created = err == nil
	}
	if err != nil {
		if isUniqueViolation(err) {
			return res, ErrOptionSetExists
		}
		return res, fmt.Errorf("save option set: write: %w", err)
	}
	if p.Values != nil {
		if err := replaceOptionSetValuesTx(ctx, tx, p.ID, values); err != nil {
			return res, err
		}
		res.Changed = append(res.Changed, "values")
	}
	if err := tx.Commit(); err != nil {
		return res, fmt.Errorf("save option set: commit: %w", err)
	}
	res.Name = name
	return res, nil
}

// RenameOptionSet renames a set. A name another set already has is
// ErrOptionSetExists; an unknown id is ErrOptionSetNotFound.
func (r *OptionSetRepo) RenameOptionSet(ctx context.Context, id, name string) error {
	_, err := r.SaveOptionSet(ctx, OptionSetSave{ID: id, Name: &name})
	return err
}

// ReplaceOptionSetValues makes values the set's full ordered value list in
// one call — add, edit, reorder and remove (see replaceOptionSetValuesTx).
func (r *OptionSetRepo) ReplaceOptionSetValues(ctx context.Context, optionSetID string, values []OptionSetValueInput) error {
	if values == nil {
		values = []OptionSetValueInput{}
	}
	_, err := r.SaveOptionSet(ctx, OptionSetSave{ID: optionSetID, Values: &values})
	return err
}

// SetOptionSetActive sets a set's active flag. An inactive set stays listed
// on the admin screen but is not offered on the item panel.
func (r *OptionSetRepo) SetOptionSetActive(ctx context.Context, id string, active bool) error {
	_, err := r.SaveOptionSet(ctx, OptionSetSave{ID: id, Active: &active})
	return err
}

// normalizeOptionSetValues trims and checks a full value list before any
// write: every value needs a unique id and 1–MaxOptionSetValueRunes
// characters of text, at most MaxOptionSetValues of them, and no text twice
// (UNIQUE (option_set_id, value) — ErrOptionSetValueExists).
func normalizeOptionSetValues(in []OptionSetValueInput) ([]OptionSetValueInput, error) {
	if len(in) > MaxOptionSetValues {
		return nil, fmt.Errorf("%w: an option set can have at most %d values", ErrOptionSetTooManyValues, MaxOptionSetValues)
	}
	out := make([]OptionSetValueInput, 0, len(in))
	ids, texts := map[string]bool{}, map[string]bool{}
	for _, v := range in {
		id, text := strings.TrimSpace(v.ID), strings.TrimSpace(v.Value)
		switch {
		case id == "":
			return nil, errors.New("every option set value needs an id")
		case ids[id]:
			return nil, fmt.Errorf("option set value id %s is listed twice", id)
		case text == "" || runeLen(text) > MaxOptionSetValueRunes:
			return nil, fmt.Errorf("%w: option set value %q must be 1–%d characters", ErrOptionSetValueInvalid, text, MaxOptionSetValueRunes)
		case strings.ContainsFunc(text, unicode.IsControl):
			// Review finding (ut-docs#3319): replaceOptionSetValuesTx parks
			// each kept row on a NUL-prefixed placeholder text to avoid a
			// transient UNIQUE collision while swapping two values. A value
			// containing a control character (NUL included) could otherwise
			// collide with that placeholder instead of a real value.
			return nil, fmt.Errorf("%w: option set value %q must not contain control characters", ErrOptionSetValueInvalid, text)
		case texts[text]:
			return nil, fmt.Errorf("%w: %s", ErrOptionSetValueExists, text)
		}
		ids[id], texts[text] = true, true
		out = append(out, OptionSetValueInput{ID: id, Value: text})
	}
	return out, nil
}

// replaceOptionSetValuesTx makes values (already normalized) the set's full
// ordered value list — replaceOptionsTx's shape: known ids updated (text +
// sort_order = index), unknown ids inserted with that id, unlisted ids
// deleted. Deleting a value cascades its item_variant_options links; the
// generated variants themselves stay (they are ordinary item_variants rows).
//
// UNIQUE (option_set_id, value) is checked per statement, so a swap ("S"↔"M")
// done row by row would collide midway: unlisted rows go first, then every
// kept row is parked on a placeholder text (NUL + its own id, unique and
// never a valid trimmed value) before the final texts are written.
func replaceOptionSetValuesTx(ctx context.Context, tx *sql.Tx, setID string, values []OptionSetValueInput) error {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM option_set_values WHERE option_set_id = ?`, setID)
	if err != nil {
		return fmt.Errorf("save option set: read values: %w", err)
	}
	stored := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("save option set: read values: %w", err)
		}
		stored[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("save option set: read values: %w", err)
	}
	listed := make(map[string]bool, len(values))
	for _, v := range values {
		listed[v.ID] = true
	}
	for id := range stored {
		if !listed[id] {
			if _, err := tx.ExecContext(ctx, `DELETE FROM option_set_values WHERE id = ?`, id); err != nil {
				return fmt.Errorf("save option set: delete value: %w", err)
			}
		}
	}
	for _, v := range values {
		if stored[v.ID] {
			if _, err := tx.ExecContext(ctx, `UPDATE option_set_values SET value = ? WHERE id = ?`, "\x00"+v.ID, v.ID); err != nil {
				return fmt.Errorf("save option set: park value: %w", err)
			}
		}
	}
	for i, v := range values {
		if stored[v.ID] {
			if _, err := tx.ExecContext(ctx, `UPDATE option_set_values SET value = ?, sort_order = ? WHERE id = ?`, v.Value, i, v.ID); err != nil {
				if isUniqueViolation(err) {
					return fmt.Errorf("%w: %s", ErrOptionSetValueExists, v.Value)
				}
				return fmt.Errorf("save option set: update value: %w", err)
			}
			continue
		}
		// An id that exists under ANOTHER set must never be moved here.
		if taken, err := rowExists(ctx, tx, `SELECT 1 FROM option_set_values WHERE id = ?`, v.ID); err != nil {
			return fmt.Errorf("save option set: check value: %w", err)
		} else if taken {
			return fmt.Errorf("option set value id %s belongs to another option set", v.ID)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO option_set_values (id, option_set_id, value, sort_order) VALUES (?, ?, ?, ?)`,
			v.ID, setID, v.Value, i); err != nil {
			if isUniqueViolation(err) {
				return fmt.Errorf("%w: %s", ErrOptionSetValueExists, v.Value)
			}
			return fmt.Errorf("save option set: insert value: %w", err)
		}
	}
	return nil
}

// rowExistsArgs is rowExists for a query with more than one argument.
func rowExistsArgs(ctx context.Context, tx *sql.Tx, query string, args ...any) (bool, error) {
	var one int
	err := tx.QueryRowContext(ctx, query, args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// GetOptionSet returns one set with its values in sort order, or
// ErrOptionSetNotFound.
func (r *OptionSetRepo) GetOptionSet(ctx context.Context, id string) (OptionSetView, error) {
	var s OptionSetView
	var active int
	err := r.db.QueryRowContext(ctx, `SELECT id, name, is_active FROM option_sets WHERE id = ?`, strings.TrimSpace(id)).Scan(&s.ID, &s.Name, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return s, ErrOptionSetNotFound
	}
	if err != nil {
		return s, fmt.Errorf("get option set: %w", err)
	}
	s.IsActive = active == 1
	vals, err := r.db.QueryContext(ctx, `SELECT id, value, sort_order FROM option_set_values WHERE option_set_id = ? ORDER BY sort_order, value`, s.ID)
	if err != nil {
		return s, fmt.Errorf("get option set values: %w", err)
	}
	defer vals.Close()
	for vals.Next() {
		var v OptionSetValueView
		if err := vals.Scan(&v.ID, &v.Value, &v.SortOrder); err != nil {
			return s, fmt.Errorf("scan option set value: %w", err)
		}
		s.Values = append(s.Values, v)
	}
	if err := vals.Err(); err != nil {
		return s, fmt.Errorf("get option set values: %w", err)
	}
	return s, nil
}

// ItemsUsingOptionSet returns the active items the set is applied to
// (item_option_sets), by name then id. A retired item's link is left out:
// /catalog can't reach it to unapply the set, so it must neither be listed
// as "used by" nor block a delete (ut-docs#3319). AssignedItem.IsActive is
// therefore always true here (the struct is shared with modifier groups,
// which do list retired items).
func (r *OptionSetRepo) ItemsUsingOptionSet(ctx context.Context, id string) ([]AssignedItem, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT i.id, i.name, i.is_active
FROM item_option_sets ios
JOIN items i ON i.id = ios.item_id
WHERE ios.option_set_id = ? AND i.is_active = 1
ORDER BY i.name, i.id`, strings.TrimSpace(id))
	if err != nil {
		return nil, fmt.Errorf("items using option set: %w", err)
	}
	defer rows.Close()
	var out []AssignedItem
	for rows.Next() {
		var it AssignedItem
		var active int
		if err := rows.Scan(&it.ID, &it.Name, &active); err != nil {
			return nil, fmt.Errorf("scan item using option set: %w", err)
		}
		it.IsActive = active == 1
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("items using option set: %w", err)
	}
	return out, nil
}

// ListOptionSetsWithItems is ListOptionSets plus each set's active items
// (retired ones left out, as in ItemsUsingOptionSet) — one more query for
// every link (item_option_sets JOIN items), joined in Go, never one query
// per set.
func (r *OptionSetRepo) ListOptionSetsWithItems(ctx context.Context) ([]OptionSetAdmin, error) {
	sets, err := r.ListOptionSets(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]OptionSetAdmin, len(sets))
	byID := make(map[string]*OptionSetAdmin, len(sets))
	for i, s := range sets {
		out[i] = OptionSetAdmin{OptionSetView: s}
		byID[s.ID] = &out[i]
	}
	if len(sets) == 0 {
		return out, nil
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT ios.option_set_id, i.id, i.name, i.is_active
FROM item_option_sets ios
JOIN items i ON i.id = ios.item_id
WHERE i.is_active = 1
ORDER BY ios.option_set_id, i.name, i.id`)
	if err != nil {
		return nil, fmt.Errorf("option set items: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var setID string
		var it AssignedItem
		var active int
		if err := rows.Scan(&setID, &it.ID, &it.Name, &active); err != nil {
			return nil, fmt.Errorf("scan option set item: %w", err)
		}
		it.IsActive = active == 1
		if s, ok := byID[setID]; ok {
			s.Items = append(s.Items, it)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("option set items: %w", err)
	}
	return out, nil
}

// DeleteOptionSetIfUnused deletes a set (its values, and any retired item's
// link, cascade) only when no active item has it applied; otherwise
// ErrOptionSetInUse and nothing is written. A retired item's variants
// generated from the set keep their item_variants rows (sales and stock
// still point at them); only their item_variant_options provenance goes,
// the same as unapplying the set on the Variants tab and then deleting it.
// An item reactivated later (my.'s save_item) comes back without the set.
// The check and the delete share one transaction so an apply can't slip in
// between. Reports whether there was a set to delete — false, nil is the
// "already deleted" replay answer, like ModifierRepo.DeleteGroupIfExists.
func (r *OptionSetRepo) DeleteOptionSetIfUnused(ctx context.Context, id string) (bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return false, errors.New("id required")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("delete option set: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	inUse, err := rowExists(ctx, tx, `SELECT 1 FROM item_option_sets ios JOIN items i ON i.id = ios.item_id WHERE ios.option_set_id = ? AND i.is_active = 1 LIMIT 1`, id)
	if err != nil {
		return false, fmt.Errorf("delete option set: usage check: %w", err)
	}
	if inUse {
		return false, ErrOptionSetInUse
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM option_sets WHERE id = ?`, id)
	if err != nil {
		return false, fmt.Errorf("delete option set: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("delete option set: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("delete option set: commit: %w", err)
	}
	return n > 0, nil
}

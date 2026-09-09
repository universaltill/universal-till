package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/universaltill/universal-till/internal/catalogtypes"
)

// OptionSetView is one reusable, named variant axis ("Size") with its
// ordered values ("S", "M", "L") — ut-docs#1900, migration 020. Shop-wide,
// not per-item: the same set generates the range on any number of items.
type OptionSetView struct {
	ID       string
	Name     string
	IsActive bool
	Values   []OptionSetValueView
}

// OptionSetValueView is one value within an OptionSetView.
type OptionSetValueView struct {
	ID        string
	Value     string
	SortOrder int
}

// ErrOptionSetExists reports that option_sets.name's UNIQUE constraint
// rejected CreateOptionSet — same shape as ErrSKUExists, so the handler can
// name the problem instead of downgrading to the generic invalid-request
// message.
var ErrOptionSetExists = errors.New("option set name already in use")

// ErrOptionSetValueExists reports that UNIQUE (option_set_id, value)
// rejected AddOptionSetValue — the same value twice in ONE set. The same
// value in two different sets is fine.
var ErrOptionSetValueExists = errors.New("option set value already exists in this set")

// ErrNoOptionSetsApplied reports that GenerateVariants was asked to generate
// an item's range before any option set was applied to it — distinct from
// "everything already existed" (created == 0, nil), which is the safe
// re-run case, so the panel can tell the operator what to do first.
var ErrNoOptionSetsApplied = errors.New("no option sets applied to this item")

// maxOptionSetAxes caps how many option sets one item's range is generated
// from. A real constraint, not a UI hint: three axes of five values is
// already 125 rows an operator would have to price and stock one by one.
const maxOptionSetAxes = 2

// OptionSetRepo owns the option_sets / option_set_values / item_option_sets /
// item_variant_options tables (migration 020). Instantiated ad hoc per
// handler call, exactly like ModifierRepo — this codebase doesn't wire
// repos into common.Deps.
type OptionSetRepo struct {
	db *sql.DB
}

func NewOptionSetRepo(db *sql.DB) *OptionSetRepo {
	return &OptionSetRepo{db: db}
}

// ListOptionSets returns every option set (active or not — the admin
// screen must still show a deactivated one) with its values in sort order.
//
// Sets come back in CREATION order (rowid — SQLite-only, like migration
// 016's dedup), not alphabetical: the item panel renders them as a checkbox
// row in this order, and the form submits the ticked ones in DOM order,
// which becomes the axis order and so the generated names ("S / Red" vs
// "Red / S"). Alphabetical made Colour outrank Size on the first driven
// run — a merchant who created Size first expects it to lead.
func (r *OptionSetRepo) ListOptionSets(ctx context.Context) ([]OptionSetView, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, name, is_active FROM option_sets ORDER BY rowid`)
	if err != nil {
		return nil, fmt.Errorf("list option sets: %w", err)
	}
	defer rows.Close()
	var sets []OptionSetView
	for rows.Next() {
		var s OptionSetView
		var active int
		if err := rows.Scan(&s.ID, &s.Name, &active); err != nil {
			return nil, fmt.Errorf("scan option set: %w", err)
		}
		s.IsActive = active == 1
		sets = append(sets, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list option sets: %w", err)
	}
	if err := r.attachValues(ctx, sets); err != nil {
		return nil, err
	}
	return sets, nil
}

// attachValues fills Values on every set in one query (not N+1), in
// sort_order then value order so a stable, deterministic list renders.
func (r *OptionSetRepo) attachValues(ctx context.Context, sets []OptionSetView) error {
	if len(sets) == 0 {
		return nil
	}
	byID := map[string]*OptionSetView{}
	for i := range sets {
		byID[sets[i].ID] = &sets[i]
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT id, option_set_id, value, sort_order
FROM option_set_values
ORDER BY option_set_id, sort_order, value`)
	if err != nil {
		return fmt.Errorf("list option set values: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var v OptionSetValueView
		var setID string
		if err := rows.Scan(&v.ID, &setID, &v.Value, &v.SortOrder); err != nil {
			return fmt.Errorf("scan option set value: %w", err)
		}
		if s, ok := byID[setID]; ok {
			s.Values = append(s.Values, v)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("list option set values: %w", err)
	}
	return nil
}

// CreateOptionSet adds a shop-wide option set and returns its new id.
// A name collision surfaces as ErrOptionSetExists.
func (r *OptionSetRepo) CreateOptionSet(ctx context.Context, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("name required")
	}
	id := uuid.NewString()
	if _, err := r.db.ExecContext(ctx, `INSERT INTO option_sets (id, name, is_active) VALUES (?, ?, 1)`, id, name); err != nil {
		if isUniqueViolation(err) {
			return "", ErrOptionSetExists
		}
		return "", fmt.Errorf("insert option set: %w", err)
	}
	return id, nil
}

// AddOptionSetValue appends a value to a set (sort_order = current max + 1
// within that set) and returns its new id. A duplicate value in the same
// set surfaces as ErrOptionSetValueExists.
func (r *OptionSetRepo) AddOptionSetValue(ctx context.Context, optionSetID, value string) (string, error) {
	optionSetID = strings.TrimSpace(optionSetID)
	value = strings.TrimSpace(value)
	if optionSetID == "" {
		return "", errors.New("option_set_id required")
	}
	if value == "" {
		return "", errors.New("value required")
	}
	id := uuid.NewString()
	// max+1 computed inside the same statement so two concurrent adds to
	// the same set can't both read the same max and land on one sort_order
	// (they'd still both insert — sort_order isn't unique — but the list
	// would then interleave nondeterministically).
	if _, err := r.db.ExecContext(ctx, `
INSERT INTO option_set_values (id, option_set_id, value, sort_order)
VALUES (?, ?, ?, (SELECT COALESCE(MAX(sort_order), 0) + 1 FROM option_set_values WHERE option_set_id = ?))`,
		id, optionSetID, value, optionSetID); err != nil {
		if isUniqueViolation(err) {
			return "", ErrOptionSetValueExists
		}
		return "", fmt.Errorf("insert option set value: %w", err)
	}
	return id, nil
}

// ItemOptionSets returns the sets applied to an item, in axis order, each
// with its values in sort order.
func (r *OptionSetRepo) ItemOptionSets(ctx context.Context, itemID string) ([]OptionSetView, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT s.id, s.name, s.is_active
FROM item_option_sets ios
JOIN option_sets s ON s.id = ios.option_set_id
WHERE ios.item_id = ?
ORDER BY ios.axis_order, s.name`, itemID)
	if err != nil {
		return nil, fmt.Errorf("item option sets: %w", err)
	}
	defer rows.Close()
	var sets []OptionSetView
	for rows.Next() {
		var s OptionSetView
		var active int
		if err := rows.Scan(&s.ID, &s.Name, &active); err != nil {
			return nil, fmt.Errorf("scan item option set: %w", err)
		}
		s.IsActive = active == 1
		sets = append(sets, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("item option sets: %w", err)
	}
	if err := r.attachValues(ctx, sets); err != nil {
		return nil, err
	}
	return sets, nil
}

// ApplyOptionSetsToItem replaces the item's applied option sets with the
// given ids (slice index = axis_order). More than maxOptionSetAxes is
// rejected outright; an empty slice clears the item's sets. Delete-then-
// insert in one transaction so a failed insert (an unknown set id, say)
// leaves the previous choice in place rather than half-replaced.
func (r *OptionSetRepo) ApplyOptionSetsToItem(ctx context.Context, itemID string, optionSetIDs []string) error {
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		return errors.New("item_id required")
	}
	if len(optionSetIDs) > maxOptionSetAxes {
		return fmt.Errorf("at most %d option sets can be applied to an item, got %d", maxOptionSetAxes, len(optionSetIDs))
	}
	seen := map[string]bool{}
	for _, id := range optionSetIDs {
		if strings.TrimSpace(id) == "" {
			return errors.New("option_set_id required")
		}
		if seen[id] {
			return fmt.Errorf("option set %s listed twice", id)
		}
		seen[id] = true
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin apply option sets: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM item_option_sets WHERE item_id = ?`, itemID); err != nil {
		return fmt.Errorf("clear item option sets: %w", err)
	}
	for i, id := range optionSetIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO item_option_sets (item_id, option_set_id, axis_order) VALUES (?, ?, ?)`, itemID, id, i); err != nil {
			return fmt.Errorf("insert item option set: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit apply option sets: %w", err)
	}
	return nil
}

// combinationKey is the generator's idempotency key for one variant: its
// option value ids, sorted, joined — independent of axis order so the same
// combination is recognised however the sets were ordered when it was
// first generated.
func combinationKey(valueIDs []string) string {
	sorted := append([]string(nil), valueIDs...)
	sort.Strings(sorted)
	return strings.Join(sorted, "|")
}

// GenerateVariants creates the item_variants rows for every combination of
// the item's applied option sets' values that doesn't exist yet, and
// returns how many were created. Re-running after nothing changed creates
// 0 and touches nothing; re-running after a value was added creates only
// the new combinations (ut-docs#1839's re-import-duplication bug class,
// deliberately not reintroduced here). Hand-added variants (no
// item_variant_options rows) are neither counted nor touched.
//
// Each new row goes through CatalogRepo.CreateVariant — the same insert the
// manual add-variant row uses — so the inventory-row side effect, the
// stock-untracked parent check and the blank-SKU auto-generation all apply
// without being reimplemented here. Name = the values joined " / " in axis
// order ("Small / Red"); price = the item's current base price; cost NULL.
func (r *OptionSetRepo) GenerateVariants(ctx context.Context, itemID string) (int, error) {
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		return 0, errors.New("item_id required")
	}
	sets, err := r.ItemOptionSets(ctx, itemID)
	if err != nil {
		return 0, err
	}
	if len(sets) == 0 {
		return 0, ErrNoOptionSetsApplied
	}
	// A set with no values contributes no combinations at all — the product
	// is empty, which is correct (nothing to generate yet), not an error.
	for _, s := range sets {
		if len(s.Values) == 0 {
			return 0, nil
		}
	}

	var basePrice int64
	if err := r.db.QueryRowContext(ctx, `SELECT base_price FROM items WHERE id = ?`, itemID).Scan(&basePrice); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, errors.New("item not found")
		}
		return 0, fmt.Errorf("read item base price: %w", err)
	}

	existing, err := r.existingCombinations(ctx, itemID)
	if err != nil {
		return 0, err
	}

	// Cartesian product across the (at most maxOptionSetAxes) applied sets.
	type combo struct {
		valueIDs []string
		names    []string
	}
	combos := []combo{{}}
	for _, s := range sets {
		var next []combo
		for _, c := range combos {
			for _, v := range s.Values {
				next = append(next, combo{
					valueIDs: append(append([]string(nil), c.valueIDs...), v.ID),
					names:    append(append([]string(nil), c.names...), v.Value),
				})
			}
		}
		combos = next
	}

	catalog := NewCatalogRepo(r.db)
	created := 0
	for _, c := range combos {
		if existing[combinationKey(c.valueIDs)] {
			continue
		}
		variantID, err := catalog.CreateVariant(ctx, catalogtypes.VariantInput{
			ItemID:   itemID,
			Name:     strings.Join(c.names, " / "),
			Price:    basePrice,
			IsActive: true,
		})
		if err != nil {
			return created, fmt.Errorf("create generated variant %q: %w", strings.Join(c.names, " / "), err)
		}
		for _, valueID := range c.valueIDs {
			if _, err := r.db.ExecContext(ctx, `INSERT INTO item_variant_options (variant_id, option_set_value_id) VALUES (?, ?)`, variantID, valueID); err != nil {
				return created, fmt.Errorf("link generated variant %s to option value %s: %w", variantID, valueID, err)
			}
		}
		created++
	}
	return created, nil
}

// existingCombinations returns the combinationKey of every variant of the
// item that was generated from option values (has item_variant_options
// rows), whatever its active flag — a deactivated generated variant is
// still "present" so a re-run never resurrects it as a duplicate.
func (r *OptionSetRepo) existingCombinations(ctx context.Context, itemID string) (map[string]bool, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT ivo.variant_id, ivo.option_set_value_id
FROM item_variant_options ivo
JOIN item_variants v ON v.id = ivo.variant_id
WHERE v.item_id = ?
ORDER BY ivo.variant_id`, itemID)
	if err != nil {
		return nil, fmt.Errorf("existing variant combinations: %w", err)
	}
	defer rows.Close()
	byVariant := map[string][]string{}
	for rows.Next() {
		var variantID, valueID string
		if err := rows.Scan(&variantID, &valueID); err != nil {
			return nil, fmt.Errorf("scan variant combination: %w", err)
		}
		byVariant[variantID] = append(byVariant[variantID], valueID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("existing variant combinations: %w", err)
	}
	keys := make(map[string]bool, len(byVariant))
	for _, valueIDs := range byVariant {
		keys[combinationKey(valueIDs)] = true
	}
	return keys, nil
}

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

// validateOptionSetIDs checks an item's option-set list — at most
// maxOptionSetAxes, no blank id, no duplicate — and returns it trimmed.
func validateOptionSetIDs(ids []string) ([]string, error) {
	if len(ids) > maxOptionSetAxes {
		return nil, fmt.Errorf("at most %d option sets can be applied to an item, got %d", maxOptionSetAxes, len(ids))
	}
	out := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, errors.New("option_set_id required")
		}
		if seen[id] {
			return nil, fmt.Errorf("option set %s listed twice", id)
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
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
	optionSetIDs, err := validateOptionSetIDs(optionSetIDs)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin apply option sets: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := applyOptionSetsToItemTx(ctx, tx, itemID, optionSetIDs); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit apply option sets: %w", err)
	}
	return nil
}

// ApplyOptionSetsToItemTx is ApplyOptionSetsToItem inside the caller's
// transaction — the cloud catalogue pull's item option_set_cloud_ids
// (ADR-0162 §4, ut-docs#4491), the item's whole ordered set. Like the
// non-Tx method it never generates variants (the cloud sends those as
// their own rows, ut-docs#4521). It also refuses an item or a set this
// till does not have by name, rather than as a bare FK failure.
func (r *OptionSetRepo) ApplyOptionSetsToItemTx(ctx context.Context, tx *sql.Tx, itemID string, optionSetIDs []string) error {
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		return errors.New("item_id required")
	}
	optionSetIDs, err := validateOptionSetIDs(optionSetIDs)
	if err != nil {
		return err
	}
	if ok, err := rowExists(ctx, tx, `SELECT 1 FROM items WHERE id = ?`, itemID); err != nil {
		return fmt.Errorf("apply option sets: check item: %w", err)
	} else if !ok {
		return fmt.Errorf("item %s does not exist on this till", itemID)
	}
	for _, id := range optionSetIDs {
		if ok, err := rowExists(ctx, tx, `SELECT 1 FROM option_sets WHERE id = ?`, id); err != nil {
			return fmt.Errorf("apply option sets: check option set: %w", err)
		} else if !ok {
			return fmt.Errorf("%w: %s", ErrOptionSetNotFound, id)
		}
	}
	return applyOptionSetsToItemTx(ctx, tx, itemID, optionSetIDs)
}

// applyOptionSetsToItemTx replaces the item's sets with the validated ids
// (index = axis_order).
func applyOptionSetsToItemTx(ctx context.Context, tx *sql.Tx, itemID string, optionSetIDs []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM item_option_sets WHERE item_id = ?`, itemID); err != nil {
		return fmt.Errorf("clear item option sets: %w", err)
	}
	for i, id := range optionSetIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO item_option_sets (item_id, option_set_id, axis_order) VALUES (?, ?, ?)`, itemID, id, i); err != nil {
			return fmt.Errorf("insert item option set: %w", err)
		}
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

// ErrTooManyVariants reports that generating an item's variant matrix
// would take it past MaxItemVariants (ut-docs#4163): the cloud keeps at
// most that many per item and SaveVariant refuses the next one, so the
// generator refuses up front, before writing anything.
var ErrTooManyVariants = errors.New("too many variants")

// TooManyVariantsError is the ErrTooManyVariants refusal with its counts,
// so the till's panel can say how many variants generating would make.
type TooManyVariantsError struct {
	New   int // combinations that would be created
	Total int // the item's variant count afterwards
	Max   int
}

func (e *TooManyVariantsError) Error() string {
	return fmt.Sprintf("%v: generating %d variants would give the item %d; an item can have at most %d", ErrTooManyVariants, e.New, e.Total, e.Max)
}

func (e *TooManyVariantsError) Is(target error) bool { return target == ErrTooManyVariants }

// variantCombo is one combination of option values, one per applied set.
type variantCombo struct {
	valueIDs []string
	names    []string
}

// missingCombinations returns the combinations of sets' values (in axis
// order) that the item has no generated variant for yet. A set with no
// values contributes no combinations at all — the product is empty, which
// is correct (nothing to generate yet), not an error.
func (r *OptionSetRepo) missingCombinations(ctx context.Context, itemID string, sets []OptionSetView) ([]variantCombo, error) {
	for _, s := range sets {
		if len(s.Values) == 0 {
			return nil, nil
		}
	}
	existing, err := r.existingCombinations(ctx, itemID)
	if err != nil {
		return nil, err
	}
	// Cartesian product across the (at most maxOptionSetAxes) applied sets.
	combos := []variantCombo{{}}
	for _, s := range sets {
		var next []variantCombo
		for _, c := range combos {
			for _, v := range s.Values {
				next = append(next, variantCombo{
					valueIDs: append(append([]string(nil), c.valueIDs...), v.ID),
					names:    append(append([]string(nil), c.names...), v.Value),
				})
			}
		}
		combos = next
	}
	missing := combos[:0]
	for _, c := range combos {
		if !existing[combinationKey(c.valueIDs)] {
			missing = append(missing, c)
		}
	}
	return missing, nil
}

// checkVariantCap refuses adding n variants when the item's existing ones
// (active or not, hand-added included) plus n would exceed MaxItemVariants.
func (r *OptionSetRepo) checkVariantCap(ctx context.Context, itemID string, n int) error {
	if n == 0 {
		return nil
	}
	var have int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM item_variants WHERE item_id = ?`, itemID).Scan(&have); err != nil {
		return fmt.Errorf("count item variants: %w", err)
	}
	if have+n > MaxItemVariants {
		return &TooManyVariantsError{New: n, Total: have + n, Max: MaxItemVariants}
	}
	return nil
}

// GenerateVariants creates the item_variants rows for every combination of
// the item's applied option sets' values that doesn't exist yet, and
// returns how many were created. Re-running after nothing changed creates
// 0 and touches nothing; re-running after a value was added creates only
// the new combinations (ut-docs#1839's re-import-duplication bug class,
// deliberately not reintroduced here). Hand-added variants (no
// item_variant_options rows) are neither counted nor touched. More than
// MaxItemVariants in total is refused before anything is created
// (ErrTooManyVariants, ut-docs#4163).
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
	missing, err := r.missingCombinations(ctx, itemID, sets)
	if err != nil || len(missing) == 0 {
		return 0, err
	}

	var basePrice int64
	if err := r.db.QueryRowContext(ctx, `SELECT base_price FROM items WHERE id = ?`, itemID).Scan(&basePrice); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, errors.New("item not found")
		}
		return 0, fmt.Errorf("read item base price: %w", err)
	}
	if err := r.checkVariantCap(ctx, itemID, len(missing)); err != nil {
		return 0, err
	}

	catalog := NewCatalogRepo(r.db)
	created := 0
	for _, c := range missing {
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

// ApplyOptionSetsResult reports what ApplyOptionSetsAndGenerate did.
type ApplyOptionSetsResult struct {
	ItemName string
	SetNames []string // the applied sets, axis order
	Created  int      // variants generated
}

// ApplyOptionSetsAndGenerate is the apply_item_option_sets directive's
// write (ut-docs#4163): replace the item's applied option sets with
// optionSetIDs (index = axis order, [] clears) and, when generate is set,
// generate the missing variants exactly as the till's own Generate button
// does. Every refusal — a missing item, an unknown set, a newly applied
// inactive set, more
// than maxOptionSetAxes, a duplicate, or a matrix past MaxItemVariants — is
// checked before anything is written. Idempotent: a replay applies the same
// sets and generates nothing new.
func (r *OptionSetRepo) ApplyOptionSetsAndGenerate(ctx context.Context, itemID string, optionSetIDs []string, generate bool) (ApplyOptionSetsResult, error) {
	var res ApplyOptionSetsResult
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		return res, errors.New("item_id required")
	}
	err := r.db.QueryRowContext(ctx, `SELECT name FROM items WHERE id = ?`, itemID).Scan(&res.ItemName)
	if errors.Is(err, sql.ErrNoRows) {
		return res, fmt.Errorf("item %s does not exist on this till", itemID)
	}
	if err != nil {
		return res, fmt.Errorf("read item: %w", err)
	}
	optionSetIDs, err = validateOptionSetIDs(optionSetIDs)
	if err != nil {
		return res, err
	}
	// An inactive set may stay applied (the till keeps it, the snapshot
	// reports it, Generate still uses it); only newly applying one is
	// refused — so re-sending the item's current list always works.
	current, err := r.ItemOptionSets(ctx, itemID)
	if err != nil {
		return res, err
	}
	applied := map[string]bool{}
	for _, s := range current {
		applied[s.ID] = true
	}
	sets := make([]OptionSetView, 0, len(optionSetIDs))
	for _, id := range optionSetIDs {
		s, err := r.GetOptionSet(ctx, id)
		if errors.Is(err, ErrOptionSetNotFound) {
			return res, fmt.Errorf("option set %s does not exist on this till", id)
		}
		if err != nil {
			return res, err
		}
		if !s.IsActive && !applied[s.ID] {
			return res, fmt.Errorf("option set %s is inactive", s.Name)
		}
		sets = append(sets, s)
		res.SetNames = append(res.SetNames, s.Name)
	}
	if generate && len(sets) > 0 {
		missing, err := r.missingCombinations(ctx, itemID, sets)
		if err != nil {
			return res, err
		}
		if err := r.checkVariantCap(ctx, itemID, len(missing)); err != nil {
			return res, err
		}
	}
	if err := r.ApplyOptionSetsToItem(ctx, itemID, optionSetIDs); err != nil {
		return res, err
	}
	if generate && len(sets) > 0 {
		res.Created, err = r.GenerateVariants(ctx, itemID)
		if err != nil {
			return res, err
		}
	}
	return res, nil
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

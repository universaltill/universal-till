package agentaction

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
)

// catalog.item.update.v1 (ut-docs#4128): change existing items' name,
// price, category and, for an item that has none, its barcode:
// {changes:[{item_id, name?, price_minor?, category_id?, barcode?}]}.
// Before/after are read from the database at propose time; at confirm each
// item is re-read inside the transaction and any difference is stale. The
// write is the item editor's: CatalogRepo.UpdateItemTx (full row, price
// history) and AddNormalizedBarcodeTx. Replacing or removing a barcode,
// clearing a category and deactivating are deliberately not offered.

type updateChange struct {
	ItemID     string  `json:"item_id"`
	Name       *string `json:"name,omitempty"`
	PriceMinor *int64  `json:"price_minor,omitempty"`
	CategoryID *string `json:"category_id,omitempty"`
	Barcode    *string `json:"barcode,omitempty"`
}

type updateArgs struct {
	Changes []updateChange `json:"changes"`
}

// updateSeen is what plan saw of one item; apply compares it to the row it
// re-reads inside the transaction.
type updateSeen struct {
	Name       string
	PriceMinor int64
	CategoryID string
	HasBarcode bool
}

func init() {
	register(&Action{
		Name:           "catalog.item.update.v1",
		Area:           "catalog",
		RolePermission: "catalog_management",
		LabelKey:       "agent.action.catalog_item_update",
		parse:          parseUpdate,
		plan:           planUpdate,
		check:          checkUpdate,
		apply:          applyUpdate,
	})
}

func parseUpdate(raw []byte) (any, error) {
	var a updateArgs
	if err := decodeStrict(raw, &a); err != nil {
		return nil, err
	}
	if len(a.Changes) == 0 || len(a.Changes) > MaxBatch {
		return nil, fmt.Errorf("%w: 1..%d changes", ErrInvalid, MaxBatch)
	}
	seen := map[string]bool{}
	seenCode := map[string]bool{}
	for i := range a.Changes {
		c := &a.Changes[i]
		c.ItemID = strings.TrimSpace(c.ItemID)
		if c.ItemID == "" || len(c.ItemID) > itemIDMax || seen[c.ItemID] {
			return nil, fmt.Errorf("%w: changes[%d].item_id", ErrInvalid, i)
		}
		seen[c.ItemID] = true
		if c.Name == nil && c.PriceMinor == nil && c.CategoryID == nil && c.Barcode == nil {
			return nil, fmt.Errorf("%w: changes[%d] changes nothing", ErrInvalid, i)
		}
		if c.Name != nil {
			n, ok := cleanText(*c.Name, itemNameMaxRunes)
			if !ok {
				return nil, fmt.Errorf("%w: changes[%d].name", ErrInvalid, i)
			}
			c.Name = &n
		}
		if c.PriceMinor != nil && (*c.PriceMinor < 0 || *c.PriceMinor > priceMinorMax) {
			return nil, fmt.Errorf("%w: changes[%d].price_minor", ErrInvalid, i)
		}
		if c.CategoryID != nil {
			id := strings.TrimSpace(*c.CategoryID)
			if id == "" || len(id) > itemIDMax {
				return nil, fmt.Errorf("%w: changes[%d].category_id", ErrInvalid, i)
			}
			c.CategoryID = &id
		}
		if c.Barcode != nil {
			b := strings.TrimSpace(*c.Barcode)
			if !barcodeRe.MatchString(b) {
				return nil, fmt.Errorf("%w: changes[%d].barcode", ErrInvalid, i)
			}
			if seenCode[b] {
				return nil, fmt.Errorf("%w: changes[%d].barcode repeated in the batch", ErrInvalid, i)
			}
			seenCode[b] = true
			c.Barcode = &b
		}
	}
	return a, nil
}

func planUpdate(ctx context.Context, env Env, args any) ([]Row, any, error) {
	a := args.(updateArgs)
	repo := data.NewCatalogRepo(env.DB)
	cats, err := repo.ListActiveCategories(ctx)
	if err != nil {
		return nil, nil, err
	}
	catName := map[string]string{}
	for _, c := range cats {
		catName[c.ID] = c.Name
	}
	var rows []Row
	seen := make([]updateSeen, len(a.Changes))
	for i, c := range a.Changes {
		it, ok, err := repo.GetItem(ctx, c.ItemID)
		if err != nil {
			return nil, nil, err
		}
		if !ok || !it.IsActive {
			return nil, nil, fmt.Errorf("%w: changes[%d].item_id not found", ErrInvalid, i)
		}
		codes, err := repo.BarcodesForItem(ctx, it.ID)
		if err != nil {
			return nil, nil, err
		}
		s := updateSeen{Name: it.Name, PriceMinor: it.BasePrice, HasBarcode: len(codes) > 0}
		if it.CategoryID != nil {
			s.CategoryID = *it.CategoryID
		}
		seen[i] = s
		if c.Name != nil && *c.Name != it.Name {
			rows = append(rows, Row{Item: it.Name, FieldKey: "agent.field.name",
				Before: Value{Kind: "text", Text: it.Name}, After: Value{Kind: "text", Text: *c.Name}})
		}
		if c.PriceMinor != nil && *c.PriceMinor != it.BasePrice {
			rows = append(rows, Row{Item: it.Name, FieldKey: "agent.field.price",
				Before: Value{Kind: "money", Minor: it.BasePrice}, After: Value{Kind: "money", Minor: *c.PriceMinor}})
		}
		if c.Barcode != nil {
			if s.HasBarcode {
				return nil, nil, fmt.Errorf("%w: changes[%d].barcode: item already has one", ErrInvalid, i)
			}
			taken, err := repo.BarcodeExists(ctx, *c.Barcode)
			if err != nil {
				return nil, nil, err
			}
			if taken {
				return nil, nil, fmt.Errorf("%w: changes[%d].barcode already in the catalogue", ErrInvalid, i)
			}
			rows = append(rows, Row{Item: it.Name, FieldKey: "agent.field.barcode",
				Before: Value{Kind: "none"}, After: Value{Kind: "text", Text: *c.Barcode}})
		}
		if c.CategoryID != nil && *c.CategoryID != s.CategoryID {
			name, ok := catName[*c.CategoryID]
			if !ok {
				return nil, nil, fmt.Errorf("%w: changes[%d].category_id not found", ErrInvalid, i)
			}
			before := Value{Kind: "none"}
			if s.CategoryID != "" {
				before = Value{Kind: "text", Text: catName[s.CategoryID]}
			}
			rows = append(rows, Row{Item: it.Name, FieldKey: "agent.field.category",
				Before: before, After: Value{Kind: "text", Text: name}})
		}
	}
	if len(rows) == 0 {
		return nil, nil, fmt.Errorf("%w: nothing would change", ErrInvalid)
	}
	return rows, seen, nil
}

// checkUpdate normalizes each new barcode for the shop's symbologies and
// re-checks it is still free (outside the transaction, like create).
func checkUpdate(ctx context.Context, env Env, args, _ any) (any, error) {
	a := args.(updateArgs)
	repo := data.NewCatalogRepo(env.DB)
	out := make([]catalogtypes.BarcodeInput, len(a.Changes))
	for i, c := range a.Changes {
		if c.Barcode == nil {
			continue
		}
		taken, err := repo.BarcodeExists(ctx, *c.Barcode)
		if err != nil {
			return nil, err
		}
		if taken {
			return nil, ErrStale
		}
		in, err := repo.NormalizeBarcode(ctx, catalogtypes.BarcodeInput{Barcode: *c.Barcode, ItemID: c.ItemID, IsPrimary: true})
		if err != nil {
			return nil, fmt.Errorf("%w: barcode: %v", ErrStale, err)
		}
		out[i] = in
	}
	return out, nil
}

func applyUpdate(ctx context.Context, env Env, tx *sql.Tx, args, state, prep any, _ string) (Result, error) {
	a := args.(updateArgs)
	seen := state.([]updateSeen)
	codes := prep.([]catalogtypes.BarcodeInput)
	repo := data.NewCatalogRepo(env.DB)
	var res Result
	for i, c := range a.Changes {
		it, ok, err := repo.GetItemTx(ctx, tx, c.ItemID)
		if err != nil {
			return Result{}, err
		}
		cat := ""
		if it.CategoryID != nil {
			cat = *it.CategoryID
		}
		if !ok || !it.IsActive || it.Name != seen[i].Name || it.BasePrice != seen[i].PriceMinor || cat != seen[i].CategoryID {
			return Result{}, ErrStale
		}
		// The barcode change was planned as "item has none": a barcode added
		// since (by hand) makes it stale, not a second primary.
		n, err := repo.ItemBarcodeCountTx(ctx, tx, c.ItemID)
		if err != nil {
			return Result{}, err
		}
		if (n > 0) != seen[i].HasBarcode {
			return Result{}, ErrStale
		}
		next := it
		changed := false
		if c.Name != nil && *c.Name != it.Name {
			next.Name, changed = *c.Name, true
			res.Changes++
		}
		if c.PriceMinor != nil && *c.PriceMinor != it.BasePrice {
			next.BasePrice, changed = *c.PriceMinor, true
			res.Changes++
		}
		if c.CategoryID != nil && *c.CategoryID != cat {
			id := *c.CategoryID
			next.CategoryID, changed = &id, true
			res.Changes++
		}
		if changed {
			if err := repo.UpdateItemTx(ctx, tx, next); err != nil {
				return Result{}, fmt.Errorf("update item: %w", err)
			}
		}
		if c.Barcode != nil {
			in := codes[i]
			in.ItemID = c.ItemID
			if err := repo.AddNormalizedBarcodeTx(ctx, tx, in); err != nil {
				return Result{}, fmt.Errorf("%w: barcode: %v", ErrStale, err)
			}
			res.Changes++
		}
	}
	return res, nil
}

package agentaction

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
)

// catalog.item.create.v1: add new items {name, price_minor, barcode?}.
// Before is "—"; a barcode already in the catalogue is refused (-4 at
// propose, stale at confirm). Items go through CatalogRepo.CreateItemTx —
// the catalog screen's insert — and the barcode through AddBarcodeTx, in
// the one transaction.

const (
	itemNameMaxRunes = 120
	priceMinorMax    = 1_000_000_000_00 // a sanity ceiling, not a business rule
)

var barcodeRe = regexp.MustCompile(`^[0-9A-Za-z\-]{1,64}$`)

type createItem struct {
	Name       string `json:"name"`
	PriceMinor *int64 `json:"price_minor"`
	Barcode    string `json:"barcode,omitempty"`
}

type createArgs struct {
	Items []createItem `json:"items"`
}

func init() {
	register(&Action{
		Name:           "catalog.item.create.v1",
		Area:           "catalog",
		RolePermission: "catalog_management",
		LabelKey:       "agent.action.catalog_item_create",
		parse:          parseCreate,
		plan:           planCreate,
		check:          checkCreate,
		apply:          applyCreate,
	})
}

func parseCreate(raw []byte) (any, error) {
	var a createArgs
	if err := decodeStrict(raw, &a); err != nil {
		return nil, err
	}
	if len(a.Items) == 0 || len(a.Items) > MaxBatch {
		return nil, fmt.Errorf("%w: 1..%d items", ErrInvalid, MaxBatch)
	}
	seen := map[string]bool{}
	for i := range a.Items {
		it := &a.Items[i]
		name, ok := cleanText(it.Name, itemNameMaxRunes)
		if !ok {
			return nil, fmt.Errorf("%w: items[%d].name", ErrInvalid, i)
		}
		it.Name = name
		if it.PriceMinor == nil || *it.PriceMinor < 0 || *it.PriceMinor > priceMinorMax {
			return nil, fmt.Errorf("%w: items[%d].price_minor", ErrInvalid, i)
		}
		it.Barcode = strings.TrimSpace(it.Barcode)
		if it.Barcode != "" {
			if !barcodeRe.MatchString(it.Barcode) || seen[it.Barcode] {
				return nil, fmt.Errorf("%w: items[%d].barcode", ErrInvalid, i)
			}
			seen[it.Barcode] = true
		}
	}
	return a, nil
}

func planCreate(ctx context.Context, env Env, args any) ([]Row, any, error) {
	a := args.(createArgs)
	repo := data.NewCatalogRepo(env.DB)
	var rows []Row
	for i, it := range a.Items {
		rows = append(rows, Row{Item: it.Name, FieldKey: "agent.field.price",
			Before: Value{Kind: "none"}, After: Value{Kind: "money", Minor: *it.PriceMinor}})
		if it.Barcode == "" {
			continue
		}
		taken, err := repo.BarcodeExists(ctx, it.Barcode)
		if err != nil {
			return nil, nil, err
		}
		if taken {
			return nil, nil, fmt.Errorf("%w: items[%d].barcode already in the catalogue", ErrInvalid, i)
		}
		rows = append(rows, Row{Item: it.Name, FieldKey: "agent.field.barcode",
			Before: Value{Kind: "none"}, After: Value{Kind: "text", Text: it.Barcode}})
	}
	return rows, nil, nil
}

// checkCreate is the still-current check outside the transaction: no
// barcode was taken in the meantime. It returns each barcode normalized
// for the shop's symbologies ("" for an item without one).
func checkCreate(ctx context.Context, env Env, args, _ any) (any, error) {
	a := args.(createArgs)
	repo := data.NewCatalogRepo(env.DB)
	out := make([]catalogtypes.BarcodeInput, len(a.Items))
	for i, it := range a.Items {
		if it.Barcode == "" {
			continue
		}
		taken, err := repo.BarcodeExists(ctx, it.Barcode)
		if err != nil {
			return nil, err
		}
		if taken {
			return nil, ErrStale
		}
		// ItemID is a placeholder for the check; apply sets the real one.
		in, err := repo.NormalizeBarcode(ctx, catalogtypes.BarcodeInput{Barcode: it.Barcode, ItemID: "pending", IsPrimary: true})
		if err != nil {
			return nil, fmt.Errorf("%w: barcode: %v", ErrStale, err)
		}
		out[i] = in
	}
	return out, nil
}

func applyCreate(ctx context.Context, env Env, tx *sql.Tx, args, _, prep any, _ string) (Result, error) {
	a := args.(createArgs)
	codes := prep.([]catalogtypes.BarcodeInput)
	repo := data.NewCatalogRepo(env.DB)
	var res Result
	for i, it := range a.Items {
		id, err := repo.CreateItemTx(ctx, tx, catalogtypes.ItemInput{Name: it.Name, BasePrice: *it.PriceMinor, IsActive: true})
		if err != nil {
			return Result{}, fmt.Errorf("create item: %w", err)
		}
		res.CreatedItems = append(res.CreatedItems, id)
		res.Changes++
		if codes[i].Barcode != "" {
			in := codes[i]
			in.ItemID = id
			if err := repo.AddNormalizedBarcodeTx(ctx, tx, in); err != nil {
				// Taken since the check: nothing of the batch lands.
				return Result{}, fmt.Errorf("%w: barcode: %v", ErrStale, err)
			}
			res.Changes++
		}
	}
	return res, nil
}

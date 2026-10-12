package agentaction

import (
	"context"
	"database/sql"
	"fmt"
	"math"

	"github.com/universaltill/universal-till/internal/data"
)

// stock.adjust.v1: move the till's own stock levels {item_id, delta,
// reason?}. Before is the current level, after is level + delta. At
// confirm the level is re-read inside the transaction; any difference is
// stale. Each change is a RecordStockMovement type "adjust" with the
// operator as actor — the manual adjust screen's write.

const (
	stockDeltaMax  = 100_000
	stockReasonMax = 200
	itemIDMax      = 64
)

type adjustChange struct {
	ItemID string   `json:"item_id"`
	Delta  *float64 `json:"delta"`
	Reason string   `json:"reason,omitempty"`
}

type adjustArgs struct {
	Changes []adjustChange `json:"changes"`
}

// adjustState is what planAdjust saw: the location and each before level.
type adjustState struct {
	LocationID string
	Before     []float64
}

func init() {
	register(&Action{
		Name:           "stock.adjust.v1",
		Area:           "stock",
		RolePermission: "stock_management",
		LabelKey:       "agent.action.stock_adjust",
		parse:          parseAdjust,
		plan:           planAdjust,
		apply:          applyAdjust,
	})
}

func parseAdjust(raw []byte) (any, error) {
	var a adjustArgs
	if err := decodeStrict(raw, &a); err != nil {
		return nil, err
	}
	if len(a.Changes) == 0 || len(a.Changes) > MaxBatch {
		return nil, fmt.Errorf("%w: 1..%d changes", ErrInvalid, MaxBatch)
	}
	seen := map[string]bool{}
	for i := range a.Changes {
		c := &a.Changes[i]
		id, ok := cleanText(c.ItemID, itemIDMax)
		if !ok || seen[id] {
			return nil, fmt.Errorf("%w: changes[%d].item_id", ErrInvalid, i)
		}
		seen[id] = true
		c.ItemID = id
		if c.Delta == nil || *c.Delta == 0 || math.IsNaN(*c.Delta) || math.Abs(*c.Delta) > stockDeltaMax {
			return nil, fmt.Errorf("%w: changes[%d].delta", ErrInvalid, i)
		}
		if c.Reason != "" {
			r, ok := cleanText(c.Reason, stockReasonMax)
			if !ok {
				return nil, fmt.Errorf("%w: changes[%d].reason", ErrInvalid, i)
			}
			c.Reason = r
		}
	}
	return a, nil
}

func planAdjust(ctx context.Context, env Env, args any) ([]Row, any, error) {
	a := args.(adjustArgs)
	loc, err := stockLocation(ctx, env.DB)
	if err != nil {
		return nil, nil, err
	}
	cat := data.NewCatalogRepo(env.DB)
	inv := data.NewPOSRepo(env.DB)
	st := adjustState{LocationID: loc}
	rows := make([]Row, 0, len(a.Changes))
	for i, c := range a.Changes {
		item, ok, err := cat.GetItem(ctx, c.ItemID)
		if err != nil {
			return nil, nil, err
		}
		if !ok || !item.IsActive || item.StockUntracked {
			return nil, nil, fmt.Errorf("%w: changes[%d].item_id not a stock-tracked item", ErrInvalid, i)
		}
		qty, _, err := inv.CurrentQty(ctx, nil, loc, c.ItemID, "")
		if err != nil {
			return nil, nil, err
		}
		st.Before = append(st.Before, qty)
		rows = append(rows, Row{Item: item.Name, FieldKey: "agent.field.stock",
			Before: Value{Kind: "number", Number: qty}, After: Value{Kind: "number", Number: qty + *c.Delta}})
	}
	return rows, st, nil
}

func applyAdjust(ctx context.Context, env Env, tx *sql.Tx, args, state, _ any, actorID string) (Result, error) {
	a := args.(adjustArgs)
	st := state.(adjustState)
	inv := data.NewPOSRepo(env.DB)
	for i, c := range a.Changes {
		qty, _, err := inv.CurrentQty(ctx, tx, st.LocationID, c.ItemID, "")
		if err != nil {
			return Result{}, err
		}
		if qty != st.Before[i] {
			return Result{}, ErrStale
		}
	}
	var res Result
	for _, c := range a.Changes {
		if _, err := inv.RecordStockMovement(ctx, tx, data.StockMovementInput{
			ItemID: c.ItemID, LocationID: st.LocationID, Type: "adjust",
			Quantity: *c.Delta, Reason: c.Reason, ActorID: actorID,
		}); err != nil {
			return Result{}, fmt.Errorf("record movement: %w", err)
		}
		res.Stock = append(res.Stock, StockChange{ItemID: c.ItemID, LocationID: st.LocationID, Delta: *c.Delta})
		res.Changes++
	}
	return res, nil
}

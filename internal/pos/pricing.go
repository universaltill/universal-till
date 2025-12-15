package pos

import (
	"context"
	"database/sql"
	"time"

	"github.com/universaltill/universal-till/internal/data"
)

// PricingRepo abstracts pricing data access (implemented in internal/data).
type PricingRepo interface {
	ResolveCurrentPrice(ctx context.Context, itemID, variantID string) (int64, error)
	AppendPriceHistoryItem(ctx context.Context, itemID string, price int64, startsAt time.Time) error
	AppendPriceHistoryVariant(ctx context.Context, variantID string, price int64, startsAt time.Time) error
}

// ResolveCurrentPrice delegates to the provided pricing repository.
func ResolveCurrentPrice(ctx context.Context, repo PricingRepo, itemID, variantID string) (int64, error) {
	return repo.ResolveCurrentPrice(ctx, itemID, variantID)
}

// AppendPriceHistoryItem delegates to the provided pricing repository.
func AppendPriceHistoryItem(ctx context.Context, repo PricingRepo, itemID string, price int64, startsAt time.Time) error {
	return repo.AppendPriceHistoryItem(ctx, itemID, price, startsAt)
}

// AppendPriceHistoryVariant delegates to the provided pricing repository.
func AppendPriceHistoryVariant(ctx context.Context, repo PricingRepo, variantID string, price int64, startsAt time.Time) error {
	return repo.AppendPriceHistoryVariant(ctx, variantID, price, startsAt)
}

// The functions below provide SQL-backed implementations used by POSRepo and tests.

func resolveCurrentPriceSQL(ctx context.Context, db *sql.DB, itemID, variantID string) (int64, error) {
	return data.NewPOSRepo(db).ResolveCurrentPrice(ctx, itemID, variantID)
}

func appendPriceHistoryItemSQL(ctx context.Context, db *sql.DB, itemID string, price int64, startsAt time.Time) error {
	return data.NewPOSRepo(db).AppendPriceHistoryItem(ctx, itemID, price, startsAt)
}

func appendPriceHistoryVariantSQL(ctx context.Context, db *sql.DB, variantID string, price int64, startsAt time.Time) error {
	return data.NewPOSRepo(db).AppendPriceHistoryVariant(ctx, variantID, price, startsAt)
}

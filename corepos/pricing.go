// Package corepos provides core POS business logic for pricing, basket, sales, and money operations.
// This package is designed to be used by both the POS and mobile app (gomobile compatible).
package corepos

import (
	"context"
	"time"
)

// PriceRepository defines the pricing data interface used by corepos.
type PriceRepository interface {
	ResolveCurrentPrice(ctx context.Context, itemID, variantID string) (int64, error)
	AppendPriceHistoryItem(ctx context.Context, itemID string, price int64, startsAt time.Time) error
	AppendPriceHistoryVariant(ctx context.Context, variantID string, price int64, startsAt time.Time) error
}

// ResolveCurrentPrice delegates to the provided repository.
func ResolveCurrentPrice(ctx context.Context, repo PriceRepository, itemID, variantID string) (int64, error) {
	return repo.ResolveCurrentPrice(ctx, itemID, variantID)
}

// AppendPriceHistoryItem delegates to the provided repository.
func AppendPriceHistoryItem(ctx context.Context, repo PriceRepository, itemID string, price int64, startsAt time.Time) error {
	return repo.AppendPriceHistoryItem(ctx, itemID, price, startsAt)
}

// AppendPriceHistoryVariant delegates to the provided repository.
func AppendPriceHistoryVariant(ctx context.Context, repo PriceRepository, variantID string, price int64, startsAt time.Time) error {
	return repo.AppendPriceHistoryVariant(ctx, variantID, price, startsAt)
}

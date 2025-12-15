package pos

import (
	"context"
	"errors"
	"strings"
)

// CatalogRepo defines the catalog search operations used by catalog searcher.
type CatalogRepo interface {
	SearchActiveItems(ctx context.Context, q string, offset, limit int) ([]ItemInput, error)
	LookupActiveVariant(ctx context.Context, variantID string) (*VariantInput, error)
}

// CatalogSearcher provides catalog lookup filtered to active items/variants.
type CatalogSearcher struct {
	repo CatalogRepo
}

func NewCatalogSearcher(repo CatalogRepo) *CatalogSearcher {
	return &CatalogSearcher{repo: repo}
}

// SearchActiveItems returns active items matching the query (name, sku, barcode) with optional limit/offset.
func (c *CatalogSearcher) SearchActiveItems(ctx context.Context, q string, offset, limit int) ([]ItemInput, error) {
	return c.repo.SearchActiveItems(ctx, q, offset, limit)
}

// LookupActiveVariant returns a variant if active; otherwise error.
func (c *CatalogSearcher) LookupActiveVariant(ctx context.Context, variantID string) (*VariantInput, error) {
	if strings.TrimSpace(variantID) == "" {
		return nil, errors.New("variantID required")
	}
	return c.repo.LookupActiveVariant(ctx, variantID)
}

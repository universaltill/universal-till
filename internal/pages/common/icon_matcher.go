package common

import (
	"context"
	"log"

	"github.com/universaltill/universal-till/internal/catimport"
	"github.com/universaltill/universal-till/internal/httpx"
)

// ShopIconMatcher builds the icon-suggestion matcher for the shop's own
// languages (default first, then staff languages; ut-docs#2654) with one
// settings read. A failed read logs and falls back to the default-language
// list — the suggestion is cosmetic and must never block an import.
func ShopIconMatcher(ctx context.Context, s interface {
	Get(context.Context, string) (string, bool, error)
}) *catimport.IconMatcher {
	stored := ""
	if s != nil {
		v, _, err := s.Get(ctx, KeyStaffLocales)
		if err != nil {
			log.Printf("[icon-suggest] staff locales unavailable, using default language: %v", err)
		} else {
			stored = v
		}
	}
	return catimport.NewIconMatcher(httpx.ShopLanguages(stored), httpx.TExact)
}

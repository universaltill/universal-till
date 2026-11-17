package common

import (
	"context"
	"log"

	"github.com/universaltill/universal-till/internal/catimport"
	"github.com/universaltill/universal-till/internal/httpx"
)

// shopLanguages reads the staff-language setting once and returns the shop's
// languages (default first). A failed read logs and uses the default only.
func shopLanguages(ctx context.Context, s interface {
	Get(context.Context, string) (string, bool, error)
}, tag string) []string {
	stored := ""
	if s != nil {
		v, _, err := s.Get(ctx, KeyStaffLocales)
		if err != nil {
			log.Printf("[%s] staff locales unavailable, using default language: %v", tag, err)
		} else {
			stored = v
		}
	}
	return httpx.ShopLanguages(stored)
}

// ShopIconMatcher builds the icon-suggestion matcher for the shop's own
// languages (default first, then staff languages; ut-docs#2654) with one
// settings read. A failed read logs and falls back to the default-language
// list — the suggestion is cosmetic and must never block an import.
func ShopIconMatcher(ctx context.Context, s interface {
	Get(context.Context, string) (string, bool, error)
}) *catimport.IconMatcher {
	return catimport.NewIconMatcher(shopLanguages(ctx, s, "icon-suggest"), httpx.TExact)
}

// ShopPickerIconGroups is the icon picker's tile data with search text in
// the shop's languages (ut-docs#6042): one settings read; a failed read
// logs and falls back to the default language — never fails the page.
func ShopPickerIconGroups(ctx context.Context, s interface {
	Get(context.Context, string) (string, bool, error)
}) []catimport.BuiltinIconGroup {
	return catimport.PickerIconGroups(shopLanguages(ctx, s, "icon-search"), httpx.TExact)
}

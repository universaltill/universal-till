package pages

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// registerSuggestions serves the "customers also buy" strip under the basket
// (docs: architecture/ai-integration.md §h). Suggestions come from the local
// related_items co-occurrence table — an indexed SQLite lookup, no network —
// so the strip is safe inside the checkout path (ADR-0003). The basket
// partial re-loads it on every swap, keeping it in step with the basket.
func registerSuggestions(mux *http.ServeMux, d *common.Deps) {
	repo := data.NewRelatedItemsRepo(d.Db)
	catalogRepo := data.NewCatalogRepo(d.Db)

	mux.HandleFunc("GET /ui/suggestions", func(w http.ResponseWriter, r *http.Request) {
		var itemIDs []string
		seen := map[string]bool{}
		for _, line := range d.Engine.Lines() {
			if line.ItemID != "" && !seen[line.ItemID] {
				seen[line.ItemID] = true
				itemIDs = append(itemIDs, line.ItemID)
			}
		}
		var suggestions []data.Suggestion
		if len(itemIDs) > 0 {
			// Errors degrade to an empty (hidden) strip: suggestions are
			// assistive and must never break the sale screen.
			if s, err := repo.SuggestForBasket(r.Context(), itemIDs, 4); err == nil {
				suggestions = s
			}
		}
		// ut-docs#2258 review, BLOCKER: SuggestForBasket's own SELECT reads
		// raw base_price, so this chip strip had the identical
		// quote-vs-charge gap as the sale-screen tile — worse here, since
		// the chip posts to the SAME /api/pos/scan endpoint the fixed tile
		// does (web/ui/partials/suggestions.html), so a promo price would
		// show wrong right under a basket that would charge it right.
		// At most 4 suggestions per render (the SuggestForBasket limit
		// above), so this is one small extra batched lookup, not an N+1.
		if len(suggestions) > 0 {
			ids := make([]string, 0, len(suggestions))
			for _, s := range suggestions {
				ids = append(ids, s.ItemID)
			}
			currentPrices, err := catalogRepo.ItemCurrentPrices(r.Context(), ids)
			if err != nil {
				// Same non-fatal-but-loud treatment as the tile fixes:
				// every chip falls back to its already-set raw base_price.
				logging.L().Warnf("suggestions: load item current prices failed, every chip falls back to raw base_price (ut-docs#2258): %v", err)
			}
			for i, s := range suggestions {
				if p, ok := currentPrices[s.ItemID]; ok {
					suggestions[i].PriceMinor = p
				}
			}
		}
		httpx.RenderPartial("ui/partials/suggestions.html", map[string]any{
			"Suggestions": suggestions,
		})(w, r)
	})
}

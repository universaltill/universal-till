package pages

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// registerSuggestions serves the "customers also buy" strip under the basket
// (docs: architecture/ai-integration.md §h). Suggestions come from the local
// related_items co-occurrence table — an indexed SQLite lookup, no network —
// so the strip is safe inside the checkout path (ADR-0003). The basket
// partial re-loads it on every swap, keeping it in step with the basket.
func registerSuggestions(mux *http.ServeMux, d *common.Deps) {
	repo := data.NewRelatedItemsRepo(d.Db)

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
		httpx.RenderPartial("ui/partials/suggestions.html", map[string]any{
			"Suggestions": suggestions,
		})(w, r)
	})
}

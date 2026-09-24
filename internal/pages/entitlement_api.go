package pages

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/entitlement"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// GET /api/entitlement (ut-docs#2547): the read surface ADR-0060 §6's
// follow-up UI (ut-docs#2569) builds on — the cached subscription
// entitlement as last confirmed by the cloud, plus the plan the till
// actually honours right now (entitlement.EffectivePlan) and what that plan
// allows.
//
// Read-only: it never writes the cache and never calls the network — only
// internal/cloudsync's sync response writes these keys. Not a sale gate
// either (ADR-0060 §5/§7): nothing on the sale path calls this.
//
// Gated by the "settings" action (manager/admin/super_admin), the same
// canPerform check + localized 403 as the other manager-only settings JSON
// endpoints (print_api.go, categories_page.go): the subscription is a
// shop-wide setting, not a Tills-page (sync_management) concern.
func registerEntitlementAPI(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /api/entitlement", entitlementHandler(d))
}

func entitlementHandler(d *common.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "settings") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return
		}
		ctx := r.Context()
		repo := data.NewSettingsRepo(d.Db)

		cached := map[string]string{}
		for _, k := range []string{
			entitlement.KeyPlan,
			entitlement.KeySubscriptionStatus,
			entitlement.KeyExpiresAt,
			entitlement.KeyLastConfirmedAt,
		} {
			v, _, err := repo.Get(ctx, k)
			if err != nil {
				common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "common.error.server", "entitlement", err)
				return
			}
			cached[k] = v
		}

		effective := entitlement.EffectivePlan(ctx, repo, time.Now())
		caps := make(map[string]bool, len(entitlement.Capabilities()))
		for _, c := range entitlement.Capabilities() {
			caps[string(c)] = entitlement.Allows(effective, c)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"effective_plan":      string(effective),
				"plan":                cached[entitlement.KeyPlan],
				"subscription_status": cached[entitlement.KeySubscriptionStatus],
				"expires_at":          cached[entitlement.KeyExpiresAt],
				"last_confirmed_at":   cached[entitlement.KeyLastConfirmedAt],
				"grace_hours":         int(entitlement.Grace / time.Hour),
				"capabilities":        caps,
			},
			"error": nil,
		})
	}
}

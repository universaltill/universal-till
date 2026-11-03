package pages

import (
	"context"
	"net/http"
	"strings"

	"github.com/universaltill/universal-till/internal/entitlement"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#5058 UT-3: after a till is set up from the shop's cloud account,
// the sale screen shows a manager a dismissible "Next steps" card for what
// the cloud does not copy to a new till (staff PINs, card terminal and
// printer, stock counts, and the products on a plan that keeps them on the
// till). Not a modal and never auto-hidden: the work it lists is real until
// done. One per-till settings row, no migration.

// nextStepsSettingKey is "pending:<catalog_master>" after a cloud setup,
// "done" once a manager closed the card, absent otherwise. "cloudsync." is a
// per-till prefix: a replica joined to this till neither inherits nor shows it.
const nextStepsSettingKey = "cloudsync.next_steps"

const nextStepsPending = "pending:"

// nextStepsCard is the template data of the card.
type nextStepsCard struct {
	// TillMaster: this shop's products live on the till, so a cloud setup
	// copied none (only when known: see nextStepsView).
	TillMaster bool
}

// markNextSteps records that a cloud setup just finished. Best effort: with
// no row the sale screen simply shows no card.
func markNextSteps(ctx context.Context, d *common.Deps, catalogMaster string) {
	mode := ""
	switch catalogMaster {
	case entitlement.CatalogMasterTill, entitlement.CatalogMasterAdopting, entitlement.CatalogMasterCloud:
		mode = catalogMaster
	}
	if err := d.Settings.Set(ctx, nextStepsSettingKey, nextStepsPending+mode); err != nil {
		logging.L().Warnf("setup wizard: record the next steps card: %v", err)
	}
}

// nextStepsView returns the card for this request, or nil: no settings
// permission, nothing pending, or any read error (fail quiet).
func nextStepsView(r *http.Request, d *common.Deps) *nextStepsCard {
	if d == nil || d.Settings == nil || !canPerform(d, r, "settings") {
		return nil
	}
	v, ok, err := d.Settings.Get(r.Context(), nextStepsSettingKey)
	if err != nil || !ok || !strings.HasPrefix(v, nextStepsPending) {
		return nil
	}
	// The cached entitlement is the live truth once a check-in landed; until
	// then the mode the pairing answered with. Never the "no cache means
	// till" default: a cloud shop would be told its products were not copied.
	till := strings.TrimPrefix(v, nextStepsPending) == entitlement.CatalogMasterTill
	if raw, cached, cerr := d.Settings.Get(r.Context(), entitlement.KeyCatalogMaster); cerr == nil && cached {
		till = entitlement.CatalogMasterMode(raw) == entitlement.CatalogMasterTill
	}
	return &nextStepsCard{TillMaster: till}
}

// registerNextStepsUI: POST /ui/next-steps/dismiss closes the card for good.
// Same shape as POST /api/release-notes/seen: a settings user only; the
// empty 200 replaces the card (hx-swap outerHTML); an error leaves it.
func registerNextStepsUI(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("POST /ui/next-steps/dismiss", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "settings") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return
		}
		if err := d.Settings.Set(r.Context(), nextStepsSettingKey, "done"); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "common.error.server", "next steps: dismiss", err)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
	})
}

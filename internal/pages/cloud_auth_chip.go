package pages

import (
	"context"
	"net/http"
	"strings"

	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/entitlement"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ADR-0116 D6 (ut-docs#3524): after 3 consecutive 401s on a till-auth
// endpoint the till keeps selling offline, retries the cloud hourly
// (cloudsync's scheduler) and shows this status chip — never a modal. Its
// words come from the 401's machine code and whether the store has an
// owner:
//
//	device_revoked                          → "Removed from the shop's cloud account"
//	token_retired / unauthorized, claimed   → "This till needs pairing again"
//	token_retired / unauthorized, anonymous → "Register as a new store"
//
// device_revoked always gets its own words: only an owner can revoke, so
// an anonymous store never sees it.

// cloudAuthChipStatus is the chip's source; a var so tests can pin it.
var cloudAuthChipStatus = cloudsync.AuthChipStatus

// The chip's variants (data-cloud-auth in the partial).
const (
	cloudAuthNone     = ""
	cloudAuthRevoked  = "revoked"
	cloudAuthRepair   = "repair"
	cloudAuthRegister = "register"
)

// cloudAuthChipVariant picks the chip's words.
func cloudAuthChipVariant(st cloudsync.AuthChip, hasOwner bool) string {
	switch {
	case !st.Show:
		return cloudAuthNone
	case st.Code == cloudsync.AuthCodeDeviceRevoked:
		return cloudAuthRevoked
	case hasOwner:
		return cloudAuthRepair
	default:
		return cloudAuthRegister
	}
}

// storeHasOwner reports whether this till's store is known to be claimed.
// The till keeps no "claimed" flag and the sync contract sends none, so the
// only local evidence of an owner is the cached entitlement (ADR-0060 §4):
// a subscription — paid plan, or a status other than none — can only exist
// on an owner's account. Without that evidence the store counts as
// anonymous. Known gap: a claimed store that never subscribed looks
// anonymous until the cloud sends an owner signal.
func storeHasOwner(ctx context.Context, kv interface {
	Get(ctx context.Context, key string) (string, bool, error)
}) bool {
	plan, _, err := kv.Get(ctx, entitlement.KeyPlan)
	if err != nil {
		return false
	}
	status, _, err := kv.Get(ctx, entitlement.KeySubscriptionStatus)
	if err != nil {
		return false
	}
	p := entitlement.Plan(strings.TrimSpace(plan))
	if p.Valid() && p != entitlement.PlanLocal {
		return true
	}
	switch strings.TrimSpace(status) {
	case entitlement.StatusActive, entitlement.StatusLapsed:
		return true
	}
	return false
}

// registerCloudAuthChip: GET /ui/cloud-auth-chip, polled from the status
// bar on every page (base.html). Empty 200 unless the till is locked out;
// then the chip for every role — it is the till's state — linking to
// Settings → Registration only for a viewer who may open Settings.
func registerCloudAuthChip(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /ui/cloud-auth-chip", func(w http.ResponseWriter, r *http.Request) {
		st := cloudAuthChipStatus()
		if !st.Show {
			w.WriteHeader(http.StatusOK)
			return
		}
		variant := cloudAuthChipVariant(st, storeHasOwner(r.Context(), d.Settings))
		httpx.RenderPartial("ui/partials/cloud_auth_chip.html", map[string]any{
			"variant":   variant,
			"canManage": canPerform(d, r, "settings"),
		})(w, r)
	})
}

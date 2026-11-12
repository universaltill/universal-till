package pages

import (
	"context"
	"net/http"
	"strings"

	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/enroll"
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

// restoredChipStatus is the registration state the restored variant gates
// on; a var so tests can pin it.
var restoredChipStatus = enroll.CurrentStatus

// The chip's variants (data-cloud-auth in the partial).
const (
	cloudAuthNone     = ""
	cloudAuthRevoked  = "revoked"
	cloudAuthRepair   = "repair"
	cloudAuthRegister = "register"
	cloudAuthReplaced = "replaced" // ut-docs#5058: replaced as main till
	cloudAuthRestored = "restored" // ut-docs#5157: data restored onto a new device
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
// The cloud's own signal wins: the entitlement block's `claimed` boolean,
// cached as entitlement.claimed (ADR-0060 amendment 2026-11, ut-docs#3532).
// It is authoritative both ways, so a claimed store that never subscribed
// is "owned" and a store the cloud calls unclaimed is not, whatever plan is
// cached. Only when the till was never told (an older cloud, or no check-in
// since the upgrade) does it fall back to the older inference from the
// cached entitlement (ADR-0060 §4): a subscription — paid plan, or a status
// other than none — can only exist on an owner's account; with no evidence
// at all the store counts as anonymous. Wording only, never a sale gate.
func storeHasOwner(ctx context.Context, kv interface {
	Get(ctx context.Context, key string) (string, bool, error)
}) bool {
	if claimed, known := entitlement.StoreClaimed(ctx, kv); known {
		return claimed
	}
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
		// ut-docs#5058: a till the owner replaced says so, by name, from
		// its first refused call and across restarts (persisted), whatever
		// the 401 streak says.
		if rp, replaced := tillReplaced(r, d); replaced {
			locale := httpx.ResolveLocale(w, r)
			httpx.RenderPartial("ui/partials/cloud_auth_chip.html", map[string]any{
				"variant":      cloudAuthReplaced,
				"canManage":    canPerform(d, r, "settings"),
				"replacedBy":   replacedByText(locale, rp),
				"replacedHint": replacedHintText(locale, rp),
			})(w, r)
			return
		}
		// ut-docs#5157: this till's data was restored onto a new device and
		// its cloud identity reset. Shown until it is registered or paired
		// again (or its main till vouches for it), after the replaced case.
		// A replica is told to join the main till again instead (status bar
		// and Tills page, ut-docs#5162): Registration is the wrong fix there.
		if d != nil && d.Settings != nil && d.SyncPrimaryURL(r.Context()) == "" && enroll.RestoredState(r.Context(), d.Settings) {
			if es := restoredChipStatus(); !es.Registered && !es.ViaMainTill {
				httpx.RenderPartial("ui/partials/cloud_auth_chip.html", map[string]any{
					"variant":   cloudAuthRestored,
					"canManage": canPerform(d, r, "settings"),
				})(w, r)
				return
			}
		}
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

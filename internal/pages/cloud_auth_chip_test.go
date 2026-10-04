package pages

import (
	"net/http"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/entitlement"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ADR-0116 D6 (ut-docs#3524): after 3 consecutive 401s the status bar shows
// a chip — never a modal — whose words depend on the 401's machine code
// and on whether the store has an owner.

func newCloudAuthChipDeps(t *testing.T, st cloudsync.AuthChip) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	orig := cloudAuthChipStatus
	cloudAuthChipStatus = func() cloudsync.AuthChip { return st }
	t.Cleanup(func() { cloudAuthChipStatus = orig })
	mux, _, d := newFullAuthDeps(t)
	registerCloudAuthChip(mux, d)
	return mux, d
}

// claimStore seeds the cached entitlement a claimed store's sync leaves
// behind (an owner's paid plan).
func claimStore(t *testing.T, d *common.Deps) {
	t.Helper()
	if err := d.Settings.Set(t.Context(), entitlement.KeyPlan, "shop"); err != nil {
		t.Fatal(err)
	}
	if err := d.Settings.Set(t.Context(), entitlement.KeySubscriptionStatus, "active"); err != nil {
		t.Fatal(err)
	}
}

func TestCloudAuthChip_EmptyBeforeTheThird401(t *testing.T) {
	mux, _ := newCloudAuthChipDeps(t, cloudsync.AuthChip{}) // 0, 1 or 2 401s: not shown
	rec := getAs(mux, "/ui/cloud-auth-chip", &mgrUser)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "" {
		t.Fatalf("not locked out: code=%d body=%q, want an empty 200", rec.Code, rec.Body.String())
	}
}

func TestCloudAuthChip_RendersEachVariantVerbatim(t *testing.T) {
	const (
		revoked  = "Removed from the shop&#39;s cloud account"
		repair   = "This till needs pairing again"
		register = "Register as a new store"
	)
	all := []string{revoked, repair, register}
	for _, c := range []struct {
		name    string
		code    string
		claimed bool
		want    string
		variant string
	}{
		{"device_revoked on a claimed store", cloudsync.AuthCodeDeviceRevoked, true, revoked, "revoked"},
		{"device_revoked with no owner evidence", cloudsync.AuthCodeDeviceRevoked, false, revoked, "revoked"},
		{"token_retired on a claimed store", cloudsync.AuthCodeTokenRetired, true, repair, "repair"},
		{"unauthorized on a claimed store", cloudsync.AuthCodeUnauthorized, true, repair, "repair"},
		{"token_retired on an anonymous store", cloudsync.AuthCodeTokenRetired, false, register, "register"},
		{"unauthorized on an anonymous store", cloudsync.AuthCodeUnauthorized, false, register, "register"},
	} {
		t.Run(c.name, func(t *testing.T) {
			mux, d := newCloudAuthChipDeps(t, cloudsync.AuthChip{Show: true, Code: c.code})
			if c.claimed {
				claimStore(t, d)
			}
			rec := getAs(mux, "/ui/cloud-auth-chip", &mgrUser)
			body := rec.Body.String()
			if rec.Code != http.StatusOK || !strings.Contains(body, c.want) {
				t.Fatalf("code=%d body=%s, want %q", rec.Code, body, c.want)
			}
			for _, other := range all {
				if other != c.want && strings.Contains(body, other) {
					t.Fatalf("body also carries %q: %s", other, body)
				}
			}
			if !strings.Contains(body, `data-cloud-auth="`+c.variant+`"`) {
				t.Fatalf("body lacks data-cloud-auth=%q: %s", c.variant, body)
			}
			if !strings.Contains(body, `href="/settings#registration"`) {
				t.Fatalf("manager chip must link to Settings → Registration, got %s", body)
			}
			// A status chip, never a modal (ADR-0116 D6, CLAUDE.md offline-first).
			for _, modal := range []string{"<dialog", "showModal", "role=\"alertdialog\""} {
				if strings.Contains(body, modal) {
					t.Fatalf("chip carries %q — must never be a modal: %s", modal, body)
				}
			}
		})
	}
}

// The chip shows for every role (it is the till's state, and it keeps
// selling), but only a viewer who may open Settings gets a link — a
// cashier never gets one that 403s (same gating as the diagnostics chip).
func TestCloudAuthChip_CashierSeesNoLink(t *testing.T) {
	mux, _ := newCloudAuthChipDeps(t, cloudsync.AuthChip{Show: true, Code: cloudsync.AuthCodeDeviceRevoked})
	body := getAs(mux, "/ui/cloud-auth-chip", &cashUser).Body.String()
	if !strings.Contains(body, "Removed from the shop&#39;s cloud account") {
		t.Fatalf("cashier must still see the chip, got %s", body)
	}
	if strings.Contains(body, "href=") {
		t.Fatalf("cashier chip must not link to Settings, got %s", body)
	}
}

func TestStoreHasOwner(t *testing.T) {
	for _, c := range []struct {
		name         string
		plan, status string
		want         bool
	}{
		{"no cached entitlement", "", "", false},
		{"free plan, no subscription", "local", "none", false},
		{"paid plan, active", "pro", "active", true},
		{"paid plan, lapsed", "shop", "lapsed", true},
		{"local plan, lapsed subscription", "local", "lapsed", true},
		{"unknown plan", "gold", "none", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, d := newCloudAuthChipDeps(t, cloudsync.AuthChip{})
			if c.plan != "" {
				_ = d.Settings.Set(t.Context(), entitlement.KeyPlan, c.plan)
			}
			if c.status != "" {
				_ = d.Settings.Set(t.Context(), entitlement.KeySubscriptionStatus, c.status)
			}
			if got := storeHasOwner(t.Context(), d.Settings); got != c.want {
				t.Fatalf("storeHasOwner = %t, want %t", got, c.want)
			}
		})
	}
}

package common

import (
	"net/http"
	"os"
	"strings"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
)

// PersonalDisplayAction is the permission action that lets an operator's own
// user_display_settings rows override the till/shop display settings
// (ut-docs#3149). Migration 061 registers it in permission_actions; migration
// 102 (ut-docs#3440) grants it to admin, manager and super_admin — cashier is
// sale-only. A role without a grant (AuthSvc.Can false), and a till whose
// permission_actions lacks the row altogether (older schema), get the
// till/shop value from both resolvers below.
const PersonalDisplayAction = "personal_display"

// ResolvedTheme is the theme to render for this request: the signed-in
// operator's personal theme when they are allowed one and have set one,
// otherwise the till/shop theme from CurrentState().
func (d *Deps) ResolvedTheme(r *http.Request) string {
	if v, ok := d.personalDisplayValue(r, KeyTheme); ok && strings.TrimSpace(v) != "" {
		// A theme uninstalled since it was saved falls through to the till
		// theme (ut-docs#3440); a nil hook means "don't check".
		if d.ThemeAvailable == nil || d.ThemeAvailable(r.Context(), v) {
			return v
		}
	}
	return d.CurrentState().Theme
}

// ResolvedBrowsingMode is ResolvedTheme's twin for the sell screen's
// browsing layout. A personal value is clamped to the closed enum, the same
// defence LoadState applies to the till-wide row.
func (d *Deps) ResolvedBrowsingMode(r *http.Request) string {
	if v, ok := d.personalDisplayValue(r, KeyBrowsingMode); ok {
		return ClampBrowsingMode(v)
	}
	return d.CurrentState().BrowsingMode
}

// personalDisplayValue returns the request operator's personal value for key,
// or ok=false whenever anything is missing or denied — the caller then uses
// the till/shop value. It never surfaces an error: a display preference must
// not be able to break a page render.
//
// The permission check mirrors pages.canPerform (duplicated, not imported:
// package common must not import pages — same reason httpx.go duplicates
// saleScreenReturnURLFor): UT_AUTH=off passes it, otherwise
// AuthSvc.Can(personal_display) must grant it, failing closed on error.
func (d *Deps) personalDisplayValue(r *http.Request, key string) (string, bool) {
	if d.Db == nil {
		return "", false
	}
	u, ok := auth.FromContext(r.Context())
	if !ok || u.ID == "" {
		return "", false
	}
	if !auth.Disabled(os.Getenv("UT_AUTH")) {
		if d.AuthSvc == nil {
			return "", false
		}
		can, err := d.AuthSvc.Can(r.Context(), u, PersonalDisplayAction)
		if err != nil || !can {
			return "", false
		}
	}
	v, found, err := data.NewUserDisplayRepo(d.Db).Get(r.Context(), u.ID, key)
	if err != nil || !found {
		return "", false
	}
	return v, true
}

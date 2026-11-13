package pages

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// personalDisplayAction is the gate for the page, its API and the chip link.
// A package-level literal const (not common.PersonalDisplayAction) so the
// permission-page guard can see the gate; TestMyDisplay_ActionConstMatchesCommon
// keeps it equal to the resolver's.
const personalDisplayAction = "personal_display"

// myDisplayMaxValue bounds a posted theme key / mode before it touches the
// store; real keys are short filenames or plugin entry keys.
const myDisplayMaxValue = 128

// registerMyDisplayPage serves "My display" (ut-docs#3440, part 3/3 of the
// personal display): the signed-in operator's own theme and sale-screen
// browsing layout, stored per user in user_display_settings and resolved at
// render time by Deps.ResolvedTheme / ResolvedBrowsingMode. Gated on the
// personal_display action (migration 102 grants admin/manager/super_admin).
//
// An additional till (one that follows a main till) refuses the writes with
// 409 rather than writing through: the rows sync down from the main till,
// and a write-through would need a new apply route there (follow-up).
// No settingsAudit row: a cosmetic per-operator preference.
func registerMyDisplayPage(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /my-display", func(w http.ResponseWriter, r *http.Request) {
		if !requirePage(d, w, r, personalDisplayAction) {
			return
		}
		ctx := r.Context()
		u, ok := auth.FromContext(ctx)
		noOperator := !ok || u.ID == ""
		personal := map[string]string{}
		if !noOperator {
			all, err := data.NewUserDisplayRepo(d.Db).GetAll(ctx, u.ID)
			if err != nil {
				httpx.RenderError(w, r, http.StatusInternalServerError, "common.error.server", err)
				return
			}
			personal = all
		}
		st := d.CurrentState()
		loc := httpx.ResolveLocale(w, r)
		themes := availableThemes(ctx, d)
		tillThemeLabel := st.Theme
		for _, opt := range themes {
			if opt.Key == st.Theme {
				tillThemeLabel = httpx.T(loc, opt.Label)
				break
			}
		}
		defaultFmt := httpx.T(loc, "settings.my_display.use_till_default")
		modeDefault := fmt.Sprintf(defaultFmt, httpx.T(loc, "settings.sell_screen.browsing_mode_"+common.ClampBrowsingMode(st.BrowsingMode)))

		savedTheme := personal[common.KeyTheme]
		themeMissing := savedTheme != "" && !themeAvailable(ctx, d, savedTheme)
		themeMissingText := ""
		if themeMissing {
			themeMissingText = fmt.Sprintf(httpx.T(loc, "settings.my_display.theme_unavailable"), savedTheme)
		}
		savedMode := personal[common.KeyBrowsingMode]
		hintMode := savedMode
		if hintMode == "" {
			hintMode = common.ClampBrowsingMode(st.BrowsingMode)
		}
		httpx.Render("ui/pages/my_display.html", map[string]any{
			"title":            httpx.T(loc, "page.title.my_display"),
			"theme":            d.ResolvedTheme(r),
			"menuItems":        d.MenuSnapshot(),
			"themes":           themes,
			"themeDefault":     fmt.Sprintf(defaultFmt, tillThemeLabel),
			"modeDefault":      modeDefault,
			"savedTheme":       savedTheme,
			"savedMode":        savedMode,
			"hintMode":         hintMode,
			"themeMissing":     themeMissing,
			"themeMissingText": themeMissingText,
			"followsMain":      tillFollowsMain(ctx, d),
			"noOperator":       noOperator,
		})(w, r)
	})

	mux.HandleFunc("POST /api/my-display/theme", func(w http.ResponseWriter, r *http.Request) {
		saveMyDisplay(d, w, r, common.KeyTheme, "theme", func(ctx context.Context, v string) bool {
			return themeAvailable(ctx, d, v)
		}, "settings.my_display.error.unknown_theme")
	})
	mux.HandleFunc("POST /api/my-display/browsing-mode", func(w http.ResponseWriter, r *http.Request) {
		saveMyDisplay(d, w, r, common.KeyBrowsingMode, "mode", func(_ context.Context, v string) bool {
			// Exact enum, not ClampBrowsingMode: an unknown value is a 400,
			// not a silent coercion to the default.
			switch v {
			case common.BrowsingModeCategoryTabs, common.BrowsingModeAllFilterChips, common.BrowsingModeStripOverflow:
				return true
			}
			return false
		}, "settings.my_display.error.unknown_mode")
	})
}

// saveMyDisplay is the shared write path of both endpoints. Only the
// signed-in operator's own id is ever used — no user_id field is read. An
// empty value clears the operator's row ("use the till's choice").
func saveMyDisplay(d *common.Deps, w http.ResponseWriter, r *http.Request, key, field string, valid func(context.Context, string) bool, badKey string) {
	if !canPerform(d, r, personalDisplayAction) {
		common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
		return
	}
	ctx := r.Context()
	u, ok := auth.FromContext(ctx)
	if !ok || u.ID == "" {
		common.LocalizedError(w, r, http.StatusForbidden, "settings.my_display.no_operator")
		return
	}
	if tillFollowsMain(ctx, d) {
		common.LocalizedError(w, r, http.StatusConflict, "settings.my_display.error.replica_use_primary")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if err := r.ParseForm(); err != nil {
		common.LocalizedError(w, r, http.StatusBadRequest, "settings.my_display.error.invalid")
		return
	}
	v := strings.TrimSpace(r.Form.Get(field))
	if len(v) > myDisplayMaxValue {
		common.LocalizedError(w, r, http.StatusBadRequest, "settings.my_display.error.invalid")
		return
	}
	repo := data.NewUserDisplayRepo(d.Db)
	var err error
	switch {
	case v == "":
		err = repo.Delete(ctx, u.ID, key)
	case !valid(ctx, v):
		common.LocalizedError(w, r, http.StatusBadRequest, badKey)
		return
	default:
		err = repo.Set(ctx, u.ID, key, v)
	}
	if err != nil {
		common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "common.error.server", "my-display", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

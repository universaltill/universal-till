package pages

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#2999: menu.restored_keys is shop-wide, so restoring (or re-hiding)
// a hidden Menu tile on a till that follows a main till goes through the
// main till's /api/sync/settings/apply. A local-only write would be reverted
// by the next admin pull.

func newMenuLayoutReplica(t *testing.T, mainURL string) (*http.ServeMux, *common.Deps) {
	t.Helper()
	mux, dp := newMenuLayoutSettingsDeps(t)
	// These deps carry no auth service: the replica's own gate is off, and
	// the session user (mgrUser, "m1") still travels as the actor the main
	// till decides on.
	t.Setenv("UT_AUTH", "off")
	setReplicaSettings(t, dp.Settings, mainURL, syncSettingsBearer)
	installBuiltinLayout(t, dp)
	return mux, dp
}

func TestMenuLayoutSettings_RestoreOnAdditionalTillLandsOnMain(t *testing.T) {
	main := newSettingsSyncMain(t)
	mux, dp := newMenuLayoutReplica(t, main.srv.URL)

	rec := postForm(mux, "/api/settings/menu/restore", url.Values{"key": {"/tables"}}, &mgrUser)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/settings/menu" {
		t.Fatalf("restore = %d Location=%q: %s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if main.calls.Load() != 1 {
		t.Fatalf("main till calls = %d, want 1", main.calls.Load())
	}
	if got := mustSetting(t, main.dp, common.MenuRestoredKeysSetting); got != `["/tables"]` {
		t.Fatalf("main till %s = %q, want [\"/tables\"] -- the restore must land on the main till", common.MenuRestoredKeysSetting, got)
	}
	if got := mustSetting(t, dp, common.MenuRestoredKeysSetting); got != `["/tables"]` {
		t.Fatalf("replica %s = %q, want the mirrored value", common.MenuRestoredKeysSetting, got)
	}
	if !common.RestoredMenuKeys(t.Context(), dp.Settings)["/tables"] {
		t.Fatal("replica must read the restore back")
	}

	rec = postForm(mux, "/api/settings/menu/rehide", url.Values{"key": {"/tables"}}, &mgrUser)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("rehide = %d: %s", rec.Code, rec.Body.String())
	}
	if main.calls.Load() != 2 {
		t.Fatalf("main till calls = %d, want 2", main.calls.Load())
	}
	if got := mustSetting(t, main.dp, common.MenuRestoredKeysSetting); got != `[]` {
		t.Fatalf("main till %s = %q after rehide, want []", common.MenuRestoredKeysSetting, got)
	}
}

// Main till unreachable: refused with the write-through message on the page,
// nothing written locally.
func TestMenuLayoutSettings_RestoreOnAdditionalTillUnreachableWritesNothing(t *testing.T) {
	mux, dp := newMenuLayoutReplica(t, deadPrimaryURL())

	rec := postForm(mux, "/api/settings/menu/restore", url.Values{"key": {"/tables"}}, &mgrUser)
	want := "/settings/menu?err=settings.error.main_till_unreachable"
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != want {
		t.Fatalf("restore = %d Location=%q, want a redirect to %s", rec.Code, rec.Header().Get("Location"), want)
	}
	if got := mustSetting(t, dp, common.MenuRestoredKeysSetting); got != "" {
		t.Fatalf("refused restore wrote locally: %s = %q", common.MenuRestoredKeysSetting, got)
	}
	page := getWithUser(mux, want, &mgrUser).Body.String()
	if !strings.Contains(page, "Can&#39;t reach the main till") && !strings.Contains(page, "Can't reach the main till") {
		t.Fatalf("page must show the unreachable message, got: %s", page)
	}
}

// Review finding 1: the main till applying a follower's restore (and a
// follower pulling one) re-derives through newRederiveSettings, which must
// rebuild the cached Menu amendments too -- otherwise the store says
// "restored" while the Menu keeps hiding the tile until a plugin reload.
func TestRederiveSettings_RebuildsMenuAmendmentsFromRestoredKeys(t *testing.T) {
	mux, dp := newMenuLayoutSettingsDeps(t)
	t.Setenv("UT_AUTH", "off")
	dp.AuthSvc = auth.NewService(dp.Db)
	installBuiltinLayout(t, dp)
	if body := getMenu(t, mux); strings.Contains(body, `href="/tables"`) {
		t.Fatalf("precondition: /tables hidden, got: %s", body)
	}
	i18n, err := config.NewI18n("web/locales", "en")
	if err != nil {
		t.Fatal(err)
	}
	// What /api/sync/settings/apply or an admin pull writes: the row only.
	if err := dp.Settings.Set(t.Context(), common.MenuRestoredKeysSetting, `["/tables"]`); err != nil {
		t.Fatal(err)
	}
	newRederiveSettings(dp, true, i18n)(t.Context())
	if body := getMenu(t, mux); !strings.Contains(body, `href="/tables"`) {
		t.Fatalf("re-derive must bring the restored /tables back on the Menu, got: %s", body)
	}
}

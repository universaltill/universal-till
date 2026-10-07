package pages

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#3308: a rename to a name another till in the shop already uses is
// refused where it is made — Settings on the main till and on a joined till,
// and the cloud's rename_till — with the same case-insensitive fold as the
// pairing-time check (TillsRepo.NameTaken, ut-docs#1264).

// joinedTillWithRoster makes dp a joined till named "Back Office" whose
// synced tills roster holds its own row plus a sibling "Terrace", under a
// main till named "Main Counter".
func joinedTillWithRoster(t *testing.T, dp *common.Deps) {
	t.Helper()
	ctx := t.Context()
	repo := data.NewTillsRepo(dp.Db)
	self, err := repo.InsertTill(ctx, "Back Office", "hash-self", data.TillRoleAdditional)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.InsertTill(ctx, "Terrace", "hash-terrace", data.TillRoleAdditional); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		"sync.primary_url": "http://primary.invalid",
		"till.name":        "Main Counter",
		"sync.till_name":   "Back Office",
		"sync.till_id":     self,
	} {
		if err := dp.Settings.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTillNameEndpoint_MainTillRefusesASiblingsName(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	ctx := t.Context()
	if err := d.Settings.Set(ctx, "till.name", "Front"); err != nil {
		t.Fatal(err)
	}
	if _, err := data.NewTillsRepo(d.Db).InsertTill(ctx, "Terrace", "hash-terrace", data.TillRoleAdditional); err != nil {
		t.Fatal(err)
	}

	rec := postForm(mux, "/api/settings/till-name", url.Values{"name": {"  terrace "}}, &mgrUser)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("taken name = %d body=%s, want 422", rec.Code, rec.Body.String())
	}
	if want := httpx.T("en", "sync.error.name_taken"); !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("body = %q, want the translated %q", rec.Body.String(), want)
	}
	if got := settingValue(t, d, "till.name"); got != "Front" {
		t.Fatalf("till.name = %q, want it unchanged", got)
	}
	if audits := renameTillAudits(t, d); len(audits) != 0 {
		t.Fatalf("a refused rename was audited: %+v", audits)
	}

	// Refused before the elevation gate (ut-docs#557 convention): a name
	// that would be refused anyway must not burn an approver's PIN entry.
	if rec := postForm(mux, "/api/settings/till-name", url.Values{"name": {"Terrace"}}, &cashUser); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("cashier with a taken name = %d, want 422 before the elevation prompt", rec.Code)
	}

	// A free name, and a case change of the till's own name, still save.
	for _, name := range []string{"FRONT", "Patio"} {
		if rec := postForm(mux, "/api/settings/till-name", url.Values{"name": {name}}, &mgrUser); rec.Code != http.StatusNoContent {
			t.Fatalf("rename to %q = %d body=%s, want 204", name, rec.Code, rec.Body.String())
		}
		if got := settingValue(t, d, "till.name"); got != name {
			t.Fatalf("till.name = %q, want %q", got, name)
		}
	}
}

func TestTillNameEndpoint_JoinedTillRefusesTheMainTillsAndASiblingsName(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	joinedTillWithRoster(t, d)

	for _, name := range []string{"main counter", "TERRACE"} {
		rec := postForm(mux, "/api/settings/till-name", url.Values{"name": {name}}, &mgrUser)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("rename to %q = %d body=%s, want 422", name, rec.Code, rec.Body.String())
		}
		if got := settingValue(t, d, "sync.till_name"); got != "Back Office" {
			t.Fatalf("sync.till_name = %q after a refused rename", got)
		}
	}
	// Its own roster row is not "another till": a case change saves, and so
	// does a free name.
	for _, name := range []string{"back office", "Patio"} {
		if rec := postForm(mux, "/api/settings/till-name", url.Values{"name": {name}}, &mgrUser); rec.Code != http.StatusNoContent {
			t.Fatalf("rename to %q = %d body=%s, want 204", name, rec.Code, rec.Body.String())
		}
		if got := settingValue(t, d, "sync.till_name"); got != name {
			t.Fatalf("sync.till_name = %q, want %q", got, name)
		}
	}
}

func TestCloudRenameTill_RefusesANameAnotherTillUses(t *testing.T) {
	t.Run("main till", func(t *testing.T) {
		dp := newCloudSyncTestDeps(t)
		ctx := t.Context()
		if err := dp.Settings.Set(ctx, "till.name", "Front"); err != nil {
			t.Fatal(err)
		}
		if _, err := data.NewTillsRepo(dp.Db).InsertTill(ctx, "Terrace", "hash-terrace", data.TillRoleAdditional); err != nil {
			t.Fatal(err)
		}
		if _, err := cloudRenameTill(ctx, dp, "terrace"); err == nil {
			t.Fatal("cloudRenameTill to a joined till's name succeeded")
		}
		if got := settingValue(t, dp, "till.name"); got != "Front" {
			t.Fatalf("till.name = %q, want it unchanged", got)
		}
		if _, err := cloudRenameTill(ctx, dp, "Patio"); err != nil {
			t.Fatalf("free name refused: %v", err)
		}
	})
	t.Run("joined till", func(t *testing.T) {
		dp := newCloudSyncTestDeps(t)
		ctx := t.Context()
		joinedTillWithRoster(t, dp)
		for _, name := range []string{"Main Counter", "terrace"} {
			if _, err := cloudRenameTill(ctx, dp, name); err == nil {
				t.Fatalf("cloudRenameTill to %q succeeded", name)
			}
		}
		if got := settingValue(t, dp, "sync.till_name"); got != "Back Office" {
			t.Fatalf("sync.till_name = %q, want it unchanged", got)
		}
		if audits := renameTillAudits(t, dp); len(audits) != 0 {
			t.Fatalf("a refused rename was audited: %+v", audits)
		}
		if _, err := cloudRenameTill(ctx, dp, "BACK OFFICE"); err != nil {
			t.Fatalf("case change of its own name refused: %v", err)
		}
	})
}

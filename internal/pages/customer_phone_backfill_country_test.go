package pages

import (
	"net/url"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#3992: a store.country change after boot re-normalises
// customers.phone_e164 without a restart.

func customerE164(t *testing.T, d *common.Deps, id string) string {
	t.Helper()
	var v *string
	if err := d.Db.QueryRow(`SELECT phone_e164 FROM customers WHERE id = ?`, id).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v == nil {
		return "<NULL>"
	}
	return *v
}

func seedBootedPhoneBackfill(t *testing.T, d *common.Deps) {
	t.Helper()
	if _, err := d.Db.Exec(`INSERT INTO customers (id, name, phone) VALUES ('c1', 'Ann', '020 7946 0958')`); err != nil {
		t.Fatal(err)
	}
	// The boot back-fill, with the country the shop had then (DE).
	if err := d.Settings.Set(t.Context(), common.KeyCountry, "DE"); err != nil {
		t.Fatal(err)
	}
	d.UpdateState(func(s *common.RuntimeState) { s.Country = "DE" })
	if _, err := data.NewPOSRepo(d.Db).BackfillCustomerPhoneE164(t.Context(), 10); err != nil {
		t.Fatal(err)
	}
	if got := customerE164(t, d, "c1"); got != "+492079460958" {
		t.Fatalf("boot back-fill under DE: %q", got)
	}
}

func TestSaveCountryChange_RebackfillsCustomerPhoneE164(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	seedBootedPhoneBackfill(t, d)
	rec := postForm(mux, "/api/settings/save", url.Values{"country": {"GB"}}, &mgrUser)
	if rec.Code != 204 {
		t.Fatalf("save = %d body=%s", rec.Code, rec.Body.String())
	}
	d.WaitForAsyncWork()
	if got := customerE164(t, d, "c1"); got != "+442079460958" {
		t.Errorf("phone_e164 after DE -> GB = %q, want +442079460958", got)
	}
}

func TestUpsertCountryChange_RebackfillsCustomerPhoneE164(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	seedBootedPhoneBackfill(t, d)
	rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {"store.country"}, "value": {"GB"}}, &mgrUser)
	if rec.Code != 204 {
		t.Fatalf("upsert = %d body=%s", rec.Code, rec.Body.String())
	}
	d.WaitForAsyncWork()
	if got := customerE164(t, d, "c1"); got != "+442079460958" {
		t.Errorf("phone_e164 after DE -> GB = %q, want +442079460958", got)
	}
}

func TestCloudSetSettingCountryChange_RebackfillsCustomerPhoneE164(t *testing.T) {
	_, _, d := newFullAuthDeps(t)
	seedBootedPhoneBackfill(t, d)
	if _, err := buildCloudHooks(d, nil).SetSetting(t.Context(), common.KeyCountry, "GB"); err != nil {
		t.Fatal(err)
	}
	d.WaitForAsyncWork()
	if got := customerE164(t, d, "c1"); got != "+442079460958" {
		t.Errorf("phone_e164 after DE -> GB = %q, want +442079460958", got)
	}
}

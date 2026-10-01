package data

// ADR-0124 (ut-docs#3169): country_settings.shadow_customer_documents is a
// shipped market fact, not an operator setting. These tests pin the four
// repository rules: the seed, Upsert never taking it from the caller (on
// insert or update), Delete restoring a builtin row's value, and the
// compiled floor helper.

import (
	"context"
	"testing"
)

func shadowValue(t *testing.T, repo *CountrySettingsRepo, code string) (string, bool) {
	t.Helper()
	v, found, err := repo.ShadowCustomerDocuments(context.Background(), code)
	if err != nil {
		t.Fatalf("ShadowCustomerDocuments(%s): %v", code, err)
	}
	return v, found
}

func TestShadowCustomerDocuments_SeededValues(t *testing.T) {
	_, repo := openCountryTestDB(t)
	for _, c := range BuiltinCountryDefaults() {
		v, found := shadowValue(t, repo, c.Code)
		if !found {
			t.Fatalf("%s: no row", c.Code)
		}
		want := "allowed"
		if c.Code == "PT" {
			want = "forbidden"
		}
		if v != want {
			t.Errorf("%s: shadow_customer_documents = %q, want %q", c.Code, v, want)
		}
	}
	if _, found := shadowValue(t, repo, "ZZ"); found {
		t.Fatal("ZZ: found a row for a code nobody created")
	}
	// The read normalises the code, like Get.
	if v, found := shadowValue(t, repo, " pt "); !found || v != "forbidden" {
		t.Fatalf("\" pt \": got %q, %v; want forbidden, true", v, found)
	}
	// And List/Get scan the column into the struct.
	got, ok, err := repo.Get(context.Background(), "PT")
	if err != nil || !ok || got.ShadowCustomerDocuments != "forbidden" {
		t.Fatalf("Get(PT).ShadowCustomerDocuments = %q (ok=%v err=%v), want forbidden", got.ShadowCustomerDocuments, ok, err)
	}
}

func TestShadowCustomerDocuments_UpsertNeverTakesItFromTheCaller(t *testing.T) {
	dbo, repo := openCountryTestDB(t)
	ctx := context.Background()

	// Update of a forbidden builtin: the caller's "allowed" is ignored.
	pt, _, err := repo.Get(ctx, "PT")
	if err != nil {
		t.Fatal(err)
	}
	pt.ShadowCustomerDocuments = "allowed"
	pt.TaxRateBP = 2200 // a real edit, which must still land
	if err := repo.Upsert(ctx, pt); err != nil {
		t.Fatalf("upsert PT: %v", err)
	}
	if v, _ := shadowValue(t, repo, "PT"); v != "forbidden" {
		t.Fatalf("PT after Upsert(allowed) = %q, want forbidden", v)
	}
	if got, _, _ := repo.Get(ctx, "PT"); got.TaxRateBP != 2200 {
		t.Fatalf("PT tax edit lost: %d", got.TaxRateBP)
	}

	// Update of an allowed builtin: the caller's "forbidden" is ignored too
	// — the column is not an operator setting in either direction.
	gb, _, _ := repo.Get(ctx, "GB")
	gb.ShadowCustomerDocuments = "forbidden"
	if err := repo.Upsert(ctx, gb); err != nil {
		t.Fatalf("upsert GB: %v", err)
	}
	if v, _ := shadowValue(t, repo, "GB"); v != "allowed" {
		t.Fatalf("GB after Upsert(forbidden) = %q, want allowed", v)
	}

	// Update keeps the STORED value, whatever it is: a row an older sync
	// marked forbidden stays forbidden through an operator save.
	if _, err := dbo.Exec(`UPDATE country_settings SET shadow_customer_documents = 'forbidden' WHERE code = 'FR'`); err != nil {
		t.Fatal(err)
	}
	fr, _, _ := repo.Get(ctx, "FR")
	fr.ShadowCustomerDocuments = "allowed"
	if err := repo.Upsert(ctx, fr); err != nil {
		t.Fatal(err)
	}
	if v, _ := shadowValue(t, repo, "FR"); v != "forbidden" {
		t.Fatalf("FR stored forbidden overwritten by Upsert: %q", v)
	}

	// Insert of a custom code: always allowed, whatever the caller says.
	custom := CountrySetting{Code: "zz", Currency: "ZZZ", TaxRateBP: 500, TaxInclusive: true,
		ArchiveMinDays: GlobalArchiveMinDays, ShadowCustomerDocuments: "forbidden"}
	if err := repo.Upsert(ctx, custom); err != nil {
		t.Fatalf("insert ZZ: %v", err)
	}
	if v, _ := shadowValue(t, repo, "ZZ"); v != "allowed" {
		t.Fatalf("custom ZZ inserted as %q, want allowed", v)
	}

	// Insert of a builtin code whose row was pruned: the builtin value.
	if _, err := dbo.Exec(`DELETE FROM country_settings WHERE code = 'PT'`); err != nil {
		t.Fatal(err)
	}
	pt.ShadowCustomerDocuments = "allowed"
	if err := repo.Upsert(ctx, pt); err != nil {
		t.Fatalf("re-insert PT: %v", err)
	}
	if v, _ := shadowValue(t, repo, "PT"); v != "forbidden" {
		t.Fatalf("PT re-inserted as %q, want the builtin forbidden", v)
	}
}

func TestShadowCustomerDocuments_DeleteRestoresTheBuiltinValue(t *testing.T) {
	dbo, repo := openCountryTestDB(t)
	ctx := context.Background()
	// A row that drifted to allowed (an older version's re-insert, a raw
	// sync write) — Delete must put the shipped value back, which Upsert
	// alone would not, since its update keeps the stored value.
	if _, err := dbo.Exec(`UPDATE country_settings SET shadow_customer_documents = 'allowed' WHERE code = 'PT'`); err != nil {
		t.Fatal(err)
	}
	if err := repo.Delete(ctx, "pt"); err != nil {
		t.Fatalf("delete PT: %v", err)
	}
	if v, _ := shadowValue(t, repo, "PT"); v != "forbidden" {
		t.Fatalf("PT after Delete = %q, want forbidden", v)
	}
	// And the other direction for an allowed builtin.
	if _, err := dbo.Exec(`UPDATE country_settings SET shadow_customer_documents = 'forbidden' WHERE code = 'GB'`); err != nil {
		t.Fatal(err)
	}
	if err := repo.Delete(ctx, "GB"); err != nil {
		t.Fatalf("delete GB: %v", err)
	}
	if v, _ := shadowValue(t, repo, "GB"); v != "allowed" {
		t.Fatalf("GB after Delete = %q, want allowed", v)
	}
}

func TestShadowCustomerDocuments_ColumnRejectsUnknownValues(t *testing.T) {
	dbo, _ := openCountryTestDB(t)
	if _, err := dbo.Exec(`UPDATE country_settings SET shadow_customer_documents = 'maybe' WHERE code = 'GB'`); err == nil {
		t.Fatal("the CHECK constraint accepted 'maybe'")
	}
}

func TestBuiltinShadowDocumentsForbidden(t *testing.T) {
	cases := map[string]bool{
		"PT": true, " pt ": true, "pt": true,
		"GB": false, "DE": false, "OTHER": false, "ZZ": false, "": false,
	}
	for code, want := range cases {
		if got := BuiltinShadowDocumentsForbidden(code); got != want {
			t.Errorf("BuiltinShadowDocumentsForbidden(%q) = %v, want %v", code, got, want)
		}
	}
}

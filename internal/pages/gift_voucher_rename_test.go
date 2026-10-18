package pages

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#4398: a shop whose language pack wasn't live at setup got a gift
// voucher item named "Gift voucher"; when the pack's strings reach the
// translator, the untouched seeded item takes the shop's word for it.

const giftVoucherDe = "Geschenkgutschein"

var dePackLocales = config.PluginLocales{FirstPartyStale: map[string]map[string]string{
	"de": {"catalog.item_type.gift_voucher": giftVoucherDe},
}}

// newRenameTestDB wires an English-only translator for a German shop and
// seeds the gift voucher item as the wizard does before the pack arrives.
func newRenameTestDB(t *testing.T) (*sql.DB, *config.I18n, string) {
	t.Helper()
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "de")
	if err != nil {
		t.Fatal(err)
	}
	httpx.InitI18n(i18n, "de")
	seedDefaultGiftVoucherItem(t.Context(), &common.Deps{Db: db}, "de")
	id, ok, err := data.NewCatalogRepo(db).SeededGiftVoucherItemID(t.Context())
	if err != nil || !ok {
		t.Fatalf("seeded id: ok=%v err=%v", ok, err)
	}
	if got := itemName(t, db, id); got != "Gift voucher" {
		t.Fatalf("seeded before the pack = %q, want the English fallback", got)
	}
	return db, i18n, id
}

func itemName(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var name string
	if err := db.QueryRow(`SELECT name FROM items WHERE id = ?`, id).Scan(&name); err != nil {
		t.Fatalf("load item %s: %v", id, err)
	}
	return name
}

func TestSeededGiftVoucherRenamer_RenamesWhenThePackArrives(t *testing.T) {
	db, i18n, id := newRenameTestDB(t)

	seededGiftVoucherRenamer{Localizer: i18n, db: db}.SetPluginLocales(dePackLocales)

	if got := httpx.T("de", "catalog.item_type.gift_voucher"); got != giftVoucherDe {
		t.Fatalf("pack strings not forwarded to the translator: T = %q", got)
	}
	if got := itemName(t, db, id); got != giftVoucherDe {
		t.Fatalf("seeded item = %q after the pack arrived, want %q", got, giftVoucherDe)
	}
}

func TestSeededGiftVoucherRenamer_KeepsTheNameWithoutThePack(t *testing.T) {
	db, i18n, id := newRenameTestDB(t)

	seededGiftVoucherRenamer{Localizer: i18n, db: db}.SetPluginLocales(config.PluginLocales{})

	if got := itemName(t, db, id); got != "Gift voucher" {
		t.Fatalf("seeded item = %q with no pack, want it kept", got)
	}
}

// An additional till's catalogue is its main till's: it never renames
// locally, the rename reaches it from the main till.
func TestSeededGiftVoucherRenamer_AdditionalTillLeavesItToTheMainTill(t *testing.T) {
	db, i18n, id := newRenameTestDB(t)
	if _, err := db.Exec(`INSERT INTO settings (key, value) VALUES ('sync.primary_url', 'https://main-till.local:8443')`); err != nil {
		t.Fatal(err)
	}

	seededGiftVoucherRenamer{Localizer: i18n, db: db}.SetPluginLocales(dePackLocales)

	if got := itemName(t, db, id); got != "Gift voucher" {
		t.Fatalf("additional till renamed the seeded item to %q", got)
	}
}

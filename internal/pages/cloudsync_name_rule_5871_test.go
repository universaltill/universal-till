package pages

import (
	"errors"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#5871: the rename_item, save_item and create_item directives refuse
// a name with control or invisible characters; nothing is written or audited.

func itemNameAndAudit(t *testing.T, dp *common.Deps, id, action string) (string, int) {
	t.Helper()
	var name string
	if err := dp.Db.QueryRowContext(t.Context(), `SELECT name FROM items WHERE id = ?`, id).Scan(&name); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := dp.Db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM audit_log WHERE entity_id = ? AND action = ?`, id, action).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return name, n
}

func TestCloudRenameItem_RefusesBadName(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	before, _ := itemNameAndAudit(t, dp, "itm1", "cloud_item_renamed")

	_, err := cloudRenameItem(ctx, dp, "itm1", "Ap\u202eple")
	if !errors.Is(err, data.ErrNameInvalidCharacters) {
		t.Fatalf("err = %v, want ErrNameInvalidCharacters", err)
	}
	if name, audits := itemNameAndAudit(t, dp, "itm1", "cloud_item_renamed"); name != before || audits != 0 {
		t.Fatalf("name %q audits %d after a refused rename, want %q and none", name, audits, before)
	}

	msg, err := cloudRenameItem(ctx, dp, "itm1", "  Green Apple  ")
	if err != nil {
		t.Fatal(err)
	}
	if name, audits := itemNameAndAudit(t, dp, "itm1", "cloud_item_renamed"); name != "Green Apple" || audits != 1 || msg != "renamed to Green Apple" {
		t.Fatalf("name %q audits %d msg %q, want the trimmed name", name, audits, msg)
	}
}

func TestCloudSaveItem_RefusesBadName(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	before, _ := itemNameAndAudit(t, dp, "itm1", "cloud_item_saved")
	bad := "Ap\u202eple"
	if _, err := cloudSaveItem(t.Context(), dp, data.ItemPatch{ID: "itm1", Name: &bad}); !errors.Is(err, data.ErrNameInvalidCharacters) {
		t.Fatalf("err = %v, want ErrNameInvalidCharacters", err)
	}
	if name, _ := itemNameAndAudit(t, dp, "itm1", "cloud_item_saved"); name != before {
		t.Fatalf("name = %q after a refused save, want %q", name, before)
	}
	var audits int
	if err := dp.Db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM audit_log WHERE entity_id = 'itm1' AND action LIKE 'cloud_%'`).Scan(&audits); err != nil || audits != 0 {
		t.Fatalf("audit rows = %d, %v; want none", audits, err)
	}
	good := "Green Apple"
	if _, err := cloudSaveItem(t.Context(), dp, data.ItemPatch{ID: "itm1", Name: &good}); err != nil {
		t.Fatal(err)
	}
	if name, _ := itemNameAndAudit(t, dp, "itm1", "x"); name != good {
		t.Fatalf("name = %q, want %q", name, good)
	}
}

func TestCloudCreateItem_RefusesBadName(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	if _, err := cloudCreateItem(t.Context(), dp, "Ba\u202ed Widget", 500, ""); !errors.Is(err, data.ErrNameInvalidCharacters) {
		t.Fatalf("err = %v, want ErrNameInvalidCharacters", err)
	}
	var n int
	if err := dp.Db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM items WHERE name LIKE '%Widget%'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("items = %d, %v; want none written", n, err)
	}
}

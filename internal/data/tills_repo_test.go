package data

import (
	"context"
	"testing"
)

// ut-docs#3294: a joined till's rename reaches the main till's tills row.
// UpdateName writes only on a real change, so an unchanged name (every
// periodic link report repeats it) never bumps sync_admin_version and never
// makes every joined till re-pull the admin bundle.
func TestTillsRepo_UpdateNameChangesOnlyOnARealChange(t *testing.T) {
	d := openMigratedDB(t, "tills-update-name.db")
	repo := NewTillsRepo(d.DB)
	ctx := context.Background()
	id, err := repo.InsertTill(ctx, "Till 2", "hash-2")
	if err != nil {
		t.Fatal(err)
	}
	generation := func() int64 {
		t.Helper()
		var g int64
		if err := d.QueryRow(`SELECT generation FROM sync_admin_version WHERE id = 1`).Scan(&g); err != nil {
			t.Fatal(err)
		}
		return g
	}
	nameOf := func() string {
		t.Helper()
		rows, err := repo.ListTills(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if r.ID == id {
				return r.Name
			}
		}
		t.Fatalf("till %s not listed", id)
		return ""
	}

	before := generation()
	changed, err := repo.UpdateName(ctx, id, "Bar till")
	if err != nil || !changed {
		t.Fatalf("UpdateName = %v, %v; want changed", changed, err)
	}
	if got := nameOf(); got != "Bar till" {
		t.Fatalf("name = %q, want Bar till", got)
	}
	afterRename := generation()
	if afterRename == before {
		t.Fatal("a rename must bump sync_admin_version so joined tills pull the new roster")
	}

	changed, err = repo.UpdateName(ctx, id, "Bar till")
	if err != nil || changed {
		t.Fatalf("same-name UpdateName = %v, %v; want no change", changed, err)
	}
	if generation() != afterRename {
		t.Fatal("an unchanged name must not bump sync_admin_version")
	}

	changed, err = repo.UpdateName(ctx, "no-such-till", "Ghost")
	if err != nil || changed {
		t.Fatalf("unknown till UpdateName = %v, %v; want no change, no error", changed, err)
	}
}

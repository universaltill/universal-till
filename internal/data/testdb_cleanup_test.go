package data

import "testing"

// ut-docs#2180: same defect shape as testsupport.NewCatalogTestDB (fixed
// separately) — found by the card's own "grep for other instances" step.
// Each of these package-local test-DB helpers opened its *sql.DB with no
// t.Cleanup/Close anywhere in the helper, and every one of their call
// sites in this package relied on that never being forgotten (none of them
// actually closed it). Assert each helper closes its own DB once the
// (sub)test that opened it finishes.

func TestNewAuditTestDB_ClosesOnCleanup(t *testing.T) {
	var db interface{ Ping() error }
	t.Run("inner", func(t *testing.T) {
		db = newAuditTestDB(t)
		if err := db.Ping(); err != nil {
			t.Fatalf("expected a usable db inside the subtest, got: %v", err)
		}
	})
	if err := db.Ping(); err == nil {
		t.Fatal("expected newAuditTestDB's db to be closed once its test finished, but Ping still succeeded")
	}
}

func TestNewFiscalChipTestDB_ClosesOnCleanup(t *testing.T) {
	var db interface{ Ping() error }
	t.Run("inner", func(t *testing.T) {
		db = newFiscalChipTestDB(t)
		if err := db.Ping(); err != nil {
			t.Fatalf("expected a usable db inside the subtest, got: %v", err)
		}
	})
	if err := db.Ping(); err == nil {
		t.Fatal("expected newFiscalChipTestDB's db to be closed once its test finished, but Ping still succeeded")
	}
}

func TestNewPluginRepoTestDB_ClosesOnCleanup(t *testing.T) {
	var db interface{ Ping() error }
	t.Run("inner", func(t *testing.T) {
		db = newPluginRepoTestDB(t)
		if err := db.Ping(); err != nil {
			t.Fatalf("expected a usable db inside the subtest, got: %v", err)
		}
	})
	if err := db.Ping(); err == nil {
		t.Fatal("expected newPluginRepoTestDB's db to be closed once its test finished, but Ping still succeeded")
	}
}

func TestNewTranslationTestDB_ClosesOnCleanup(t *testing.T) {
	var db interface{ Ping() error }
	t.Run("inner", func(t *testing.T) {
		db = newTranslationTestDB(t)
		if err := db.Ping(); err != nil {
			t.Fatalf("expected a usable db inside the subtest, got: %v", err)
		}
	})
	if err := db.Ping(); err == nil {
		t.Fatal("expected newTranslationTestDB's db to be closed once its test finished, but Ping still succeeded")
	}
}

package testsupport

import "testing"

// ut-docs#2180: NewCatalogTestDB opened its *sql.DB with no t.Cleanup/Close
// anywhere in the file, so every one of its 26+ call sites that didn't
// remember its own `defer db.Close()` leaked the handle (and its background
// connectionOpener goroutine) forever. This asserts the helper closes its
// own DB once the (sub)test that opened it finishes, regardless of whether
// the caller ever closes it themselves.
func TestNewCatalogTestDB_ClosesOnCleanup(t *testing.T) {
	var db interface{ Ping() error }
	t.Run("inner", func(t *testing.T) {
		db = NewCatalogTestDB(t)
		if err := db.Ping(); err != nil {
			t.Fatalf("expected a usable db inside the subtest, got: %v", err)
		}
	})
	if err := db.Ping(); err == nil {
		t.Fatal("expected NewCatalogTestDB's db to be closed once its test finished, but Ping still succeeded")
	}
}

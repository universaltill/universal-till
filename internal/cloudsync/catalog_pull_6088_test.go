package cloudsync

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/entitlement"
)

// ut-docs#6088: the catalogue upload is told the field lists the cloud's last
// sync answer advertised; with none cached it gets "" (contract 1.0.0).
func TestCatalogPullOptionsCarryTheAdvertisedFields(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "till.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()
	if got := catalogPullOptions(ctx, d.DB, Hooks{}).AcceptedFields; got != "" {
		t.Fatalf("AcceptedFields with nothing cached = %q, want empty (1.0.0)", got)
	}
	const adv = `{"item":["is_weighed","name"]}`
	if err := data.NewSettingsRepo(d.DB).Set(ctx, entitlement.KeyCatalogChangesFields, adv); err != nil {
		t.Fatal(err)
	}
	if got := catalogPullOptions(ctx, d.DB, Hooks{}).AcceptedFields; got != adv {
		t.Fatalf("AcceptedFields = %q, want the cached %q", got, adv)
	}
}

// Command seed_demo restores the demo ("sample data") catalogue into a
// throwaway e2e till. Since ut-docs#539 the catalogue is opt-in (migration
// 036 removes it; the setup wizard checkbox re-seeds it), so the e2e specs
// that scan demo barcodes (5000000000012 etc.) no longer get it from the
// migrations for free — this seeds exactly what the wizard's opt-in path
// seeds (internal/data/seeddata, the single source of truth).
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
)

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(2)
}

func main() {
	dataDir := os.Getenv("UT_DATA_DIR")
	if dataDir == "" {
		fatalf("UT_DATA_DIR must be set")
	}
	conn, err := db.Open(filepath.Join(dataDir, "unitill-pos.db"))
	if err != nil {
		fatalf("open db: %v", err)
	}
	defer conn.Close()

	if err := data.NewDemoSeedRepo(conn.DB).SeedDemoCatalogue(context.Background()); err != nil {
		fatalf("seed demo catalogue: %v", err)
	}

	// ut-docs#970: run-till.sh's till never drives the setup wizard
	// (UT_AUTH=off, specs go straight to authenticated pages) or Settings,
	// so it would otherwise sit permanently "unconfirmed" and every spec
	// that commits a catalog import (e.g. catalog-import-friendly-errors)
	// would hit the new currency-confirmation gate instead of the flow it's
	// actually testing. This till is the e2e equivalent of an
	// already-trading shop with an established currency — just seeded
	// programmatically rather than through the wizard, same reasoning as
	// migration 063's setup.completed-based backfill for real installs.
	if err := data.NewSettingsRepo(conn.DB).Set(context.Background(), common.KeyCurrencyConfirmed, "true"); err != nil {
		fatalf("mark currency confirmed: %v", err)
	}
	fmt.Println("demo catalogue seeded")
}

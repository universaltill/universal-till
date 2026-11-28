// Command seed_service_shop marks a throwaway e2e till as a service shop that
// predates shop-type templates: shop.type=service and no apply-once ledger
// (ADR-0190, ut-docs#6332). Such a shop is in legacy mode and keeps its old
// barber layout (no Tables, no Kitchen stations, Items shown as Services),
// which template-menu-service-legacy-6332.spec.ts drives through the real
// render path and the Settings > Hidden menu tiles restore surface.
//
// It replaces the earlier seeder that installed the retired layout plugin:
// the layout now comes from the service template's legacy_layout, so the only
// state to seed is the shop type itself. "shop.type" is common.KeyShopType's
// value; internal/pages/common is not imported to keep this seeder's
// dependency footprint matching seed_demo's.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/universaltill/universal-till/internal/db"
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
	if _, err := conn.Exec(`INSERT OR REPLACE INTO settings (key, value) VALUES ('shop.type', 'service')`); err != nil {
		fatalf("seed shop.type: %v", err)
	}
	fmt.Println("Seeded shop.type=service (legacy mode, no template ledger) for e2e")
}

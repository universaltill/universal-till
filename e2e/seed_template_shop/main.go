// Command seed_template_shop prepares a throwaway e2e till for the shop-type
// template specs (ADR-0190, ut-docs#6332, template-*-6332.spec.ts).
//
// It writes only what a real shop would already hold, and nothing else:
//
//	UT_E2E_SHOP_TYPE   shop.type (e.g. retail). With NO template ledger this is
//	                   a shop that predates templates: legacy mode, which is
//	                   what the existing-shop offer card is for.
//	UT_E2E_PRIMARY_URL sync.primary_url. Set = this till is an additional
//	                   ("satellite") till that follows a main till, so the
//	                   shop type is locked here. The URL is never contacted by
//	                   the specs; point it at a closed local port.
//
// Both are optional. shop.type and sync.primary_url are the values of
// common.KeyShopType and the key Deps.SyncPrimaryURL reads; internal/pages is
// not imported so the footprint matches seed_demo's.
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
	for key, env := range map[string]string{"shop.type": "UT_E2E_SHOP_TYPE", "sync.primary_url": "UT_E2E_PRIMARY_URL"} {
		v := os.Getenv(env)
		if v == "" {
			continue
		}
		if _, err := conn.Exec(`INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)`, key, v); err != nil {
			fatalf("seed %s: %v", key, err)
		}
		fmt.Printf("Seeded %s=%s for e2e\n", key, v)
	}
}

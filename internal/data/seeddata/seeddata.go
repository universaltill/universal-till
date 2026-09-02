// Package seeddata is the single source of truth for the demo ("sample
// data") grocery catalogue (ut-docs#539) and its companion demo customers
// + promo codes (ut-docs#567).
//
// Before the ADR-0074 migration squash (ut-docs#1425), the catalogue was
// seeded unconditionally by 001_init.sql until migration 036 made it
// opt-in; the demo customers/promo codes went the same way at migration
// 038 (ut-docs#567). Both migrations are gone — 001_init.sql now carries
// no demo rows at all (opt-in from the very first boot) — but the runtime
// behaviour they introduced is unchanged: opt-in via the setup wizard
// checkbox (data.DemoSeedRepo.SeedDemoCatalogue /
// SeedDemoCustomersPromos) and removable via Settings → "Remove sample
// data". Tests in this package and in internal/db guard that the ID lists
// and the seed rows never drift apart from each other.
package seeddata

import (
	_ "embed"
	"fmt"
)

// DemoCatalogueSQL (re)inserts the full demo catalogue: categories, brands,
// items (flagged is_sample_data = 1), barcodes, images, variants, variant
// barcodes, inventory, price history and shortcut buttons. INSERT OR IGNORE
// throughout, so it is idempotent and never fails on an operator's clashing
// row. Requires the structural defaults (tax codes, stock locations) that
// every till has from 001_init.sql.
//
//go:embed demo_catalogue.sql
var DemoCatalogueSQL string

// DemoIDsSQL loads the demo catalogue's item/category/brand IDs into
// per-connection TEMP tables (demo_seed_items / demo_seed_categories /
// demo_seed_brands) for RemoveDemoSQL to target precisely.
//
//go:embed demo_ids.sql
var DemoIDsSQL string

// RemoveDemoSQL deletes every UNTOUCHED demo row (see the file's header for
// the exact safety rule) and drops the TEMP ID tables again. Must run on the
// same connection as DemoIDsSQL, after it.
//
//go:embed remove_demo.sql
var RemoveDemoSQL string

// DemoCustomersPromosSQL (re)inserts the 3 demo customers + 3 demo promo
// codes (ut-docs#567), every row flagged is_sample_data = 1. The opt-in
// companion to DemoCatalogueSQL — same idempotent INSERT OR IGNORE shape.
//
//go:embed demo_customers_promos.sql
var DemoCustomersPromosSQL string

// DemoCustomersPromosIDsSQL loads the demo customer IDs / promo codes into
// per-connection TEMP tables (demo_seed_customers / demo_seed_promos) for
// RemoveDemoCustomersPromosSQL to target precisely.
//
//go:embed demo_customers_promos_ids.sql
var DemoCustomersPromosIDsSQL string

// RemoveDemoCustomersPromosSQL deletes every UNTOUCHED demo customer/promo
// row (see the file's header for the safety rule — different from the
// catalogue's, since this schema can't track promo-code redemption) and
// drops the TEMP ID tables again. Must run on the same connection as
// DemoCustomersPromosIDsSQL, after it.
//
//go:embed remove_demo_customers_promos.sql
var RemoveDemoCustomersPromosSQL string

// ItemIDs / CategoryIDs / BrandIDs / VariantIDs mirror the SQL assets for
// Go-side callers and cross-checking tests.
var (
	ItemIDs = idRange("itm%03d", 50)

	CategoryIDs = []string{
		"cat_bakery", "cat_clean", "cat_dairy", "cat_drink", "cat_food",
		"cat_frozen", "cat_house", "cat_personal", "cat_produce", "cat_snack",
	}

	BrandIDs = []string{
		"br_coca", "br_generic", "br_heinz", "br_kell",
		"br_nestle", "br_pepsi", "br_unilev", "br_walk",
	}

	VariantIDs = idRange("var%03d", 12)

	// DemoCustomerIDs / DemoPromoCodes (ut-docs#567) mirror
	// demo_customers_promos_ids.sql for Go-side callers and cross-checking
	// tests.
	DemoCustomerIDs = []string{"cust-001", "cust-002", "cust-003"}
	DemoPromoCodes  = []string{"PROMO50", "PROMO500", "DISC10"}
)

func idRange(format string, n int) []string {
	ids := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		ids = append(ids, fmt.Sprintf(format, i))
	}
	return ids
}

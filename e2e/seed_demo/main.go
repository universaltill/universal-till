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
	fmt.Println("demo catalogue seeded")
}

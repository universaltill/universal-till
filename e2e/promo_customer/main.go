// Command promo_customer makes sure one generic test customer exists in a
// RUNNING e2e till's database and prints its id (ut-docs#3661,
// promotions-record-dialog-3661.spec.ts). The e2e till is seeded with no
// customers, so without this every customer-bound promotion check in that
// spec was skipped. Idempotent: the customer is found again by name and
// postcode, so repeated calls print the same id. Writes through
// data.CustomerImportRepo, the repository the customer import uses. Test
// support only, like e2e/category_picture.
//
//	UT_DATA_DIR=<dir> go run ./e2e/promo_customer
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/paths"
)

const (
	customerName     = "E2E Promo Customer"
	customerPostcode = "E2E 3661"
)

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(2)
}

func main() {
	dataDir := os.Getenv("UT_DATA_DIR")
	if dataDir == "" {
		fatalf("usage: UT_DATA_DIR=<dir> promo_customer")
	}
	paths.Init(dataDir)
	conn, err := db.Open(filepath.Join(dataDir, "unitill-pos.db"))
	if err != nil {
		fatalf("open db: %v", err)
	}
	defer conn.Close()
	ctx := context.Background()
	repo := data.NewCustomerImportRepo()
	ids, err := repo.FindByNamePostcode(ctx, conn.DB, customerName, customerPostcode)
	if err != nil {
		fatalf("find customer: %v", err)
	}
	if len(ids) > 0 {
		fmt.Println(ids[0])
		return
	}
	id, err := repo.Create(ctx, conn.DB, data.CustomerFields{Name: customerName, Postcode: customerPostcode}, "")
	if err != nil {
		fatalf("create customer: %v", err)
	}
	fmt.Println(id)
}

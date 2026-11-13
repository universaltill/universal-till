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
//	UT_DATA_DIR=<dir> go run ./e2e/promo_customer -name "E2E Promo Other"
//	UT_DATA_DIR=<dir> go run ./e2e/promo_customer -shell [-promo CODE]
//
// -name <n> finds-or-creates a customer of that name instead of the default
// (ut-docs#5574: a second customer to change a promotion to). -shell creates
// a fresh customer and retires it in place the way a replica does for a
// customer the primary erased (name ”, contact columns NULL), and prints its
// id: a promotion bound to it must show "Deleted customer". Each -shell call
// makes a new one (a shell has no name left to be found by).
package main

import (
	"context"
	"flag"
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
	name := flag.String("name", customerName, "customer name to find or create")
	shell := flag.Bool("shell", false, "create a customer and retire it as an anonymous shell")
	promo := flag.String("promo", "", "with -shell: also create an active promotion with this code bound to the customer before it is retired")
	flag.Parse()
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
	postcode := customerPostcode
	if *name != customerName {
		postcode = "E2E 5574"
	}
	if *shell {
		id, err := repo.Create(ctx, conn.DB, data.CustomerFields{Name: "E2E Promo Shell", Postcode: "E2E 5574S"}, "")
		if err != nil {
			fatalf("create customer: %v", err)
		}
		if *promo != "" {
			// The server refuses a shell as a create target, so bind it first.
			if _, err := conn.DB.ExecContext(ctx, `INSERT INTO promotions (code, type, value, description, customer_id, is_active) VALUES (?, 'amount', 100, 'Deleted customer promo', ?, 1)`, *promo, id); err != nil {
				fatalf("create promotion: %v", err)
			}
		}
		if _, err := conn.DB.ExecContext(ctx, `UPDATE customers SET name = '', postcode = NULL WHERE id = ?`, id); err != nil {
			fatalf("retire customer: %v", err)
		}
		fmt.Println(id)
		return
	}
	ids, err := repo.FindByNamePostcode(ctx, conn.DB, *name, postcode)
	if err != nil {
		fatalf("find customer: %v", err)
	}
	if len(ids) > 0 {
		fmt.Println(ids[0])
		return
	}
	id, err := repo.Create(ctx, conn.DB, data.CustomerFields{Name: *name, Postcode: postcode}, "")
	if err != nil {
		fatalf("create customer: %v", err)
	}
	fmt.Println(id)
}

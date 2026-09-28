// Command counter_order_seed writes a LEGACY pay-at-counter order into a
// RUNNING e2e till's database, out of band (ut-docs#2703, reopened;
// open-orders-counter-tab-2703.spec.ts): an order in the shape the kiosk
// stored before pay-at-counter orders were parked as held sales -- status
// "open", line names and quantities only, no prices. No till code path
// creates one any more, so the spec cannot make one through the UI.
//
//	UT_DATA_DIR=<dir> go run ./e2e/counter_order_seed "<name>" <qty> ["<name>" <qty> ...]
//
// Prints the order's id and C-number as JSON.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(2)
}

func main() {
	dataDir := os.Getenv("UT_DATA_DIR")
	args := os.Args[1:]
	if dataDir == "" || len(args) == 0 || len(args)%2 != 0 {
		fatalf(`usage: UT_DATA_DIR=<dir> counter_order_seed "<name>" <qty> [...]`)
	}
	var lines []data.KioskCounterOrderLine
	for i := 0; i < len(args); i += 2 {
		qty, err := strconv.ParseFloat(args[i+1], 64)
		if err != nil {
			fatalf("qty %q: %v", args[i+1], err)
		}
		lines = append(lines, data.KioskCounterOrderLine{Name: args[i], Qty: qty})
	}
	conn, err := db.Open(filepath.Join(dataDir, "unitill-pos.db"))
	if err != nil {
		fatalf("open db: %v", err)
	}
	defer conn.Close()
	o, err := data.NewKioskCounterOrdersRepo(conn.DB).Create(context.Background(), data.KioskCounterOrder{OrderType: "", Lines: lines})
	if err != nil {
		fatalf("create counter order: %v", err)
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"id": o.ID, "display_no": o.DisplayNo})
}

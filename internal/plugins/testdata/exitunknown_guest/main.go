//go:build wasip1

// Test guest for ut-docs#6600/#6606. Prints a payment answer carrying a
// decline_code plus provider references, then exits 2 for a ".authorize"
// event (the shape a card plugin uses when the outcome is unknown) and 0 for
// any other event.
package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
	ev := ""
	if len(os.Args) > 1 {
		ev = os.Args[len(os.Args)-1]
	}
	fmt.Println(`{"outcome":"declined","decline_code":"outcome_unknown","transaction_ref":"txn-SECRET-123","ref":"ref-SECRET-456","recent":[{"ref":"ref-SECRET-789"}]}`)
	if strings.HasSuffix(ev, ".authorize") {
		os.Exit(2)
	}
}

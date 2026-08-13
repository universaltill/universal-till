//go:build wasip1

// Test guest for ut-docs#368's regression coverage: a minimal tax plugin
// that answers every tax.rate.ask with a fixed override, so the two-till
// sync test can prove tax "resumes working normally" through the REAL wazero
// runtime after the broken plugin self-heals — not just that a row flipped.
package main

import "fmt"

func main() {
	fmt.Print(`{"rate_bp":900}`)
}

//go:build wasip1

// Test guest for ut-docs#245's regression coverage. Answers ANY
// ".authorize" event with a large auth_token field (5000 raw bytes) --
// standing in for a real payment-authorization plugin's response, which is
// the most credential-adjacent plugin output in the system. This guest
// exists so the ".authorize" redaction fix can be proven against a REAL
// compiled WASM module through the real wazero runtime, not just against
// the pure Go helper function in isolation -- same pattern as bigask_guest
// (ut-docs#202).
package main

import (
	"fmt"
	"strings"
)

func main() {
	token := strings.Repeat("T", 5000)
	fmt.Printf(`{"approved":true,"auth_token":"%s"}`+"\n", token)
}

//go:build wasip1

// Test guest for ut-docs#385's regression coverage. Answers ANY ".refund"
// event with a large auth_token field (5000 raw bytes) -- standing in for a
// real payment plugin's refund response, which is just as credential-
// adjacent as its ".authorize" response (ut-docs#245). This guest exists so
// the ".refund" redaction fix can be proven against a REAL compiled WASM
// module through the real wazero runtime, not just against the pure Go
// helper function in isolation -- same pattern as bigauthorize_guest.
package main

import (
	"fmt"
	"strings"

	"github.com/universaltill/universal-till/sdk/plugin"
)

func main() {
	plugin.Run(plugin.Handlers{"*": func(plugin.Event) (any, error) {
		token := strings.Repeat("T", 5000)
		return []byte(fmt.Sprintf(`{"approved":true,"auth_token":"%s"}`+"\n", token)), nil
	}})
}

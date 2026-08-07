//go:build wasip1

// Test guest for ut-docs#384's regression coverage. Answers ANY
// ".authorize" event with a large auth_token field NESTED under "provider"
// -- standing in for a real payment-gateway SDK response shape
// ({"approved":true,"provider":{"auth_token":"…"}}), which is exactly the
// shape safeAskResultForLog's top-level-only field check missed before this
// fix. Same pattern as bigauthorize_guest (ut-docs#245), but nested one
// level down so the end-to-end test proves the real HandleEvent call site
// redacts nested fields too, not just the pure Go helper in isolation.
package main

import (
	"fmt"
	"strings"
)

func main() {
	token := strings.Repeat("T", 5000)
	fmt.Printf(`{"approved":true,"provider":{"auth_token":"%s"}}`+"\n", token)
}

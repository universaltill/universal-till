//go:build wasip1

// Test guest for ut-docs#384's regression coverage. Answers ANY
// ".authorize" event with a SMALL auth_token field NESTED under "provider"
// -- standing in for a real payment-gateway SDK response shape
// ({"approved":true,"provider":{"auth_token":"…"}}), which is exactly the
// shape safeAskResultForLog's top-level-only field check missed before this
// fix. Same pattern as bigauthorize_guest (ut-docs#245), but nested one
// level down so the end-to-end test proves the real HandleEvent call site
// redacts nested fields too, not just the pure Go helper in isolation.
//
// The token is deliberately SMALL (well under maxAskFieldBytes), not large
// like the sibling bigauthorize_guest -- independent review of this fix
// (ut-docs#384) found that a large nested token makes the *parent*
// "provider" field's raw JSON exceed maxAskFieldBytes on its own, so the
// pre-existing top-level size check alone would already redact it wholesale
// before recursion is ever relevant. That made the first version of this
// fixture pass against the pre-fix, unpatched code -- a vacuous regression
// test. Keeping the token small forces the test through the actual
// nested-by-NAME redaction path this fix adds, not the size path that
// already existed.
package main

import (
	"fmt"
)

func main() {
	const token = "tok_live_abc123" // small: proves name-based nested redaction, not size-based
	fmt.Printf(`{"approved":true,"provider":{"auth_token":"%s"}}`+"\n", token)
}

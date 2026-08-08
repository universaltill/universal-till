//go:build wasip1

// Test guest for ut-docs#393's regression coverage. Answers ANY
// ".authorize" event with a SMALL auth_token field nested one level down,
// same as bignestedauthorize_guest (ut-docs#384) -- except here "provider"'s
// raw JSON value is itself a JSON *string* whose content is an object
// ({"approved":true,"provider":"{\"auth_token\":\"…\"}"}), not a directly
// nested object. This is exactly the shape a payment-gateway SDK produces
// when it proxies a raw upstream response by stashing it as an
// embedded-JSON string rather than a nested object -- the encoding layer
// safeAskResultForLog's ut-docs#384 recursion didn't handle, because
// json.Unmarshal into a map fails for a string value.
//
// The token is deliberately SMALL, same reasoning as bignestedauthorize_guest:
// a large nested token would push the *parent* "provider" field's raw JSON
// (even as an escaped string) over maxAskFieldBytes, letting the
// pre-existing top-level size check redact it wholesale regardless of
// whether the new embedded-JSON-string path works at all -- a vacuous
// regression test. Keeping it small forces the test through the actual
// ut-docs#393 fix.
package main

import (
	"fmt"
)

func main() {
	const token = "tok_live_abc123" // small: proves the embedded-JSON-string path, not the size path
	fmt.Printf(`{"approved":true,"provider":"{\"auth_token\":\"%s\"}"}`+"\n", token)
}

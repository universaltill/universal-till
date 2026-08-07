package plugins

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestWasmResultLogLine_GatesOnAskSuffix exercises the exact call
// HandleEvent makes (wasmResultLogLine is its only logging call for a
// handler's stdout result) — an earlier draft of this fix only unit-tested
// the redaction helper in isolation, and an independent review found that
// reverting just HandleEvent's call site (while leaving the helper intact)
// left the whole test suite green: the real ut-docs#202 defect could have
// been silently reintroduced. Testing wasmResultLogLine directly, with
// exact-format assertions on both branches, closes that gap.
func TestWasmResultLogLine_GatesOnAskSuffix(t *testing.T) {
	// Non-".ask" events (e.g. sale.completed) must keep the exact original,
	// unredacted format -- explicitly out of scope for ut-docs#202.
	nonAskOut := `{"anything":"` + strings.Repeat("Z", 5000) + `"}`
	if got, want := wasmResultLogLine("com.test.plugin", "sale.completed", nonAskOut),
		`[wasm:com.test.plugin] result: `+nonAskOut; got != want {
		t.Errorf("non-.ask event's log line changed format (want byte-identical to the pre-fix line)")
	}

	// ".ask" events must go through redaction -- if HandleEvent's wiring to
	// this function were ever reverted, this is the assertion that catches it.
	bigPayload := strings.Repeat("A", 5000)
	askOut := `{"ok":true,"content_b64":"` + bigPayload + `"}`
	got := wasmResultLogLine("com.test.plugin", "export.requested.ask", askOut)
	if strings.Contains(got, bigPayload) {
		t.Fatalf("wasmResultLogLine did not redact a large field on an .ask event")
	}
	if !strings.HasPrefix(got, "[wasm:com.test.plugin] result (export.requested.ask, ") {
		t.Errorf("expected event type + byte count in the .ask log line, got: %s", got)
	}
}

// TestWasmResultLogLine_GatesOnAuthorizeSuffix proves ".authorize" events
// (payment-authorization plugins, PublishAuthorize/Blocking hooks) get the
// same redaction treatment as ".ask" events (ut-docs#245) — their responses
// are arguably the most credential-adjacent plugin output in the system.
func TestWasmResultLogLine_GatesOnAuthorizeSuffix(t *testing.T) {
	bigToken := strings.Repeat("A", 5000)
	authorizeOut := `{"approved":true,"auth_token":"` + bigToken + `"}`
	got := wasmResultLogLine("com.test.plugin", "payment.demopay.authorize", authorizeOut)
	if strings.Contains(got, bigToken) {
		t.Fatalf("wasmResultLogLine did not redact a large field on an .authorize event")
	}
	if !strings.HasPrefix(got, "[wasm:com.test.plugin] result (payment.demopay.authorize, ") {
		t.Errorf("expected event type + byte count in the .authorize log line, got: %s", got)
	}
}

// TestWasmResultLogLine_GatesOnRefundSuffix proves ".refund" events (the
// payment leg blockingPaymentEventWithResponse publishes for a refund, see
// refund_page.go) get the same redaction treatment as ".ask"/".authorize"
// events (ut-docs#385) — a refund plugin's response is just as likely to
// carry a gateway transaction token as an authorize response.
func TestWasmResultLogLine_GatesOnRefundSuffix(t *testing.T) {
	bigToken := strings.Repeat("A", 5000)
	refundOut := `{"approved":true,"auth_token":"` + bigToken + `"}`
	got := wasmResultLogLine("com.test.plugin", "payment.demopay.refund", refundOut)
	if strings.Contains(got, bigToken) {
		t.Fatalf("wasmResultLogLine did not redact a large field on a .refund event")
	}
	if !strings.HasPrefix(got, "[wasm:com.test.plugin] result (payment.demopay.refund, ") {
		t.Errorf("expected event type + byte count in the .refund log line, got: %s", got)
	}
}

// TestSafeAskResultForLog_RedactsOversizedField proves a large content_b64
// field on an ".ask" response never reaches the log verbatim (ut-docs#202,
// found during independent review of ut-docs#189 — export.requested.ask's
// full exported dataset was landing at INFO level in wasm_runtime.go).
// Small fields (ok, filename, message) must survive so the line stays
// useful for debugging.
func TestSafeAskResultForLog_RedactsOversizedField(t *testing.T) {
	bigPayload := strings.Repeat("A", 5000) // stands in for a real base64 export
	out := `{"ok":true,"filename":"export.csv","content_b64":"` + bigPayload + `","message":"count=2 sum=2000"}`

	got := safeAskResultForLog(out)

	if strings.Contains(got, bigPayload) {
		t.Fatalf("logged line still contains the full content_b64 payload verbatim")
	}
	for _, want := range []string{`"ok":true`, `"filename":"export.csv"`, `"message":"count=2 sum=2000"`} {
		if !strings.Contains(got, want) {
			t.Errorf("logged line missing %s, still needed for debugging: %s", want, got)
		}
	}
	if !strings.Contains(got, "5002 bytes") { // raw JSON value includes the surrounding quotes
		t.Errorf("logged line should note the omitted field's size, got: %s", got)
	}
	if len(got) > 500 {
		t.Errorf("logged line still large after redaction: %d bytes", len(got))
	}
}

// TestSafeAskResultForLog_RedactsBlobFieldByNameEvenWhenSmall proves a
// SMALL content_b64 is still redacted — byte-size alone isn't the risk
// here (the ticket that filed ut-docs#202 is GDPR-adjacent): a two-line
// export can still carry a real customer's name/email, and that must never
// reach the log just because the blob happened to be small.
func TestSafeAskResultForLog_RedactsBlobFieldByNameEvenWhenSmall(t *testing.T) {
	// base64 of "date,customer,email,total\n2026-08-01,John Smith,john@example.com,20.00"
	const smallPIIBlob = "ZGF0ZSxjdXN0b21lcixlbWFpbCx0b3RhbApuMjAyNi0wOC0wMSxKb2huIFNtaXRoLGpvaG5AZXhhbXBsZS5jb20sMjAuMDA="
	out := `{"ok":true,"content_b64":"` + smallPIIBlob + `"}`

	got := safeAskResultForLog(out)

	if strings.Contains(got, smallPIIBlob) {
		t.Fatalf("a small content_b64 field was logged verbatim -- still leaks PII even though it's small: %s", got)
	}
	if !strings.Contains(got, `"ok":true`) {
		t.Errorf("logged line should still show the small ok field, got: %s", got)
	}
}

// TestSafeAskResultForLog_RedactsTokenOrSecretFieldByNameEvenWhenSmall
// proves a small auth token/secret field is redacted by name, the same way
// a small content_b64 already is (ut-docs#245): a payment-authorize
// response's token is exactly as much of a credential when it's short as
// when it's long, so size alone can't be the only redaction trigger.
func TestSafeAskResultForLog_RedactsTokenOrSecretFieldByNameEvenWhenSmall(t *testing.T) {
	for _, field := range []string{"auth_token", "client_secret"} {
		out := `{"approved":true,"` + field + `":"tok_live_abc123"}`
		got := safeAskResultForLog(out)
		if strings.Contains(got, "tok_live_abc123") {
			t.Fatalf("a small %s field was logged verbatim: %s", field, got)
		}
		if !strings.Contains(got, `"approved":true`) {
			t.Errorf("logged line should still show the small approved field, got: %s", got)
		}
	}
}

// TestSafeAskResultForLog_RedactsNestedTokenFieldByName proves a credential
// nested under another key -- e.g. a payment-gateway response shaped like
// {"approved":true,"provider":{"auth_token":"…"}} -- is redacted the same
// way a top-level auth_token is (ut-docs#384). Before this fix,
// safeAskResultForLog only inspected top-level keys: "provider" itself
// doesn't match looksLikeSensitiveFieldName and its raw JSON value can stay
// under maxAskFieldBytes, so the nested token reached the log verbatim.
func TestSafeAskResultForLog_RedactsNestedTokenFieldByName(t *testing.T) {
	out := `{"approved":true,"provider":{"auth_token":"tok_live_abc123"}}`

	got := safeAskResultForLog(out)

	if strings.Contains(got, "tok_live_abc123") {
		t.Fatalf("a nested auth_token field was logged verbatim: %s", got)
	}
	if !strings.Contains(got, `"approved":true`) {
		t.Errorf("logged line should still show the small top-level approved field, got: %s", got)
	}
	if !strings.Contains(got, "omitted") {
		t.Errorf("expected a size placeholder for the redacted nested field, got: %s", got)
	}
}

// TestSafeAskResultForLog_RedactsTokenInEmbeddedJSONString proves a
// credential nested inside a field whose raw JSON value is itself a JSON
// *string* containing an embedded object -- e.g.
// {"approved":true,"provider":"{\"auth_token\":\"…\"}"} -- is redacted the
// same way a directly-nested object is (ut-docs#384). Before this fix,
// redactField only recursed when a field's value unmarshaled as an object
// or array; a JSON string whose *content* happens to be JSON (one encoding
// layer removed, as some payment-gateway SDKs proxy a raw upstream response)
// unmarshaled as neither, so recursion never triggered and the embedded
// credential reached the log verbatim (ut-docs#393).
func TestSafeAskResultForLog_RedactsTokenInEmbeddedJSONString(t *testing.T) {
	out := `{"approved":true,"provider":"{\"auth_token\":\"tok_live_abc123\"}"}`

	got := safeAskResultForLog(out)

	if strings.Contains(got, "tok_live_abc123") {
		t.Fatalf("an auth_token embedded in a JSON-string field was logged verbatim: %s", got)
	}
	if !strings.Contains(got, `"approved":true`) {
		t.Errorf("logged line should still show the small top-level approved field, got: %s", got)
	}
	if !strings.Contains(got, "omitted") {
		t.Errorf("expected a size placeholder for the redacted embedded field, got: %s", got)
	}

	// The redacted "provider" field must still be a JSON string (its outer
	// type preserved), not an object -- callers unmarshaling the logged
	// shape (or a human reading it) shouldn't see the field's type change.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(got), &fields); err != nil {
		t.Fatalf("redacted output is not valid JSON: %v\n%s", err, got)
	}
	var providerAsString string
	if err := json.Unmarshal(fields["provider"], &providerAsString); err != nil {
		t.Fatalf("redacted \"provider\" field is no longer a JSON string (outer type not preserved): %v\n%s", err, got)
	}
}

// TestSafeAskResultForLog_LeavesPlainJSONStringFieldAlone proves a field
// whose value merely LOOKS parseable one way (e.g. a numeric or quoted
// string once decoded) but isn't a nested object/array doesn't trip the new
// embedded-JSON-string recursion added for ut-docs#393 -- only an embedded
// object/array is worth descending into.
func TestSafeAskResultForLog_LeavesPlainJSONStringFieldAlone(t *testing.T) {
	out := `{"ok":true,"note":"42"}`
	if got := safeAskResultForLog(out); got != out {
		t.Errorf("plain non-nested string field should be unchanged, got %q want %q", got, out)
	}
}

// TestSafeAskResultForLog_RedactsTokenInDoublyEmbeddedJSONString proves a
// credential still can't hide behind TWO layers of JSON-string encoding --
// a string containing a string containing an object -- not just one.
// Independent review of an earlier draft of ut-docs#393's fix found that
// gating the single-layer unwrap on the decoded shape being an
// object/array stopped recursion after exactly one layer: each extra
// escaping layer only adds a few dozen bytes (measured: 32B raw -> 38B
// once-encoded -> 50B twice-encoded), so a credential proxied through more
// than one layer of serialization -- plausible for the same payment/
// gateway-SDK reason this card exists at all -- stayed comfortably under
// maxAskFieldBytes at every layer and leaked verbatim. redactField now
// recurses into an embedded JSON string unconditionally rather than
// pre-checking its decoded shape, so each layer's own branch (object,
// array, string, or scalar) decides what to do with its own content,
// cascading through however many layers actually exist (bounded by
// maxAskNestingDepth, same as object/array recursion already was).
func TestSafeAskResultForLog_RedactsTokenInDoublyEmbeddedJSONString(t *testing.T) {
	inner := `{"auth_token":"tok_live_abc123"}`
	onceEncoded, err := json.Marshal(inner)
	if err != nil {
		t.Fatalf("test setup: %v", err)
	}
	twiceEncoded, err := json.Marshal(string(onceEncoded))
	if err != nil {
		t.Fatalf("test setup: %v", err)
	}
	out := `{"approved":true,"provider":` + string(twiceEncoded) + `}`

	got := safeAskResultForLog(out)

	if strings.Contains(got, "tok_live_abc123") {
		t.Fatalf("an auth_token embedded two JSON-string layers deep was logged verbatim: %s", got)
	}
	if !strings.Contains(got, `"approved":true`) {
		t.Errorf("logged line should still show the small top-level approved field, got: %s", got)
	}
	if !strings.Contains(got, "omitted") {
		t.Errorf("expected a size placeholder for the redacted embedded field, got: %s", got)
	}

	// The redacted "provider" field must still be a JSON string one level
	// down too -- unwrapping twice should re-wrap twice, preserving the
	// original double-string shape, not collapse it to a single string or
	// a bare object.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(got), &fields); err != nil {
		t.Fatalf("redacted output is not valid JSON: %v\n%s", err, got)
	}
	var onceUnwrapped string
	if err := json.Unmarshal(fields["provider"], &onceUnwrapped); err != nil {
		t.Fatalf("redacted \"provider\" field is no longer a JSON string (outer type not preserved): %v\n%s", err, got)
	}
	var twiceUnwrapped string
	if err := json.Unmarshal([]byte(onceUnwrapped), &twiceUnwrapped); err != nil {
		t.Fatalf("redacted \"provider\" field's inner layer is no longer a JSON string (double-string shape not preserved): %v\n%s", err, onceUnwrapped)
	}
}

// TestSafeAskResultForLog_RedactsParentWhoseNestedContentIsOversized proves
// a nested object containing an oversized field still can't reach the log
// verbatim. NOTE (found by independent review of ut-docs#384): a child's
// raw JSON is always a subset of its parent's, so if a nested field alone
// is large enough to push its PARENT's raw JSON over maxAskFieldBytes, the
// parent gets redacted wholesale by the pre-existing top-level/per-field
// size check before recursion ever reaches the child -- redactField's own
// size check at depth>=1 is therefore only ever reachable for a nested
// field that's oversized on its OWN while its ancestors all stay under the
// cap (a much narrower case than this test's payload). This test still
// documents and guards real, correct behavior (the payload never leaks
// either way); it just doesn't isolate the recursive-size branch the way
// its previous name implied.
func TestSafeAskResultForLog_RedactsParentWhoseNestedContentIsOversized(t *testing.T) {
	bigPayload := strings.Repeat("A", 5000)
	out := `{"approved":true,"provider":{"raw_response":"` + bigPayload + `"}}`

	got := safeAskResultForLog(out)

	if strings.Contains(got, bigPayload) {
		t.Fatalf("a large nested field was logged verbatim: %s", got)
	}
	if !strings.Contains(got, `"approved":true`) {
		t.Errorf("logged line should still show the small top-level approved field, got: %s", got)
	}
}

// TestSafeAskResultForLog_RedactsTokenInArrayOfObjects proves an
// array-of-objects field is walked the same way a nested object is -- a
// report/export-shaped response can return a list of per-row objects, any
// of which could carry a sensitive field.
func TestSafeAskResultForLog_RedactsTokenInArrayOfObjects(t *testing.T) {
	out := `{"ok":true,"charges":[{"id":"ch_1","client_secret":"cs_live_abc123"},{"id":"ch_2"}]}`

	got := safeAskResultForLog(out)

	if strings.Contains(got, "cs_live_abc123") {
		t.Fatalf("a client_secret nested inside an array element was logged verbatim: %s", got)
	}
	for _, want := range []string{`"ok":true`, `"id":"ch_1"`, `"id":"ch_2"`} {
		if !strings.Contains(got, want) {
			t.Errorf("logged line missing %s, still needed for debugging: %s", want, got)
		}
	}
}

// TestSafeAskResultForLog_FlatShapeRedactionUnchanged is a direct
// before/after guard for the flat-shape behavior safeAskResultForLog had
// prior to ut-docs#384's recursive rewrite -- proves the existing top-level
// redaction rules (oversized field, sensitively-named field, small
// response left untouched) still hold byte-for-byte.
func TestSafeAskResultForLog_FlatShapeRedactionUnchanged(t *testing.T) {
	if got, want := safeAskResultForLog(`{"rate_bp":2000}`), `{"rate_bp":2000}`; got != want {
		t.Errorf("flat small response changed: got %q want %q", got, want)
	}
	out := `{"approved":true,"auth_token":"tok_live_abc123"}`
	got := safeAskResultForLog(out)
	if strings.Contains(got, "tok_live_abc123") {
		t.Fatalf("flat top-level auth_token no longer redacted: %s", got)
	}
	if !strings.Contains(got, `"approved":true`) {
		t.Errorf("flat top-level approved field should survive redaction, got: %s", got)
	}
}

// TestSafeAskResultForLog_CapsOverallSizeWhenManySmallFieldsAddUp proves
// many individually-small fields (each under maxAskFieldBytes, so none is
// redacted on its own) can't sum to an unbounded log line -- e.g. a report
// plugin answering one field per sale row.
func TestSafeAskResultForLog_CapsOverallSizeWhenManySmallFieldsAddUp(t *testing.T) {
	fields := make([]string, 400)
	for i := range fields {
		fields[i] = `"row_` + strconv.Itoa(i) + `":"` + strings.Repeat("v", 150) + `"`
	}
	out := "{" + strings.Join(fields, ",") + "}"

	got := safeAskResultForLog(out)

	if len(got) > 600 { // maxAskLogBytes (500) plus the truncation suffix
		t.Errorf("many-small-fields response was not capped: %d bytes", len(got))
	}
	if len(out) <= len(got) {
		t.Fatalf("test setup didn't produce a response larger than the cap: %d bytes", len(out))
	}
}

// TestSafeAskResultForLog_LeavesSmallResponseUnchanged proves a normal-sized
// ".ask" answer (e.g. tax.rate.ask's {"rate_bp":N}) keeps its existing log
// behavior — this fix must not degrade debugging of the common case.
func TestSafeAskResultForLog_LeavesSmallResponseUnchanged(t *testing.T) {
	out := `{"rate_bp":2000}`
	if got := safeAskResultForLog(out); got != out {
		t.Errorf("small response should be unchanged, got %q want %q", got, out)
	}
}

// TestSafeAskResultForLog_FallsBackToTruncationForNonObjectOutput proves
// non-JSON-object ".ask" output (a plugin bug, or a handler that doesn't
// answer in the expected shape) still can't dump an unbounded blob into the
// log — it's hard-truncated instead of redacted field-by-field.
func TestSafeAskResultForLog_FallsBackToTruncationForNonObjectOutput(t *testing.T) {
	bigPayload := strings.Repeat("B", 5000)
	got := safeAskResultForLog(bigPayload)

	if strings.Contains(got, bigPayload) {
		t.Fatalf("logged line still contains the full non-JSON payload verbatim")
	}
	if !strings.Contains(got, "5000 bytes total") {
		t.Errorf("truncated line should note the total size, got: %s", got)
	}
}

// TestSafeAskResultForLog_TruncationIsValidUTF8 proves a truncated line
// never splits a multi-byte rune (e.g. an RTL locale's export filename) —
// a plain byte-slice cut can produce invalid UTF-8 that garbles the log.
func TestSafeAskResultForLog_TruncationIsValidUTF8(t *testing.T) {
	// Multi-byte runes (€, 3 bytes each in UTF-8) straddling likely cut
	// points around maxAskLogBytes.
	bigPayload := "x" + strings.Repeat("€", 400)
	got := safeAskResultForLog(bigPayload)

	if !utf8.ValidString(got) {
		t.Fatalf("truncated output is not valid UTF-8: %q", got)
	}
}
